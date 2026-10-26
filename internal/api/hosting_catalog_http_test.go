package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The exact v1.47.0 API checks query-key presence, not a boolean value:
// https://github.com/sissbruecker/linkding/blob/v1.47.0/bookmarks/api/routes.py#L67-L75
// Its CSRF cookie is ld_csrftoken, and SessionAuthentication requires the header:
// https://github.com/sissbruecker/linkding/blob/v1.47.0/bookmarks/settings/base.py#L110-L112
func catalogBookmarkRequest(client *http.Client, base string, create bool) (*http.Request, error) {
	var body io.Reader
	method, path := "GET", "/api/bookmarks/"
	if create {
		method = "POST"
		path += "?disable_scraping&disable_html_snapshot"
		body = strings.NewReader(`{"url":"http://127.0.0.1:9090/health","title":"NeroCD retained catalog bookmark","tag_names":["acceptance"]}`)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Referer", base+"/bookmarks/")
	if client.Jar != nil {
		for _, cookie := range client.Jar.Cookies(req.URL) {
			if cookie.Name == "ld_csrftoken" {
				req.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
	}
	return req, nil
}

// Return fixed classes only. url.Error and net.OpError can contain URLs,
// credentials or server-provided text and must never be formatted into logs.
func catalogHTTPFailureClass(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "timeout"
	}
	var operation *net.OpError
	var dns *net.DNSError
	if errors.As(err, &operation) || errors.As(err, &dns) {
		return "network"
	}
	return "transport"
}
func catalogHTTPStatus(response *http.Response) int {
	if response == nil || response.StatusCode < 100 || response.StatusCode > 599 {
		return 0
	}
	return response.StatusCode
}

func TestCatalogBookmarkRequestMatchesLinkding147SessionContract(t *testing.T) {
	const base = "http://127.0.0.1:20101"
	origin, _ := url.Parse(base)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(origin, []*http.Cookie{{Name: "ld_csrftoken", Value: "fixture-csrf", Path: "/"}, {Name: "ld_sessionid", Value: "fixture-session", Path: "/"}, {Name: "csrftoken", Value: "wrong-cookie", Path: "/"}})
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	for _, create := range []bool{false, true} {
		req, err := catalogBookmarkRequest(client, base, create)
		if err != nil {
			t.Fatal("request construction failed")
		}
		if req.URL.Path != "/api/bookmarks/" || req.Header.Get("X-CSRFToken") != "fixture-csrf" || req.Header.Get("Referer") != base+"/bookmarks/" || req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Authorization") != "" {
			t.Fatal("session request contract differs")
		}
		if create {
			query := req.URL.Query()
			if req.Method != "POST" || len(query) != 2 || !query.Has("disable_scraping") || !query.Has("disable_html_snapshot") {
				t.Fatal("scraping or snapshot suppression omitted")
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != `{"url":"http://127.0.0.1:9090/health","title":"NeroCD retained catalog bookmark","tag_names":["acceptance"]}` {
				t.Fatal("bookmark payload changed")
			}
		} else if req.Method != "GET" || req.URL.RawQuery != "" || req.Body != nil {
			t.Fatal("persistence read contract changed")
		}
	}
	if client.Timeout != 10*time.Second {
		t.Fatal("client deadline changed")
	}
}

func TestCatalogHTTPFailureDiagnosticsAreBoundedAndRedacted(t *testing.T) {
	const secret = "must-not-appear-in-diagnostics"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"none", nil, "none"},
		{"deadline", &url.Error{Op: "Post", URL: "http://user:" + secret + "@host/", Err: context.DeadlineExceeded}, "timeout"},
		{"cancelled", context.Canceled, "cancelled"},
		{"network", &url.Error{Op: "Post", URL: secret, Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secret)}}, "network"},
		{"dns", &net.DNSError{Err: secret, Name: secret}, "network"},
		{"network_timeout", &net.DNSError{Err: secret, Name: secret, IsTimeout: true}, "timeout"},
		{"other", errors.New(secret), "transport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := catalogHTTPFailureClass(tc.err)
			if got != tc.want || strings.Contains(got, secret) {
				t.Fatal("diagnostic class differs or leaked error text")
			}
		})
	}
	for _, status := range []int{200, 201, 403, 500} {
		if catalogHTTPStatus(&http.Response{StatusCode: status}) != status {
			t.Fatal("HTTP status was discarded")
		}
	}
	if catalogHTTPStatus(nil) != 0 || catalogHTTPStatus(&http.Response{StatusCode: 999}) != 0 {
		t.Fatal("unknown HTTP status was invented")
	}
}

// The trace records counters only, including automatic redirect requests. It
// never records addresses, URLs, request headers, cookies or payloads.
type catalogHTTPTrace struct{ connections, reused, written, firstBytes atomic.Int64 }

func (v *catalogHTTPTrace) hooks() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			v.connections.Add(1)
			if info.Reused {
				v.reused.Add(1)
			}
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { v.written.Add(1) },
		GotFirstResponseByte: func() { v.firstBytes.Add(1) },
	}
}
func catalogHTTPOperation(value string) string {
	switch value {
	case "health", "managed_health", "login_page", "login_submit", "bookmark_create", "bookmark_list":
		return value
	}
	return "unknown"
}
func catalogHTTPDiagnostic(operation, phase string, elapsed time.Duration, response *http.Response, err error, trace *catalogHTTPTrace) string {
	switch phase {
	case "construct", "request", "read", "close", "complete":
	default:
		phase = "unknown"
	}
	if elapsed < 0 {
		elapsed = 0
	}
	if trace == nil {
		trace = &catalogHTTPTrace{}
	}
	return fmt.Sprintf("catalog_http operation=%s phase=%s class=%s elapsed_ms=%d status=%d connections=%d reused=%d written=%d first_bytes=%d", catalogHTTPOperation(operation), phase, catalogHTTPFailureClass(err), elapsed.Milliseconds(), catalogHTTPStatus(response), trace.connections.Load(), trace.reused.Load(), trace.written.Load(), trace.firstBytes.Load())
}
func catalogHTTPRead(t *testing.T, operation string, client *http.Client, request *http.Request, limit int64) (*http.Response, []byte) {
	t.Helper()
	started := time.Now()
	trace := &catalogHTTPTrace{}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace.hooks()))
	response, err := client.Do(request)
	diagnostic := catalogHTTPDiagnostic(operation, "request", time.Since(started), response, err, trace)
	if err != nil {
		t.Fatal(diagnostic)
	}
	t.Log(diagnostic)
	body, err := io.ReadAll(io.LimitReader(response.Body, limit))
	closeErr := response.Body.Close()
	if err != nil {
		t.Fatal(catalogHTTPDiagnostic(operation, "read", time.Since(started), response, err, trace))
	}
	if closeErr != nil {
		t.Fatal(catalogHTTPDiagnostic(operation, "close", time.Since(started), response, closeErr, trace))
	}
	t.Log(catalogHTTPDiagnostic(operation, "complete", time.Since(started), response, nil, trace))
	return response, body
}

func TestCatalogHTTPDiagnosticCoversAllOperationsWithoutSensitiveData(t *testing.T) {
	const secret = "credential-cookie-body-http://private/"
	trace := &catalogHTTPTrace{}
	hooks := trace.hooks()
	hooks.GotConn(httptrace.GotConnInfo{Reused: true})
	hooks.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New(secret)})
	hooks.GotFirstResponseByte()
	for _, operation := range []string{"health", "managed_health", "login_page", "login_submit", "bookmark_create", "bookmark_list", secret} {
		for _, phase := range []string{"construct", "request", "read", "close", "complete", secret} {
			text := catalogHTTPDiagnostic(operation, phase, 123*time.Millisecond, &http.Response{StatusCode: 403}, &url.Error{URL: secret, Err: context.DeadlineExceeded}, trace)
			for _, want := range []string{"class=timeout", "elapsed_ms=123", "status=403", "connections=1", "reused=1", "written=1", "first_bytes=1"} {
				if !strings.Contains(text, want) {
					t.Fatal("diagnostic omitted safe timing or status evidence")
				}
			}
			if strings.Contains(text, secret) {
				t.Fatal("sensitive diagnostic content escaped allowlist")
			}
		}
	}
}

type catalogRoundTripFunc func(*http.Request) (*http.Response, error)

func (f catalogRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
func TestCatalogHTTPReadUsesSameClientAndRecordsRequestLifecycle(t *testing.T) {
	const secret = "sensitive response body"
	client := &http.Client{Timeout: 10 * time.Second, Transport: catalogRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(request.Context())
		if trace == nil {
			t.Fatal("request trace absent")
		}
		trace.GotConn(httptrace.GotConnInfo{})
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		trace.GotFirstResponseByte()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(secret)), Header: make(http.Header), Request: request}, nil
	})}
	request, err := http.NewRequest("GET", "http://127.0.0.1:20101/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, body := catalogHTTPRead(t, "health", client, request, 4096)
	if response.StatusCode != 200 || string(body) != secret || client.Timeout != 10*time.Second {
		t.Fatal("diagnostic wrapper changed response or deadline")
	}
}

func catalogContainerDiagnostic(raw []byte, id, binding, operation, plan string) (string, bool) {
	var v struct {
		ID, Binding, Operation, Plan                                    string
		Running, Restarting, OOMKilled, Dead                            bool
		ExitCode, RestartCount, Memory, MemorySwap, NanoCPUs, PidsLimit int64
	}
	if len(raw) > 8192 || json.Unmarshal(raw, &v) != nil || v.ID != id || v.Binding != binding || v.Operation != operation || v.Plan != plan {
		return "catalog_container class=scope_or_decode_mismatch", false
	}
	return fmt.Sprintf("catalog_container class=observed running=%t restarting=%t oom_killed=%t dead=%t exit_code=%d restart_count=%d memory=%d memory_swap=%d nano_cpus=%d pids_limit=%d", v.Running, v.Restarting, v.OOMKilled, v.Dead, v.ExitCode, v.RestartCount, v.Memory, v.MemorySwap, v.NanoCPUs, v.PidsLimit), true
}
func catalogWorkerDiagnostic(raw []byte) string {
	if len(raw) > 16384 {
		return "catalog_workers class=unavailable"
	}
	var pids []int64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != "uwsgi" {
			continue
		}
		pid, err := strconv.ParseInt(fields[0], 10, 64)
		if err == nil && pid > 0 && len(pids) < 64 {
			pids = append(pids, pid)
		}
	}
	return fmt.Sprintf("catalog_workers class=observed uwsgi_pids=%v", pids)
}
func catalogOOMDiagnostic(raw []byte) string {
	if len(raw) > 4096 {
		return "catalog_cgroup class=unavailable"
	}
	values := map[string]int64{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[0] != "oom" && fields[0] != "oom_kill" && fields[0] != "oom_group_kill" {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && value >= 0 {
			values[fields[0]] = value
		}
	}
	_, oomKnown := values["oom"]
	_, killKnown := values["oom_kill"]
	if !oomKnown || !killKnown {
		return "catalog_cgroup class=unavailable"
	}
	_, groupKnown := values["oom_group_kill"]
	return fmt.Sprintf("catalog_cgroup class=observed oom=%d oom_kill=%d oom_group_kill=%d group_counter_known=%t", values["oom"], values["oom_kill"], values["oom_group_kill"], groupKnown)
}
func TestCatalogContainerDiagnosticsKeepStateAndRejectSensitiveFields(t *testing.T) {
	raw := []byte(`{"ID":"id","Binding":"binding","Operation":"operation","Plan":"plan","Running":true,"OOMKilled":true,"ExitCode":137,"RestartCount":2,"Memory":0,"NanoCPUs":0,"Env":["password=secret-value"]}`)
	result, ok := catalogContainerDiagnostic(raw, "id", "binding", "operation", "plan")
	if !ok || !strings.Contains(result, "oom_killed=true") || !strings.Contains(result, "exit_code=137") || !strings.Contains(result, "restart_count=2") || strings.Contains(result, "secret-value") {
		t.Fatal("container diagnostic lost state or leaked environment")
	}
	if _, ok := catalogContainerDiagnostic(raw, "foreign", "binding", "operation", "plan"); ok {
		t.Fatal("foreign container state accepted")
	}
	if text := catalogWorkerDiagnostic([]byte("PID COMMAND\n42 uwsgi\n43 secret-value\n")); !strings.Contains(text, "[42]") || strings.Contains(text, "secret-value") {
		t.Fatal("worker diagnostic leaked command")
	}
	if text := catalogOOMDiagnostic([]byte("oom 2\noom_kill 1\nsecret-value 7\n")); !strings.Contains(text, "oom=2") || !strings.Contains(text, "oom_kill=1") || strings.Contains(text, "secret-value") {
		t.Fatal("cgroup diagnostic lost counters or leaked text")
	}
}
