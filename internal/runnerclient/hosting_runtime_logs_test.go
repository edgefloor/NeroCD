package runnerclient

import (
	"encoding/json"
	"nerocd/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHostingRuntimeLogClientBoundedIdentityAndAck(t *testing.T) {
	batch := domain.HostingRuntimeLogBatch{SessionID: "rls_test", InstanceID: strings.Repeat("a", 32), CollectionEpoch: strings.Repeat("b", 32), BatchID: strings.Repeat("c", 32), State: "following", Events: []domain.HostingRuntimeLogEvent{{Component: "application", Stream: "stdout", Message: "sensitive"}}}
	for _, bad := range []bool{false, true} {
		name := "valid"
		if bad {
			name = "wrong_instance"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/runners/runtime-log-sessions/events" || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("wrong transport")
				}
				ack := domain.HostingRuntimeLogAck{SessionID: batch.SessionID, InstanceID: batch.InstanceID, CollectionEpoch: batch.CollectionEpoch, BatchID: batch.BatchID, BodyHash: domain.HostingDocumentHash(batch), FirstSequence: 1, LastSequence: 1, ReceivedAt: time.Now()}
				if bad {
					ack.InstanceID = strings.Repeat("d", 32)
				}
				_ = json.NewEncoder(w).Encode(ack)
			}))
			defer server.Close()
			_, err := New(server.URL, "token").AppendRuntimeLogs(t.Context(), batch)
			if (err != nil) != bad {
				t.Fatal(err)
			}
		})
	}
	for _, body := range []string{`{"targets":[],"next_cursor":"","end_of_sweep":true} {}`, strings.Repeat(" ", 64<<10) + `{}`, `{"targets":[],"next_cursor":"","end_of_sweep":true,"socket":"foreign"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := New(server.URL, "token").ClaimRuntimeLogSessions(t.Context(), domain.HostingRuntimeLogClaimInput{InstanceID: batch.InstanceID, AfterSessionID: "", Limit: 4})
		server.Close()
		if err == nil {
			t.Fatal("invalid response accepted")
		}
	}
}
