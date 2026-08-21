package main

// The Compose engine is deliberately a deep module.  Its caller supplies an
// immutable checked-out workspace and a resolved deployment plan; command
// invocation, policy-safe arguments, and health polling remain private here.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/domain"
)

type composeCommand interface {
	Run(context.Context, string, []string, string) ([]byte, error)
}

type composeHealth interface {
	Check(context.Context, composeHealthContract) error
}

type composeHealthContract struct {
	URL              string
	ExpectedRevision string
	Interval         time.Duration
	Timeout          time.Duration
	ExpectedStatus   int
	Host             string
	Port             int
	AllowedCIDRs     []netip.Prefix
	AllowHTTP        bool
}

type composeEngine struct {
	command composeCommand
	health  composeHealth
}

type composeReconciliationState struct{ Project, Commit, ComposeHash string }

// deploymentRevisionEnv is injected only by the runner. A checked-out Compose
// file may pass it to its health endpoint, but cannot choose its value.
const deploymentRevisionEnv = "NEROCD_DEPLOYMENT_REVISION"

func newComposeEngine(command composeCommand, health composeHealth) composeEngine {
	if command == nil {
		command = osProvenanceCommand{}
	}
	if health == nil {
		health = httpComposeHealth{}
	}
	return composeEngine{command: command, health: health}
}

// Apply performs only the external mutation half.  It deliberately receives
// no secret values and creates its own empty env file so checkout-controlled
// .env files and ambient process credentials cannot alter Compose behavior.
func (e composeEngine) Apply(ctx context.Context, plan domain.DeploymentPlan, workspace string, resolved resolvedProvenance) error {
	args, cleanup, err := composeInvocation(plan, workspace, resolved.GitCommit)
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err = e.command.Run(ctx, "docker", append(append([]string{}, args...), "up", "--pull", "never", "--detach", "--remove-orphans", "--force-recreate"), workspace); err != nil {
		return commandFailure("docker compose apply", err)
	}
	// This read is intentionally before health mutation/reporting. Compose
	// labels are derived from the server-owned project name, so it is a safe,
	// idempotent restart/retry reconciliation point.
	ps, err := e.command.Run(ctx, "docker", append(append([]string{}, args...), "ps", "--format", "json"), workspace)
	if err != nil {
		return commandFailure("docker compose reconcile", err)
	}
	if err := parseComposePS(ps); err != nil {
		return err
	}
	if err := writeComposeReconciliationState(workspace, composeReconciliationState{Project: plan.ComposeProject, Commit: resolved.GitCommit, ComposeHash: resolved.ComposeHash}); err != nil {
		return err
	}
	return nil
}

// Reconcile reads durable, non-secret per-environment state and asks Docker
// for the controlled project before any mutation. A matching verified state is
// a retry/restart no-op; a missing or changed identity proceeds to Apply.
func (e composeEngine) Reconcile(ctx context.Context, plan domain.DeploymentPlan, workspace string, resolved resolvedProvenance) (bool, error) {
	args, cleanup, err := composeInvocation(plan, workspace, resolved.GitCommit)
	if err != nil {
		return false, err
	}
	defer cleanup()
	state, found, err := readComposeReconciliationState(workspace)
	if err != nil {
		return false, err
	}
	if !found || state.Project != plan.ComposeProject || state.Commit != resolved.GitCommit || state.ComposeHash != resolved.ComposeHash {
		return false, nil
	}
	ps, err := e.command.Run(ctx, "docker", append(args, "ps", "--format", "json"), workspace)
	if err != nil {
		return false, commandFailure("docker compose reconcile", err)
	}
	if err := parseComposePS(ps); err != nil {
		return false, err
	}
	return true, nil
}

func composeStatePath(workspace string) string {
	return filepath.Join(filepath.Dir(workspace), ".nerocd-compose-state")
}
func readComposeReconciliationState(workspace string) (composeReconciliationState, bool, error) {
	root := composeStatePath(workspace)
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return composeReconciliationState{}, false, nil
	}
	if err != nil {
		return composeReconciliationState{}, false, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return composeReconciliationState{}, false, errors.New("compose reconciliation state directory is unsafe")
	}
	raw, err := os.ReadFile(filepath.Join(root, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return composeReconciliationState{}, false, nil
	}
	if err != nil || len(raw) > 4096 {
		return composeReconciliationState{}, false, errors.New("compose reconciliation state is invalid")
	}
	var state composeReconciliationState
	if json.Unmarshal(raw, &state) != nil || !composeProjectName(state.Project) || !isHexCommit(state.Commit) || !strings.HasPrefix(state.ComposeHash, "sha256:") {
		return composeReconciliationState{}, false, errors.New("compose reconciliation state is invalid")
	}
	return state, true, nil
}
func writeComposeReconciliationState(workspace string, state composeReconciliationState) error {
	root := composeStatePath(workspace)
	if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("compose reconciliation state directory is unsafe")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(root, ".state-")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0600); err == nil {
		_, err = temp.Write(raw)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(root, "state.json"))
}

// EnsureAvailability is a read-only local image inspection. Pulling is an
// explicit operator-controlled supply-chain operation, never an incidental
// side effect of a deployment retry.
func (e composeEngine) EnsureAvailability(ctx context.Context, workspace string, resolved resolvedProvenance) error {
	if len(resolved.ImageDigests) == 0 {
		return errors.New("resolved deployment has no image digests")
	}
	for _, digest := range resolved.ImageDigests {
		if _, err := e.command.Run(ctx, "docker", []string{"image", "inspect", digest}, workspace); err != nil {
			return fmt.Errorf("resolved image unavailable: %w", err)
		}
	}
	return nil
}

func (e composeEngine) Verify(ctx context.Context, plan domain.DeploymentPlan, resolved resolvedProvenance) error {
	contract, err := healthContract(plan.HealthPolicy, resolved.GitCommit, plan.TimeoutSeconds)
	if err != nil {
		return err
	}
	return e.health.Check(ctx, contract)
}

func composeInvocation(plan domain.DeploymentPlan, workspace, revision string) ([]string, func(), error) {
	if !composeProjectName(plan.ComposeProject) {
		return nil, nil, errors.New("invalid server-owned compose project name")
	}
	if !isHexCommit(revision) {
		return nil, nil, errors.New("invalid resolved deployment revision")
	}
	composePath := filepath.Clean(plan.ComposePath)
	if filepath.IsAbs(composePath) || composePath == "." || strings.HasPrefix(composePath, ".."+string(os.PathSeparator)) {
		return nil, nil, errors.New("compose path escapes immutable workspace")
	}
	if len(plan.SecretBindings) > 0 {
		// Compose env interpolation is a mutation-prone secret transport. The
		// typed adapter admits runner_file bindings for controlled Git only; a
		// future Compose secrets adapter must use descriptor-confined files.
		for _, binding := range plan.SecretBindings {
			if strings.EqualFold(strings.TrimSpace(binding.Provider), domain.ProviderRunnerFile) && strings.TrimSpace(binding.Reference) == strings.TrimSpace(plan.RepositoryPolicy.CredentialReferenceID) {
				continue
			}
			return nil, nil, errors.New("compose deployment secret injection is not supported by the production adapter")
		}
	}
	envFile := filepath.Join(workspace, ".nerocd-compose-empty.env")
	if err := os.WriteFile(envFile, []byte(deploymentRevisionEnv+"="+revision+"\n"), 0600); err != nil {
		return nil, nil, err
	}
	args := []string{"compose", "--project-name", plan.ComposeProject, "--env-file", envFile, "--file", composePath}
	for _, profile := range plan.Profiles {
		if strings.TrimSpace(profile) == "" || strings.ContainsAny(profile, "\x00\r\n") {
			_ = os.Remove(envFile)
			return nil, nil, errors.New("invalid compose profile")
		}
		args = append(args, "--profile", profile)
	}
	return args, func() { _ = os.Remove(envFile) }, nil
}

func healthContract(raw domain.HealthPolicy, commit string, timeoutSeconds int) (composeHealthContract, error) {
	value := raw.URL
	if strings.TrimSpace(value) == "" {
		return composeHealthContract{}, errors.New("typed compose health policy requires url")
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return composeHealthContract{}, errors.New("typed compose health url is invalid")
	}
	host := canonicalHealthHost(u.Hostname())
	if host == "" {
		return composeHealthContract{}, errors.New("typed compose health url is invalid")
	}
	approved := raw.AllowedHosts
	if len(approved) == 0 {
		return composeHealthContract{}, errors.New("typed compose health policy requires allowed_hosts")
	}
	hostApproved := false
	for _, rawHost := range approved {
		if canonicalHealthHost(rawHost) != "" && canonicalHealthHost(rawHost) == host {
			hostApproved = true
		}
	}
	if !hostApproved {
		return composeHealthContract{}, errors.New("typed compose health destination is not allowed")
	}
	allowedCIDRs, err := healthCIDRs(raw.AllowedCIDRs)
	if err != nil {
		return composeHealthContract{}, errors.New("typed compose health address policy is invalid")
	}
	allowHTTP := raw.AllowHTTP
	if u.Scheme == "http" && (!allowHTTP || len(allowedCIDRs) == 0) {
		// Plain HTTP is intentionally confined to an explicitly configured
		// internal fixture/network. Production defaults to HTTPS.
		return composeHealthContract{}, errors.New("typed compose health http is not explicitly allowed")
	}
	port, err := healthPort(u)
	if err != nil {
		return composeHealthContract{}, errors.New("typed compose health url is invalid")
	}
	if err := healthPortAllowed(raw.AllowedPorts, port, u.Scheme); err != nil {
		return composeHealthContract{}, errors.New("typed compose health port is not allowed")
	}
	expected := raw.ExpectedRevision
	if expected == "" {
		expected = commit
	}
	if expected != commit {
		return composeHealthContract{}, errors.New("health policy expected_revision does not match resolved revision")
	}
	interval := durationPolicy(raw.IntervalSeconds, time.Second)
	timeout := durationPolicy(raw.TimeoutSeconds, time.Duration(timeoutSeconds)*time.Second)
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if interval <= 0 || interval > timeout {
		return composeHealthContract{}, errors.New("typed compose health timing is invalid")
	}
	status := 200
	if raw.ExpectedStatus != 0 {
		status = raw.ExpectedStatus
	}
	if status < 200 || status > 299 {
		return composeHealthContract{}, errors.New("typed compose health expected_status must be 2xx")
	}
	return composeHealthContract{URL: u.String(), ExpectedRevision: expected, Interval: interval, Timeout: timeout, ExpectedStatus: status, Host: host, Port: port, AllowedCIDRs: allowedCIDRs, AllowHTTP: allowHTTP}, nil
}

// canonicalHealthHost deliberately accepts only a DNS name or literal IP.
// It avoids treating URL syntax as a hostname policy token.
func canonicalHealthHost(value string) string {
	v := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if v == "" {
		return ""
	}
	if _, err := netip.ParseAddr(v); err == nil {
		return v
	}
	if strings.ContainsAny(v, ":/@?#%[]\\\x00\r\n") {
		return ""
	}
	for _, label := range strings.Split(v, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, r := range label {
			if !(r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				return ""
			}
		}
	}
	return v
}

func healthCIDRs(values []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil || !prefix.Addr().IsValid() {
			return nil, errors.New("invalid cidr")
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}

func healthPort(u *url.URL) (int, error) {
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return 0, errors.New("invalid port")
		}
		return port, nil
	}
	if u.Scheme == "https" {
		return 443, nil
	}
	return 80, nil
}

func healthPortAllowed(ports []int, port int, scheme string) error {
	if (scheme == "https" && port == 443) || (scheme == "http" && port == 80) {
		return nil
	}
	for _, allowed := range ports {
		if allowed == port && allowed > 0 && allowed <= 65535 {
			return nil
		}
	}
	return errors.New("not allowed")
}

func durationPolicy(value float64, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return time.Duration(value * float64(time.Second))
}

var errComposeHealth = errors.New("typed compose health verification failed")

// httpComposeHealth owns DNS admission and dialing as one operation. The
// seams are deliberately private: production gets the system resolver and
// dialer, while tests can prove rebind behavior without ambient DNS.
type httpComposeHealth struct {
	lookup    func(context.Context, string) ([]netip.Addr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	tlsConfig func(string) *tls.Config
}

func (h httpComposeHealth) resolver() func(context.Context, string) ([]netip.Addr, error) {
	if h.lookup != nil {
		return h.lookup
	}
	return func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
}
func (h httpComposeHealth) dialer() func(context.Context, string, string) (net.Conn, error) {
	if h.dial != nil {
		return h.dial
	}
	d := net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}
	return d.DialContext
}
func (h httpComposeHealth) tlsConfiguration(host string) *tls.Config {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if h.tlsConfig != nil {
		if configured := h.tlsConfig(host); configured != nil {
			config = configured.Clone()
			if config.MinVersion < tls.VersionTLS12 {
				config.MinVersion = tls.VersionTLS12
			}
			config.ServerName = host
		}
	}
	return config
}

func (h httpComposeHealth) Check(ctx context.Context, contract composeHealthContract) error {
	deadlineCtx, cancel := context.WithTimeout(ctx, contract.Timeout)
	defer cancel()
	for {
		// Resolve immediately before each individual request. Every answer must
		// be admitted; accepting only the first one enables mixed-answer rebinding.
		addresses, err := h.resolver()(deadlineCtx, contract.Host)
		if err != nil || len(addresses) == 0 || !healthAddressesAllowed(addresses, contract.AllowedCIDRs, contract.AllowHTTP) {
			return errComposeHealth
		}
		selected := addresses[0].Unmap()
		dialTarget := net.JoinHostPort(selected.String(), strconv.Itoa(contract.Port))
		transport := &http.Transport{
			Proxy: nil,
			DialContext: func(requestCtx context.Context, network, _ string) (net.Conn, error) {
				return h.dialer()(requestCtx, network, dialTarget)
			},
			TLSClientConfig:       h.tlsConfiguration(contract.Host),
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			ExpectContinueTimeout: time.Second,
			IdleConnTimeout:       time.Second,
			DisableKeepAlives:     true,
			ForceAttemptHTTP2:     false,
		}
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequestWithContext(deadlineCtx, http.MethodGet, contract.URL, nil)
		if err != nil {
			return errComposeHealth
		}
		// DialContext pins the IP but leaving URL/Host untouched preserves the
		// configured virtual-host Host header and HTTPS SNI.
		req.Host = req.URL.Host
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			if resp.StatusCode == contract.ExpectedStatus && resp.Header.Get("X-NeroCD-Revision") == contract.ExpectedRevision {
				return nil
			}
		}
		select {
		case <-deadlineCtx.Done():
			return errComposeHealth
		case <-time.After(contract.Interval):
		}
	}
}

func healthAddressesAllowed(addresses []netip.Addr, allowedCIDRs []netip.Prefix, requireExplicitCIDR bool) bool {
	for _, raw := range addresses {
		address := raw.Unmap()
		if !address.IsValid() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() || address.IsPrivate() {
			if !healthAddressInCIDRs(address, allowedCIDRs) {
				return false
			}
		}
		if requireExplicitCIDR && !healthAddressInCIDRs(address, allowedCIDRs) {
			return false
		}
	}
	return true
}

func healthAddressInCIDRs(address netip.Addr, allowedCIDRs []netip.Prefix) bool {
	for _, prefix := range allowedCIDRs {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// parseComposePS makes the reconciliation assertion testable without exposing
// docker output to durable runner logs.
func parseComposePS(raw []byte) error {
	if len(raw) == 0 {
		return errors.New("compose reconciliation returned no containers")
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		// Compose implementations have emitted both a JSON array and one JSON
		// object per line for --format json. Accept either bounded structured
		// representation, never unstructured command output.
		var row map[string]any
		if objectErr := json.Unmarshal(raw, &row); objectErr != nil || len(row) == 0 {
			return errors.New("compose reconciliation output is invalid")
		}
		rows = []map[string]any{row}
	}
	if len(rows) == 0 {
		return errors.New("compose reconciliation found no managed containers")
	}
	return nil
}
