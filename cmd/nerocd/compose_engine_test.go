package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
)

type fakeComposeCommand struct {
	calls  [][]string
	failAt string
}

func (f *fakeComposeCommand) Run(_ context.Context, _ string, args []string, _ string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	for _, a := range args {
		if a == f.failAt {
			return nil, errors.New("failed")
		}
	}
	if len(args) > 0 && args[len(args)-1] == "json" {
		return []byte(`[{"Name":"managed"}]`), nil
	}
	return nil, nil
}

type fakeComposeHealth struct {
	calls int
	err   error
}

func (f *fakeComposeHealth) Check(_ context.Context, _ composeHealthContract) error {
	f.calls++
	return f.err
}

func composeTestPlan() domain.DeploymentPlan {
	return domain.DeploymentPlan{ComposeProject: "project_prod", ComposePath: "compose.yaml", TimeoutSeconds: 5, HealthPolicy: domain.HealthPolicy{URL: "http://127.0.0.1:8080/health", AllowedHosts: []string{"127.0.0.1"}, AllowedCIDRs: []string{"127.0.0.0/8"}, AllowedPorts: []int{8080}, AllowHTTP: true, ExpectedRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
}
func composeTestResolved() resolvedProvenance {
	return resolvedProvenance{GitCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ComposeHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ImageDigests: []string{"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}
}

func TestComposeReconciliationIsDurableAndIdempotent(t *testing.T) {
	root, plan, resolved := t.TempDir(), composeTestPlan(), composeTestResolved()
	workspace, err := os.MkdirTemp(root, "attempt-")
	if err != nil {
		t.Fatal(err)
	}
	cmd := &fakeComposeCommand{}
	engine := newComposeEngine(cmd, &fakeComposeHealth{})
	if err := engine.Apply(context.Background(), plan, workspace, resolved); err != nil {
		t.Fatal(err)
	}
	before := len(cmd.calls)
	reconciled, err := engine.Reconcile(context.Background(), plan, workspace, resolved)
	if err != nil || !reconciled || len(cmd.calls) != before+1 {
		t.Fatalf("reconcile=%v calls=%d err=%v", reconciled, len(cmd.calls), err)
	}
	if _, found, err := readComposeReconciliationState(workspace); err != nil || !found {
		t.Fatalf("durable state found=%v err=%v", found, err)
	}
}

func TestComposeEngineStagesAreBoundedAndSecretFree(t *testing.T) {
	cmd, health := &fakeComposeCommand{}, &fakeComposeHealth{}
	root := t.TempDir()
	plan := composeTestPlan()
	if err := newComposeEngine(cmd, health).Apply(context.Background(), plan, root, composeTestResolved()); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 2 || cmd.calls[0][len(cmd.calls[0])-1] != "--force-recreate" || cmd.calls[1][len(cmd.calls[1])-1] != "json" {
		t.Fatalf("unexpected calls %#v", cmd.calls)
	}
	for _, call := range cmd.calls {
		if strings.Contains(strings.Join(call, " "), " pull") {
			t.Fatal("deployment performed implicit pull")
		}
	}
	for _, call := range cmd.calls {
		if strings.Contains(strings.Join(call, " "), "SECRET") {
			t.Fatal("secret reached compose invocation")
		}
	}
	if err := newComposeEngine(cmd, health).Verify(context.Background(), plan, composeTestResolved()); err != nil {
		t.Fatal(err)
	}
	if health.calls != 1 {
		t.Fatal("health was not called")
	}
}

func TestParseComposePSAcceptsComposeJSONArrayOrSingleObject(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`[{"Name":"managed"}]`), []byte(`{"Name":"managed"}`)} {
		if err := parseComposePS(raw); err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
	}
}

func TestComposeEngineRejectsMutationProneInputs(t *testing.T) {
	plan := composeTestPlan()
	plan.ComposeProject = "Bad Project"
	if _, cleanup, err := composeInvocation(plan, t.TempDir(), composeTestResolved().GitCommit); err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("expected invalid project rejection")
	}
	plan = composeTestPlan()
	plan.SecretBindings = []domain.SecretBinding{{Name: "db", Provider: domain.ProviderRunnerFile, Reference: "db", Target: "env:DB", Version: "v1"}}
	if _, cleanup, err := composeInvocation(plan, t.TempDir(), composeTestResolved().GitCommit); err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("expected secret injection rejection")
	}
}

func TestHealthContractRequiresResolvedRevisionAndBoundedTiming(t *testing.T) {
	plan := composeTestPlan()
	plan.HealthPolicy.ExpectedRevision = "other"
	if _, err := healthContract(plan.HealthPolicy, composeTestResolved().GitCommit, 5); err == nil {
		t.Fatal("expected revision mismatch")
	}
	plan = composeTestPlan()
	plan.HealthPolicy.IntervalSeconds = 10
	plan.HealthPolicy.TimeoutSeconds = 1
	if _, err := healthContract(plan.HealthPolicy, composeTestResolved().GitCommit, 5); err == nil {
		t.Fatal("expected timing rejection")
	}
	plan = composeTestPlan()
	plan.HealthPolicy.AllowedHosts = []string{"service.internal"}
	if _, err := healthContract(plan.HealthPolicy, composeTestResolved().GitCommit, 5); err == nil {
		t.Fatal("expected unapproved destination rejection")
	}
	for _, value := range []string{
		"ftp://health.example/", "https://user@health.example/", "https://health.example/?x=1",
		"https://health.example/#part", "https://health.example:0/", "https://health.example:65536/",
	} {
		policy := domain.HealthPolicy{URL: value, AllowedHosts: []string{"health.example"}}
		if _, err := healthContract(policy, composeTestResolved().GitCommit, 5); err == nil {
			t.Fatalf("unsafe URL %q was admitted", value)
		}
	}
	_ = time.Second
}

func healthTestContract(policy domain.HealthPolicy, url string) composeHealthContract {
	policy.URL = url
	policy.AllowedHosts = []string{"health.example"}
	policy.ExpectedRevision = composeTestResolved().GitCommit
	policy.IntervalSeconds = float64(5) / 1000
	policy.TimeoutSeconds = float64(100) / 1000
	contract, err := healthContract(policy, composeTestResolved().GitCommit, 1)
	if err != nil {
		panic(err)
	}
	return contract
}

func TestComposeHealthPinsValidatedPublicDNSAnswerAndPreservesHostAndSNI(t *testing.T) {
	var seenHost, seenSNI string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost, seenSNI = r.Host, r.TLS.ServerName
		w.Header().Set("X-NeroCD-Revision", composeTestResolved().GitCommit)
		w.WriteHeader(http.StatusOK)
	}))
	server.StartTLS()
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	contract := healthTestContract(domain.HealthPolicy{AllowedPorts: []int{port}}, "https://health.example:"+portText+"/health")
	var dialed string
	health := httpComposeHealth{
		lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		dial: func(ctx context.Context, network, target string) (net.Conn, error) {
			dialed = target
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
		tlsConfig: func(string) *tls.Config { return &tls.Config{InsecureSkipVerify: true} }, // test server certificate only
	}
	if err := health.Check(context.Background(), contract); err != nil {
		t.Fatal(err)
	}
	if dialed != net.JoinHostPort("8.8.8.8", portText) {
		t.Fatalf("dial target %q was not the admitted address", dialed)
	}
	if seenHost != "health.example:"+portText || seenSNI != "health.example" {
		t.Fatalf("Host/SNI = %q/%q", seenHost, seenSNI)
	}
}

func TestComposeHealthDeniesMixedOrPrivateDNSAnswersUnlessExplicitlyAdmitted(t *testing.T) {
	contract := composeHealthContract{Host: "health.example", Port: 443, AllowedCIDRs: nil}
	if healthAddressesAllowed([]netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, contract.AllowedCIDRs, false) {
		t.Fatal("mixed public/private answer was admitted")
	}
	if healthAddressesAllowed([]netip.Addr{netip.MustParseAddr("10.0.0.8")}, nil, false) {
		t.Fatal("private answer was admitted")
	}
	if !healthAddressesAllowed([]netip.Addr{netip.MustParseAddr("10.0.0.8")}, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, false) {
		t.Fatal("explicit private CIDR was rejected")
	}
	for _, address := range []string{"127.0.0.1", "169.254.169.254", "::1", "fe80::1", "fd00::1"} {
		if healthAddressesAllowed([]netip.Addr{netip.MustParseAddr(address)}, nil, false) {
			t.Fatalf("unsafe address %s was admitted", address)
		}
	}
}

func TestComposeHealthHTTPIsExplicitInternalFixtureOnly(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.Header().Set("X-NeroCD-Revision", composeTestResolved().GitCommit)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	raw := domain.HealthPolicy{AllowHTTP: true, AllowedCIDRs: []string{"127.0.0.0/8"}, AllowedPorts: []int{port}}
	contract := healthTestContract(raw, "http://health.example:"+portText+"/health")
	health := httpComposeHealth{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}, dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	if err := health.Check(context.Background(), contract); err != nil {
		t.Fatal(err)
	}
	if gotHost != "health.example:"+portText {
		t.Fatalf("Host = %q", gotHost)
	}
	if _, err := healthContract(domain.HealthPolicy{URL: "http://health.example/", AllowedHosts: []string{"health.example"}}, composeTestResolved().GitCommit, 1); err == nil {
		t.Fatal("unbounded HTTP was admitted")
	}
}

func TestComposeHealthRebindDoesNotReuseEarlierAdmission(t *testing.T) {
	var requests, lookups, dials atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-NeroCD-Revision", composeTestResolved().GitCommit)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	contract := healthTestContract(domain.HealthPolicy{AllowHTTP: true, AllowedCIDRs: []string{"8.8.8.0/24"}, AllowedPorts: []int{port}}, "http://health.example:"+portText+"/health")
	health := httpComposeHealth{
		lookup: func(context.Context, string) ([]netip.Addr, error) {
			if lookups.Add(1) == 1 {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
		dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	if err := health.Check(context.Background(), contract); !errors.Is(err, errComposeHealth) {
		t.Fatalf("err = %v", err)
	}
	if lookups.Load() != 2 || dials.Load() != 1 || requests.Load() != 1 {
		t.Fatalf("lookups=%d dials=%d requests=%d", lookups.Load(), dials.Load(), requests.Load())
	}
}

func TestComposeHealthBlocksRedirectsProxiesAndDisclosure(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	contract := healthTestContract(domain.HealthPolicy{AllowHTTP: true, AllowedCIDRs: []string{"127.0.0.0/8"}, AllowedPorts: []int{port}}, "http://health.example:"+portText+"/health")
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("proxy was used") }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	health := httpComposeHealth{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}, dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	err := health.Check(context.Background(), contract)
	if !errors.Is(err, errComposeHealth) || strings.Contains(err.Error(), "health.example") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("disclosing health error: %v", err)
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect target was requested")
	}
}

func TestComposeHealthBoundsResponseBody(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	var written atomic.Int32
	go func() {
		defer func() { _ = server.Close() }()
		buf := make([]byte, 4096)
		_, _ = server.Read(buf) // request headers are irrelevant to this body-bound proof
		_, _ = server.Write([]byte("HTTP/1.1 200 OK\r\nX-NeroCD-Revision: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\r\nContent-Length: 131072\r\n\r\n"))
		chunk := make([]byte, 4096)
		for {
			n, err := server.Write(chunk)
			written.Add(int32(n))
			if err != nil {
				return
			}
		}
	}()
	contract := composeHealthContract{URL: "http://health.example/health", Host: "health.example", Port: 80, ExpectedRevision: composeTestResolved().GitCommit, ExpectedStatus: 200, Interval: time.Millisecond, Timeout: time.Second, AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("8.8.8.0/24")}, AllowHTTP: true}
	health := httpComposeHealth{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	if err := health.Check(context.Background(), contract); err != nil {
		t.Fatal(err)
	}
	if n := written.Load(); n > 64<<10+4096 {
		t.Fatalf("read body exceeded bound: %d bytes", n)
	}
}
