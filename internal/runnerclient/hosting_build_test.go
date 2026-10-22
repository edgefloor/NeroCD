package runnerclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

func buildClientAuthority() domain.HostingBuildAuthority {
	return domain.HostingBuildAuthority{BuildID: "client-build", RunID: "build_run_client-build", LeaseID: "lease", Attempt: 2, Fence: "fence"}
}
func buildClientMetadata() domain.HostingBuild {
	a := buildClientAuthority()
	return domain.HostingBuild{ID: a.BuildID, RunID: a.RunID, Revision: 9, State: "failed"}
}
func buildClientSource() hostingbuildprotocol.SourceCommitRequest {
	return hostingbuildprotocol.SourceCommitRequest{Authority: buildClientAuthority(), ReceiptID: "source-receipt", Receipt: domain.HostingBuildSourceReceipt{BuildID: "client-build", SourceID: "source", InputID: "input", Commit: strings.Repeat("a", 40), PolicyHash: "sha256:" + strings.Repeat("b", 64), TreeDigest: "sha256:" + strings.Repeat("c", 64)}}
}
func buildClientCompletion() hostingbuildprotocol.CompleteRequest {
	return hostingbuildprotocol.CompleteRequest{Authority: buildClientAuthority(), ReceiptID: "complete-receipt", ExpectedRevision: 4, Completion: hostingbuildprotocol.Completion{State: "failed", FailureCode: "build_failed"}}
}
func sourceClientAck(in hostingbuildprotocol.SourceCommitRequest) hostingbuildprotocol.ReceiptAcknowledgment {
	return hostingbuildprotocol.ReceiptAcknowledgment{Authority: in.Authority, Phase: "source", ReceiptID: in.ReceiptID, BodyHash: hostingbuildprotocol.SourceReceiptHash(in.Receipt)}
}
func completeClientAck(in hostingbuildprotocol.CompleteRequest) hostingbuildprotocol.ReceiptAcknowledgment {
	phase, _ := hostingbuildprotocol.CompletionPhase(in.Completion)
	return hostingbuildprotocol.ReceiptAcknowledgment{Authority: in.Authority, Phase: phase, ReceiptID: in.ReceiptID, BodyHash: hostingbuildprotocol.CompletionHash(in.Completion)}
}

func TestBuildClientAuthenticatedExactMessages(t *testing.T) {
	a := buildClientAuthority()
	source := buildClientSource()
	complete := buildClientCompletion()
	sourceAck, completeAck := sourceClientAck(source), completeClientAck(complete)
	for _, test := range []struct {
		path            string
		input, response any
		call            func(Client) (any, error)
	}{
		{"status", hostingbuildprotocol.StatusRequest{Authority: a}, hostingbuildprotocol.StatusResponse{Build: buildClientMetadata(), Receipts: []hostingbuildprotocol.ReceiptAcknowledgment{sourceAck, completeAck}}, func(c Client) (any, error) {
			return c.BuildStatus(t.Context(), hostingbuildprotocol.StatusRequest{Authority: a})
		}},
		{"source-begin", hostingbuildprotocol.SourceBeginRequest{Authority: a, ExpectedRevision: 2}, hostingbuildprotocol.PhaseResponse{Build: buildClientMetadata()}, func(c Client) (any, error) {
			return c.BuildSourceBegin(t.Context(), hostingbuildprotocol.SourceBeginRequest{Authority: a, ExpectedRevision: 2})
		}},
		{"source-commit", source, hostingbuildprotocol.PhaseResponse{Build: buildClientMetadata(), Acknowledgment: &sourceAck}, func(c Client) (any, error) { return c.BuildSourceCommit(t.Context(), source) }},
		{"prepare", hostingbuildprotocol.PrepareRequest{Authority: a, ExpectedRevision: 3, DaemonID: "observed"}, hostingbuildprotocol.PhaseResponse{Build: buildClientMetadata()}, func(c Client) (any, error) {
			return c.BuildPrepare(t.Context(), hostingbuildprotocol.PrepareRequest{Authority: a, ExpectedRevision: 3, DaemonID: "observed"})
		}},
		{"complete", complete, hostingbuildprotocol.PhaseResponse{Build: buildClientMetadata(), Acknowledgment: &completeAck}, func(c Client) (any, error) { return c.BuildComplete(t.Context(), complete) }},
	} {
		t.Run(test.path, func(t *testing.T) {
			var requests atomic.Int32
			expected, _ := json.Marshal(test.input)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/runners/builds/"+test.path || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer runner-token" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("wrong authenticated build request")
				}
				payload, err := io.ReadAll(r.Body)
				if err != nil || string(payload) != string(expected) {
					t.Error("request changed retained protocol intent")
				}
				if err = json.NewEncoder(w).Encode(test.response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			result, err := test.call(New(server.URL, "runner-token"))
			if err != nil || !reflect.DeepEqual(result, test.response) || requests.Load() != 1 {
				t.Fatalf("response=%+v error=%v requests=%d", result, err, requests.Load())
			}
		})
	}
}

func TestBuildClientRejectsInvalidOutgoingBeforeHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	c := New(server.URL, "token")
	invalid := []func() error{
		func() error { _, err := c.BuildStatus(t.Context(), hostingbuildprotocol.StatusRequest{}); return err },
		func() error {
			_, err := c.BuildSourceBegin(t.Context(), hostingbuildprotocol.SourceBeginRequest{Authority: buildClientAuthority()})
			return err
		},
		func() error {
			in := buildClientSource()
			in.Receipt.BuildID = "other"
			_, err := c.BuildSourceCommit(t.Context(), in)
			return err
		},
		func() error {
			_, err := c.BuildPrepare(t.Context(), hostingbuildprotocol.PrepareRequest{Authority: buildClientAuthority(), ExpectedRevision: 1, DaemonID: "bad\n"})
			return err
		},
		func() error {
			in := buildClientCompletion()
			in.ExpectedRevision = 0
			_, err := c.BuildComplete(t.Context(), in)
			return err
		},
	}
	for i, call := range invalid {
		if !errors.Is(call(), hostingbuildprotocol.ErrInvalidMessage) {
			t.Errorf("invalid request%d accepted", i)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid input reached transport")
	}
}

func TestBuildClientRequiresExactAcknowledgment(t *testing.T) {
	for _, phase := range []string{"source", "complete", "cleanup"} {
		t.Run(phase, func(t *testing.T) {
			source := buildClientSource()
			complete := buildClientCompletion()
			if phase == "cleanup" {
				complete.Completion = hostingbuildprotocol.Completion{State: "cleanup_required", FailureCode: "cleanup_unconfirmed"}
			}
			ack := completeClientAck(complete)
			if phase == "source" {
				ack = sourceClientAck(source)
			}
			for _, test := range []struct {
				name   string
				mutate func(*hostingbuildprotocol.PhaseResponse)
			}{
				{"valid historical proof", func(*hostingbuildprotocol.PhaseResponse) {}},
				{"missing", func(r *hostingbuildprotocol.PhaseResponse) { r.Acknowledgment = nil }},
				{"build", func(r *hostingbuildprotocol.PhaseResponse) { r.Build.ID = "other" }},
				{"run", func(r *hostingbuildprotocol.PhaseResponse) { r.Build.RunID = "other" }},
				{"tuple build", func(r *hostingbuildprotocol.PhaseResponse) { r.Acknowledgment.Authority.BuildID = "other" }},
				{"tuple run", func(r *hostingbuildprotocol.PhaseResponse) { r.Acknowledgment.Authority.RunID = "other" }},
				{"tuple lease", func(r *hostingbuildprotocol.PhaseResponse) { r.Acknowledgment.Authority.LeaseID = "older" }},
				{"tuple attempt", func(r *hostingbuildprotocol.PhaseResponse) { r.Acknowledgment.Authority.Attempt++ }},
				{"tuple fence", func(r *hostingbuildprotocol.PhaseResponse) { r.Acknowledgment.Authority.Fence = "older" }},
				{"phase", func(r *hostingbuildprotocol.PhaseResponse) { r.Acknowledgment.Phase = "wrong" }},
				{"receipt", func(r *hostingbuildprotocol.PhaseResponse) { r.Acknowledgment.ReceiptID = "other" }},
				{"body", func(r *hostingbuildprotocol.PhaseResponse) {
					r.Acknowledgment.BodyHash = "sha256:" + strings.Repeat("f", 64)
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					copyAck := ack
					response := hostingbuildprotocol.PhaseResponse{Build: buildClientMetadata(), Acknowledgment: &copyAck}
					if phase == "cleanup" {
						response.Build.State = "canceled"
					}
					test.mutate(&response)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						if err := json.NewEncoder(w).Encode(response); err != nil {
							t.Error(err)
						}
					}))
					defer server.Close()
					c := New(server.URL, "token")
					var result hostingbuildprotocol.PhaseResponse
					var err error
					if phase == "source" {
						result, err = c.BuildSourceCommit(t.Context(), source)
					} else {
						result, err = c.BuildComplete(t.Context(), complete)
					}
					if test.name == "valid historical proof" {
						expectedState := "failed"
						if phase == "cleanup" {
							expectedState = "canceled"
						}
						if err != nil || result.Build.Revision != 9 || result.Build.State != expectedState {
							t.Fatal("advanced state rejected", err)
						}
					} else if err == nil || result.Build.ID != "" {
						t.Fatal("mismatched proof published", err)
					}
				})
			}
		})
	}
}

func TestBuildClientStatusSeparatesPermissionFromProof(t *testing.T) {
	ack := sourceClientAck(buildClientSource())
	for _, test := range []struct {
		name   string
		mutate func(*hostingbuildprotocol.StatusResponse)
		valid  bool
	}{
		{"historical false", func(*hostingbuildprotocol.StatusResponse) {}, true},
		{"explicit live", func(r *hostingbuildprotocol.StatusResponse) { r.Build.State = "building"; r.MayExecute = true }, true},
		{"cancel requested", func(r *hostingbuildprotocol.StatusResponse) {
			r.Build.State = "building"
			r.MayExecute = true
			r.CancelRequested = true
		}, false},
		{"terminal true", func(r *hostingbuildprotocol.StatusResponse) { r.MayExecute = true }, false},
		{"cleanup true", func(r *hostingbuildprotocol.StatusResponse) { r.Build.State = "cleanup_required"; r.MayExecute = true }, false},
		{"unknown state", func(r *hostingbuildprotocol.StatusResponse) { r.Build.State = "unknown" }, false},
		{"zero revision", func(r *hostingbuildprotocol.StatusResponse) { r.Build.Revision = 0 }, false},
		{"wrong build", func(r *hostingbuildprotocol.StatusResponse) { r.Build.ID = "other" }, false},
		{"wrong run", func(r *hostingbuildprotocol.StatusResponse) { r.Build.RunID = "other" }, false},
		{"old tuple", func(r *hostingbuildprotocol.StatusResponse) { r.Receipts[0].Authority.Fence = "older" }, false},
		{"bad phase", func(r *hostingbuildprotocol.StatusResponse) { r.Receipts[0].Phase = "prepare" }, false},
		{"empty receipt", func(r *hostingbuildprotocol.StatusResponse) { r.Receipts[0].ReceiptID = "" }, false},
		{"bad hash", func(r *hostingbuildprotocol.StatusResponse) { r.Receipts[0].BodyHash = "not-a-digest" }, false},
		{"duplicate phase", func(r *hostingbuildprotocol.StatusResponse) { r.Receipts = append(r.Receipts, r.Receipts[0]) }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := hostingbuildprotocol.StatusResponse{Build: buildClientMetadata(), Receipts: []hostingbuildprotocol.ReceiptAcknowledgment{ack}}
			test.mutate(&response)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			result, err := New(server.URL, "token").BuildStatus(t.Context(), hostingbuildprotocol.StatusRequest{Authority: buildClientAuthority()})
			if (err == nil) != test.valid || !test.valid && result.Build.ID != "" {
				t.Fatalf("status validation failed: %+v %v", result, err)
			}
			if test.valid && result.MayExecute != response.MayExecute {
				t.Fatal("receipt changed explicit permission")
			}
		})
	}
}

func TestBuildClientStrictBoundedResponsesAndErrors(t *testing.T) {
	valid, _ := json.Marshal(hostingbuildprotocol.PhaseResponse{Build: buildClientMetadata()})
	sentinel := "response-secret-sentinel"
	for _, test := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"valid", string(valid), 200, true},
		{"exact size", string(valid) + strings.Repeat(" ", maxBuildResponseBytes-len(valid)), 200, true},
		{"over size", string(valid) + strings.Repeat(" ", maxBuildResponseBytes-len(valid)+1), 200, false},
		{"trailing document", string(valid) + ` {}`, 200, false},
		{"trailing garbage", string(valid) + sentinel, 200, false},
		{"unknown field", `{"build":{},"` + sentinel + `":true}`, 200, false},
		{"unknown nested field", `{"build":{"` + sentinel + `":true}}`, 200, false},
		{"malformed", `{"build":"` + sentinel, 200, false},
		{"wrong type", `{"build":"` + sentinel + `"}`, 200, false},
		{"null", `null`, 200, false},
		{"array", `[]`, 200, false},
		{"missing metadata", `{}`, 200, false},
		{"unauthenticated", sentinel, 401, false},
		{"forbidden", sentinel, 403, false},
		{"conflict", sentinel, 409, false},
		{"server error", sentinel, 500, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			result, err := New(server.URL, "token").BuildSourceBegin(t.Context(), hostingbuildprotocol.SourceBeginRequest{Authority: buildClientAuthority(), ExpectedRevision: 2})
			if (err == nil) != test.valid || !test.valid && result.Build.ID != "" || requests.Load() != 1 {
				t.Fatal("invalid response or HTTP retry", err, requests.Load())
			}
			if err != nil {
				if strings.Contains(err.Error(), sentinel) {
					t.Fatal("response reflected in error")
				}
				var apiErr *APIError
				if errors.As(err, &apiErr) {
					if apiErr.Detail != "" || apiErr.Err != nil && strings.Contains(apiErr.Err.Error(), sentinel) {
						t.Fatal("response retained in APIError")
					}
				}
			}
		})
	}
	reader := &countedIdentityReader{Reader: strings.NewReader(string(valid) + strings.Repeat(" ", 2*maxBuildResponseBytes))}
	var result buildResponse[hostingbuildprotocol.PhaseResponse]
	if result.decodeResponse(reader) == nil || reader.bytesRead != maxBuildResponseBytes+1 || result.value.Build.ID != "" {
		t.Fatal("decoder exceeded bound or published incomplete response")
	}
	if result.decodeResponse(strings.NewReader(string(valid)+` {}`)) == nil || result.value.Build.ID != "" {
		t.Fatal("decoder published trailing document")
	}
}

func TestBuildClientBeginPrepareRejectUnexpectedProof(t *testing.T) {
	ack := sourceClientAck(buildClientSource())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(hostingbuildprotocol.PhaseResponse{Build: buildClientMetadata(), Acknowledgment: &ack})
	}))
	defer server.Close()
	client := New(server.URL, "token")
	if _, err := client.BuildSourceBegin(t.Context(), hostingbuildprotocol.SourceBeginRequest{Authority: buildClientAuthority(), ExpectedRevision: 1}); err == nil {
		t.Fatal("begin interpreted unsolicited acknowledgment")
	}
	if _, err := client.BuildPrepare(t.Context(), hostingbuildprotocol.PrepareRequest{Authority: buildClientAuthority(), ExpectedRevision: 1, DaemonID: "daemon"}); err == nil {
		t.Fatal("prepare interpreted unsolicited acknowledgment")
	}
}

func TestBuildClientContextCancellationJoins(t *testing.T) {
	started := make(chan struct{})
	handlerDone := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
		close(handlerDone)
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := New(server.URL, "token").BuildStatus(ctx, hostingbuildprotocol.StatusRequest{Authority: buildClientAuthority()})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("request failed to join")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("server request did not cancel")
	}
}
