package hostingproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

// PrepareManaged probes only the configured Unix daemon and compiles typed input.
// It performs no installation or routing mutation. A supplied daemon identity
// must match; an initial empty identity is resolved before controller preparation.
func (e *Engine) PrepareManaged(ctx context.Context, identity Identity, generation, previous uint64, routes []Route) (plan Plan, daemonID string, err error) {
	return e.prepareManaged(ctx, identity, generation, previous, routes, nil)
}

// PrepareManagedVerifiedSwitch probes the same pinned daemon and compiles strict intent.
// It never falls back to a legacy plan when verification inputs are invalid.
func (e *Engine) PrepareManagedVerifiedSwitch(ctx context.Context, identity Identity, generation, previous uint64, routes []Route, requiredRouteIDs []string) (Plan, string, error) {
	if len(requiredRouteIDs) == 0 {
		return Plan{}, "", errors.New("verified switch requires selected routes")
	}
	return e.prepareManaged(ctx, identity, generation, previous, routes, requiredRouteIDs)
}

func (e *Engine) prepareManaged(ctx context.Context, identity Identity, generation, previous uint64, routes []Route, requiredRouteIDs []string) (plan Plan, daemonID string, err error) {
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", e.settings.DockerSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/info", nil)
	if err != nil {
		return Plan{}, "", err
	}
	response, err := client.Do(req)
	if err != nil {
		return Plan{}, "", errors.New("configured proxy Docker daemon unavailable")
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	var daemon struct{ ID, OSType, Architecture string }
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&daemon) != nil || daemon.ID == "" || daemon.OSType != "linux" || (daemon.Architecture != "aarch64" && daemon.Architecture != "x86_64" && daemon.Architecture != "arm64" && daemon.Architecture != "amd64") {
		return Plan{}, "", errors.New("configured proxy daemon has unsupported identity or platform")
	}
	if identity.DaemonID != "" && identity.DaemonID != daemon.ID {
		return Plan{}, "", errors.New("configured proxy Docker daemon identity changed")
	}
	identity.DaemonID = daemon.ID
	if requiredRouteIDs == nil {
		plan, err = Prepare(identity, e.settings, generation, previous, routes)
	} else {
		plan, err = PrepareVerifiedSwitch(identity, e.settings, generation, previous, routes, requiredRouteIDs)
	}
	if err != nil {
		return Plan{}, "", err
	}
	return plan, daemon.ID, nil
}

// ValidateManagedRoot creates or verifies the trusted owner-only state root.
// It does not open a proxy generation or mutate Docker. Non-Linux hosts refuse.
func ValidateManagedRoot(directory string) error {
	if err := platform(); err != nil {
		return err
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("managed proxy root must be an absolute canonical path")
	}
	return secureDirectory(directory)
}

// VerifyManagedReceipt compares a settled result to the daemon's owned receipt
// and loaded routing generation. It never retries an effect or clears an intent.
func (e *Engine) VerifyManagedReceipt(ctx context.Context, plan Plan, outcome Outcome, generation uint64) error {
	if err := e.validate(plan); err != nil {
		return err
	}
	if outcome == Uncertain || (outcome != Applied && outcome != RolledBack && outcome != Refused) {
		return errors.New("proxy result is unresolved")
	}
	if outcome == Applied && generation != plan.generation || outcome != Applied && generation != plan.previous {
		return errors.New("proxy result generation changed")
	}
	state, err := openStorage(ctx, e.settings.Directory)
	if err != nil {
		return err
	}
	defer state.close()
	local, err := state.load()
	if err != nil {
		return err
	}
	if err = e.daemon(ctx, plan); err != nil {
		return err
	}
	if generation == 0 && outcome == Refused && local.Generation == 0 && local.Phase == "" {
		return nil
	}
	if local.Generation != generation || local.Phase != "committed" || local.Identity != plan.identity || local.StaticHash != plan.staticHash || outcome == Applied && local.Hash != plan.hash {
		return errors.New("local proxy receipt does not match settled result")
	}
	previous := plan
	previous.generation = local.Generation
	previous.hash = local.Hash
	previous.routes = append([]Route(nil), local.Routes...)
	container, err := e.find(ctx, previous)
	if err != nil {
		return err
	}
	if container == nil {
		return errors.New("settled proxy container is missing")
	}
	if err = state.checkFile("certificates/acme.json"); err != nil {
		return err
	}
	return e.loaded(ctx, previous)
}
