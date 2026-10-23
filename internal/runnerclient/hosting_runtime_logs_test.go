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
	batch := domain.HostingRuntimeLogBatch{BatchSequence: 1, SessionID: "rls_test", InstanceID: strings.Repeat("a", 32), CollectionEpoch: strings.Repeat("b", 32), BatchID: strings.Repeat("c", 32), State: "following", Events: []domain.HostingRuntimeLogEvent{{Component: "application", Stream: "stdout", Message: "sensitive"}}}
	for _, name := range []string{"valid", "wrong_instance", "wrong_batch_sequence"} {
		bad := name != "valid"
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/runners/runtime-log-sessions/events" || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("wrong transport")
				}
				ack := domain.HostingRuntimeLogAck{SessionID: batch.SessionID, InstanceID: batch.InstanceID, CollectionEpoch: batch.CollectionEpoch, BatchID: batch.BatchID, BodyHash: domain.HostingDocumentHash(batch), BatchSequence: batch.BatchSequence, FirstSequence: 1, LastSequence: 1, ReceivedAt: time.Now()}
				if name == "wrong_batch_sequence" {
					ack.BatchSequence++
				}
				if name == "wrong_instance" {
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

func TestHostingRuntimeLogClientClaimBatchCounter(t *testing.T) {
	instance := strings.Repeat("a", 32)
	hash := "sha256:" + strings.Repeat("b", 64)
	target := domain.HostingRuntimeLogTarget{ResourceID: "res_test", BindingID: "hbind_test", DestinationID: "dst_test", ServerID: "srv_test", RunnerID: "run_test", OperationID: "hop_test", DaemonID: "daemon_test", ContainerID: "container_test", ImageID: hash, ReleaseHash: hash, PlanHash: hash, RuntimeNamespace: "test"}
	for _, counter := range []int{0, -1, 1, 2147483647, 2147483648} {
		claim := domain.HostingRuntimeLogClaim{SessionID: "rls_test", InstanceID: instance, CollectionEpoch: strings.Repeat("b", 32), EpochExpiresAt: time.Now().Add(time.Second), DemandExpiresAt: time.Now().Add(2 * time.Second), Target: target, NextSequence: 1, NextBatchSequence: counter}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(domain.HostingRuntimeLogClaims{Targets: []domain.HostingRuntimeLogClaim{claim}, EndOfSweep: true})
		}))
		_, err := New(server.URL, "token").ClaimRuntimeLogSessions(t.Context(), domain.HostingRuntimeLogClaimInput{InstanceID: instance, Limit: 4})
		server.Close()
		if (err != nil) != (counter < 1 || counter > 2147483647) {
			t.Fatal(counter, err)
		}
	}
}
