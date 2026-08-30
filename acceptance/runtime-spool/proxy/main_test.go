package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestControlStatus(t *testing.T) {
	proxy := newTestProxy(t)

	status := getStatus(t, proxy.URL)
	if status.Outage {
		t.Errorf("GET /__control/status Outage = true, want false")
	}
	if got := len(status.Requests); got != 0 {
		t.Errorf("GET /__control/status len(Requests) = %d, want 0", got)
	}
	if got := len(status.Trace); got != 0 {
		t.Errorf("GET /__control/status len(Trace) = %d, want 0", got)
	}
}

func TestListenerSurvivesDroppedResponse(t *testing.T) {
	tests := []struct {
		name        string
		controlPath string
		requestPath string
		lostCount   func(statusPayload) int
	}{
		{
			name:        "event",
			controlPath: "/__control/drop-event",
			requestPath: "/api/v1/runners/events/batch",
			lostCount:   func(status statusPayload) int { return status.LostEvents },
		},
		{
			name:        "completion",
			controlPath: "/__control/drop-completion",
			requestPath: "/api/v1/runners/complete",
			lostCount:   func(status statusPayload) int { return status.LostCompletions },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proxy := newTestProxy(t)

			response, err := http.Post(proxy.URL+test.controlPath, "application/json", nil)
			if err != nil {
				t.Fatalf("POST %s setup error: %v", test.controlPath, err)
			}
			_ = response.Body.Close()
			if got, want := response.StatusCode, http.StatusNoContent; got != want {
				t.Fatalf("POST %s status = %d, want %d", test.controlPath, got, want)
			}

			client := &http.Client{Timeout: 2 * time.Second}
			response, err = client.Post(proxy.URL+test.requestPath, "application/json", strings.NewReader("{}"))
			if err == nil {
				_ = response.Body.Close()
				t.Errorf("POST %s error = nil, want dropped connection", test.requestPath)
			}

			status := getStatus(t, proxy.URL)
			if got, want := test.lostCount(status), 1; got != want {
				t.Errorf("GET /__control/status lost count after POST %s = %d, want %d", test.requestPath, got, want)
			}
			if got, want := status.Requests[test.requestPath], 1; got != want {
				t.Errorf("GET /__control/status Requests[%q] = %d, want %d", test.requestPath, got, want)
			}
		})
	}
}

func newTestProxy(t *testing.T) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(%q) error: %v", upstream.URL, err)
	}
	proxy := httptest.NewServer(newProxyHandler(newProxyState(), upstreamURL, upstream.Client()))
	t.Cleanup(proxy.Close)
	return proxy
}

func getStatus(t *testing.T, proxyURL string) statusPayload {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(proxyURL + "/__control/status")
	if err != nil {
		t.Fatalf("GET /__control/status error: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if got, want := response.StatusCode, http.StatusOK; got != want {
		t.Fatalf("GET /__control/status status = %d, want %d", got, want)
	}
	var status statusPayload
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatalf("GET /__control/status decode error: %v", err)
	}
	return status
}
