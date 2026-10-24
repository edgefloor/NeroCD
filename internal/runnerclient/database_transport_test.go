package runnerclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestDatabaseTransportRejectsTLSRedirectsAcrossControlAndLifecycle(t *testing.T) {
	const token = "database_bearer_canary"
	a := domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease_canary", Attempt: 1, Fence: "fence_canary"}
	calls := []struct {
		name   string
		invoke func(context.Context, Client) error
	}{
		{"identity", func(ctx context.Context, c Client) error {
			_, err := c.RuntimeExecutionIdentityWithOptions(ctx, RuntimeExecutionOptions{Database: true})
			return err
		}},
		{"plan", func(ctx context.Context, c Client) error { _, err := c.HostingPlan(ctx, a); return err }},
		{"prepared", func(ctx context.Context, c Client) error {
			_, err := c.HostingPrepared(ctx, domain.HostingPreparedRequest{HostingAuthority: a})
			return err
		}},
		{"apply", func(ctx context.Context, c Client) error { _, err := c.HostingApplyBegin(ctx, a); return err }},
		{"status", func(ctx context.Context, c Client) error {
			_, err := c.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a, Phase: "verifying"})
			return err
		}},
		{"hosting complete", func(ctx context.Context, c Client) error {
			_, err := c.HostingComplete(ctx, domain.HostingCompleteRequest{HostingAuthority: a})
			return err
		}},
		{"heartbeat", func(ctx context.Context, c Client) error { _, err := c.Heartbeat(ctx); return err }},
		{"claim", func(ctx context.Context, c Client) error { _, err := c.Claim(ctx); return err }},
		{"renew", func(ctx context.Context, c Client) error {
			_, err := c.Renew(ctx, Authority{LeaseID: a.LeaseID, Attempt: 1, Fence: a.Fence})
			return err
		}},
		{"cancel polling", func(ctx context.Context, c Client) error {
			_, err := c.Lease(ctx, Authority{LeaseID: a.LeaseID, Attempt: 1, Fence: a.Fence})
			return err
		}},
		{"events", func(ctx context.Context, c Client) error {
			_, err := c.AppendEvents(ctx, EventBatchRequest{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: 1, Fence: a.Fence})
			return err
		}},
		{"completion", func(ctx context.Context, c Client) error {
			_, err := c.Complete(ctx, CompleteRequest{LeaseID: a.LeaseID, Attempt: 1, Fence: a.Fence, Status: "failed"})
			return err
		}},
		{"telemetry", func(ctx context.Context, c Client) error { return c.ReportTelemetry(ctx, OperationalTelemetry{}) }},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			var leaked atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); _, _ = w.Write([]byte(`{}`)) }))
			defer destination.Close()
			sourceCalls := 0
			source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceCalls++
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("original authenticated request lost bearer")
				}
				http.Redirect(w, r, destination.URL+"/leak?fence="+a.Fence, http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			client := New(source.URL, token)
			client.HTTP = source.Client()
			err := call.invoke(WithDatabaseTransport(t.Context(), DatabaseTransport{}), client)
			if err == nil || sourceCalls != 1 || leaked.Load() != 0 {
				t.Fatalf("redirect escaped protection: source=%d destination=%d err=%v", sourceCalls, leaked.Load(), err)
			}
			for _, canary := range []string{token, a.Fence, a.LeaseID} {
				if strings.Contains(fmt.Sprintf("%+v", err), canary) {
					t.Fatal("authority escaped error output")
				}
			}
			if !HTTPStatus(err, http.StatusTemporaryRedirect) {
				t.Fatalf("redirect was not an explicit failed response: %v", err)
			}
		})
	}
}

func TestDatabaseTransportIsExplicitAndChildCannotWeakenIt(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner"})
	}))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	legacy := New(source.URL, "fixture")
	legacy.HTTP = source.Client()
	if _, err := legacy.Identity(t.Context()); err != nil || destinationCalls.Load() != 1 {
		t.Fatal("untagged legacy redirect behavior changed", err)
	}
	ctx := WithDatabaseTransport(t.Context(), DatabaseTransport{})
	ctx = WithDatabaseTransport(ctx, DatabaseTransport{TrustedLoopbackHTTP: true})
	if _, err := New(destination.URL, "fixture").Identity(ctx); err == nil || destinationCalls.Load() != 1 {
		t.Fatal("child relaxed protected HTTPS policy")
	}
	fixture := WithDatabaseTransport(t.Context(), DatabaseTransport{TrustedLoopbackHTTP: true})
	if _, err := New(destination.URL, "fixture").Identity(fixture); err != nil || destinationCalls.Load() != 2 {
		t.Fatal("explicit loopback fixture failed", err)
	}
	if _, err := New("http://controller.invalid", "fixture").Identity(fixture); err == nil {
		t.Fatal("fixture policy allowed remote HTTP")
	}
}

type databaseRoundTripper struct{ calls *atomic.Int32 }

func (r databaseRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, errors.New("unexpected custom transport")
}
func TestDatabaseTransportRejectsUnverifiableClientsAndDisablesProxy(t *testing.T) {
	var requests atomic.Int32
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner"})
	}))
	defer source.Close()
	ctx := WithDatabaseTransport(t.Context(), DatabaseTransport{})
	var custom atomic.Int32
	for _, transport := range []http.RoundTripper{&http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, databaseRoundTripper{calls: &custom}} {
		client := New(source.URL, "fixture")
		client.HTTP = &http.Client{Transport: transport}
		if _, err := client.Identity(ctx); err == nil {
			t.Fatal("accepted unverifiable transport")
		}
	}
	if requests.Load() != 0 || custom.Load() != 0 {
		t.Fatal("invalid transport received credentials")
	}
	client := New(source.URL, "fixture")
	client.HTTP = source.Client()
	base := client.HTTP.Transport.(*http.Transport).Clone()
	t.Cleanup(base.CloseIdleConnections)
	var proxy atomic.Int32
	base.Proxy = func(*http.Request) (*url.URL, error) { proxy.Add(1); return nil, errors.New("ambient proxy invoked") }
	client.HTTP.Transport = base
	if _, err := client.Identity(ctx); err != nil || requests.Load() != 1 || proxy.Load() != 0 {
		t.Fatal("protected request used ambient proxy", err)
	}
}
func TestDatabaseTransportSanitizesFailureDetailsAndKeepsFencingCode(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				_, _ = io.WriteString(w, `{"code":"not_found","echo":"authority_canary"}`)
			}))
			defer server.Close()
			c := New(server.URL, "fixture")
			c.HTTP = server.Client()
			_, err := c.Lease(WithDatabaseTransport(t.Context(), DatabaseTransport{}), Authority{LeaseID: "lease", Attempt: 1, Fence: "authority_canary"})
			var api *APIError
			if !errors.As(err, &api) {
				t.Fatal("expected API response error", err)
			}
			if strings.Contains(fmt.Sprintf("%#v", api), "authority_canary") {
				t.Fatal("failure retained authority diagnostics")
			}
			if code == http.StatusNotFound && api.Detail != `{"code":"not_found"}` {
				t.Fatal("lost closed replay fencing code")
			}
		})
	}
}

func TestDatabaseTransportPreservesCancellationWithoutRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	client := New(server.URL, "fixture")
	client.HTTP = server.Client()
	for _, deadline := range []bool{false, true} {
		parent := WithDatabaseTransport(t.Context(), DatabaseTransport{})
		ctx, cancel := context.WithCancel(parent)
		want := context.Canceled
		if deadline {
			cancel()
			ctx, cancel = context.WithDeadline(parent, time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			cancel()
		}
		_, err := client.Identity(ctx)
		cancel()
		if !errors.Is(err, want) || requests.Load() != 0 {
			t.Fatal("cancellation identity changed or request escaped", err)
		}
	}
}
