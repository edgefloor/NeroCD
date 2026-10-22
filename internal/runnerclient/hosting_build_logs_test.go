package runnerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestBuildEventsExactAcknowledgment(t *testing.T) {
	a := domain.HostingBuildAuthority{BuildID: "build-one", RunID: "build_run_build-one", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	b := domain.HostingBuildLogBatch{Authority: a, Events: []domain.HostingBuildLogEvent{{EventKey: "one", Sequence: 1, Component: "build", Stream: "system", Message: "output"}, {EventKey: "two", Sequence: 2, Component: "buildkit", Stream: "stderr", Message: "more"}}}
	for _, name := range []string{"valid", "build", "run", "lease", "attempt", "fence", "count", "order", "key", "requested", "hash", "zero", "overflow", "descending", "unknown", "trailing", "oversize", "null"} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/runners/builds/events" || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("wrong dedicated transport")
				}
				var got domain.HostingBuildLogBatch
				if json.NewDecoder(r.Body).Decode(&got) != nil || domain.HostingDocumentHash(got) != domain.HostingDocumentHash(b) {
					t.Error("request changed")
				}
				ack := domain.HostingBuildLogAck{Authority: a}
				for _, e := range b.Events {
					ack.Receipts = append(ack.Receipts, domain.HostingBuildLogReceipt{EventKey: e.EventKey, RequestedSequence: e.Sequence, Sequence: e.Sequence + 10, BodyHash: domain.HostingBuildLogBodyHash(a, e)})
				}
				switch name {
				case "build":
					ack.Authority.BuildID = "other"
				case "run":
					ack.Authority.RunID = "other"
				case "lease":
					ack.Authority.LeaseID = "other"
				case "attempt":
					ack.Authority.Attempt++
				case "fence":
					ack.Authority.Fence = "other"
				case "count":
					ack.Receipts = ack.Receipts[:1]
				case "order":
					ack.Receipts[0], ack.Receipts[1] = ack.Receipts[1], ack.Receipts[0]
				case "key":
					ack.Receipts[0].EventKey = "other"
				case "requested":
					ack.Receipts[0].RequestedSequence++
				case "hash":
					ack.Receipts[0].BodyHash = "wrong"
				case "zero":
					ack.Receipts[0].Sequence = 0
				case "overflow":
					ack.Receipts[1].Sequence = 1 << 31
				case "descending":
					ack.Receipts[1].Sequence = 1
				}
				raw, _ := json.Marshal(ack)
				switch name {
				case "unknown":
					raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
				case "trailing":
					raw = append(raw, []byte(` {}`)...)
				case "oversize":
					raw = []byte(strings.Repeat(" ", maxBuildResponseBytes+1))
				case "null":
					raw = []byte("null")
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			_, err := New(server.URL, "token").BuildEvents(t.Context(), b)
			if (err == nil) != (name == "valid") {
				t.Fatalf("ack validation: %v", err)
			}
			if err != nil && ClassifyFailure(err) != FailurePermanent {
				t.Fatalf("invalid ack is retryable: %v", err)
			}
		})
	}
}
