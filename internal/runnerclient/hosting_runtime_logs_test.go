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
			_, err := New(server.URL, "token").AppendRuntimeLogs(t.Context(), batch, RuntimeLogTransport{TrustedLoopbackHTTP: true})
			if (err != nil) != bad {
				t.Fatal(err)
			}
		})
	}
	for _, body := range []string{`{"targets":[],"next_cursor":"","end_of_sweep":true} {}`, strings.Repeat(" ", 64<<10) + `{}`, `{"targets":[],"next_cursor":"","end_of_sweep":true,"socket":"foreign"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := New(server.URL, "token").ClaimRuntimeLogSessions(t.Context(), domain.HostingRuntimeLogClaimInput{InstanceID: batch.InstanceID, AfterSessionID: "", Limit: 4}, RuntimeLogTransport{TrustedLoopbackHTTP: true})
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
		_, err := New(server.URL, "token").ClaimRuntimeLogSessions(t.Context(), domain.HostingRuntimeLogClaimInput{InstanceID: instance, Limit: 4}, RuntimeLogTransport{TrustedLoopbackHTTP: true})
		server.Close()
		if (err != nil) != (counter < 1 || counter > 2147483647) {
			t.Fatal(counter, err)
		}
	}
}

func runtimeLogResponseFixtures() (domain.HostingRuntimeLogBatch, domain.HostingRuntimeLogClaims, domain.HostingRuntimeLogAck) {
	stamp := time.Now().UTC()
	hash := "sha256:" + strings.Repeat("b", 64)
	batch := domain.HostingRuntimeLogBatch{SessionID: "rls_test", InstanceID: strings.Repeat("a", 32), CollectionEpoch: strings.Repeat("b", 32), BatchID: strings.Repeat("c", 32), BatchSequence: 1, State: "following", Events: []domain.HostingRuntimeLogEvent{}}
	target := domain.HostingRuntimeLogTarget{ResourceID: "res_test", BindingID: "hbind_test", DestinationID: "dst_test", ServerID: "srv_test", RunnerID: "run_test", OperationID: "hop_test", DaemonID: "daemon_test", ContainerID: "container_test", ImageID: hash, ReleaseHash: hash, PlanHash: hash, RuntimeNamespace: "test"}
	claims := domain.HostingRuntimeLogClaims{Targets: []domain.HostingRuntimeLogClaim{{SessionID: batch.SessionID, InstanceID: batch.InstanceID, CollectionEpoch: batch.CollectionEpoch, EpochExpiresAt: stamp.Add(time.Second), DemandExpiresAt: stamp.Add(2 * time.Second), Target: target, NextSequence: 1, NextBatchSequence: 1}}, NextCursor: "", EndOfSweep: true}
	ack := domain.HostingRuntimeLogAck{SessionID: batch.SessionID, InstanceID: batch.InstanceID, CollectionEpoch: batch.CollectionEpoch, BatchID: batch.BatchID, BatchSequence: batch.BatchSequence, BodyHash: domain.HostingDocumentHash(batch), ReceivedAt: stamp}
	return batch, claims, ack
}

func TestHostingRuntimeLogAndObservationResponseContractsRemainSeparate(t *testing.T) {
	batch, claims, ack := runtimeLogResponseFixtures()
	observation := domain.HostingRuntimeObservation{BindingID: "binding", SampleID: "sample", ContainerState: "exited", Readiness: "not_checked", ExitCode: new(int)}
	observationAck := domain.HostingObservationAck{SampleID: observation.SampleID, BodyHash: domain.HostingDocumentHash(observation), ReceivedAt: time.Now().UTC()}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing runner bearer")
		}
		var result any
		switch r.URL.Path {
		case "/api/v1/runners/runtime-log-sessions/claim":
			result = claims
		case "/api/v1/runners/runtime-log-sessions/events":
			result = ack
		case "/api/v1/runners/hosting-observations/targets":
			result = domain.HostingObservationTargets{Targets: []domain.HostingObservationTarget{}, NextCursor: "", EndOfSweep: true}
		case "/api/v1/runners/hosting-observations/report":
			result = observationAck
		default:
			t.Error("unexpected path", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := New(server.URL, "token")
	client.HTTP = server.Client()
	if result, err := client.ClaimRuntimeLogSessions(t.Context(), domain.HostingRuntimeLogClaimInput{InstanceID: batch.InstanceID, Limit: 4}, RuntimeLogTransport{TrustedLoopbackHTTP: true}); err != nil || len(result.Targets) != 1 || result.Targets[0].NextBatchSequence != 1 {
		t.Fatal("valid log claims rejected", result, err)
	}
	if result, err := client.AppendRuntimeLogs(t.Context(), batch, RuntimeLogTransport{TrustedLoopbackHTTP: true}); err != nil || result != ack {
		t.Fatal("valid log ack rejected", result, err)
	}
	if result, err := client.HostingObservationTargets(t.Context(), domain.HostingObservationTargetsInput{Limit: 4}, RuntimeLogTransport{TrustedLoopbackHTTP: true}); err != nil || !result.EndOfSweep || result.Targets == nil {
		t.Fatal("observation targets changed", result, err)
	}
	if result, err := client.ReportHostingObservation(t.Context(), observation); err != nil || result != observationAck {
		t.Fatal("observation ack changed", result, err)
	}
}

func TestHostingRuntimeLogClientRejectsMalformedResponseDocuments(t *testing.T) {
	batch, claims, ack := runtimeLogResponseFixtures()
	for _, kind := range []string{"claims", "ack"} {
		t.Run(kind, func(t *testing.T) {
			var value any = claims
			limit := 64 << 10
			counterKey := "next_batch_sequence"
			if kind == "ack" {
				value, limit, counterKey = ack, 8<<10, "batch_sequence"
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			valid := string(raw)
			counter := `"` + counterKey + `":1`
			cases := map[string]string{
				"valid":               valid,
				"null_document":       `null`,
				"wrong_document_type": `[]`,
				"unknown_root":        strings.TrimSuffix(valid, "}") + `,"foreign":true}`,
				"trailing_document":   valid + ` {}`,
				"trailing_scalar":     valid + ` false`,
				"trailing_invalid":    valid + ` !`,
				"oversized":           strings.Repeat(" ", limit) + valid,
				"missing_counter":     strings.Replace(valid, counter+",", "", 1),
				"null_counter":        strings.Replace(valid, counter, `"`+counterKey+`":null`, 1),
				"string_counter":      strings.Replace(valid, counter, `"`+counterKey+`":"1"`, 1),
				"fractional_counter":  strings.Replace(valid, counter, `"`+counterKey+`":1.5`, 1),
				"zero_counter":        strings.Replace(valid, counter, `"`+counterKey+`":0`, 1),
				"oversized_counter":   strings.Replace(valid, counter, `"`+counterKey+`":2147483648`, 1),
				"duplicate_counter":   strings.Replace(valid, counter, counter+","+counter, 1),
				"counter_alias":       strings.Replace(valid, `"`+counterKey+`"`, `"BATCH_SEQUENCE"`, 1),
			}
			if kind == "claims" {
				// The final claim counter precedes the closing object, so remove its
				// leading comma when constructing this missing-field response.
				cases["missing_counter"] = strings.Replace(valid, ","+counter, "", 1)
				cases["nested_unknown"] = strings.Replace(valid, `"target":{`, `"target":{"socket":"foreign",`, 1)
				cases["nested_missing"] = strings.Replace(valid, `"container_id":"container_test",`, "", 1)
				cases["nested_null"] = strings.Replace(valid, `"container_id":"container_test"`, `"container_id":null`, 1)
				cases["nested_wrong_type"] = strings.Replace(valid, `"container_id":"container_test"`, `"container_id":true`, 1)
				cases["nested_duplicate"] = strings.Replace(valid, `"container_id":"container_test"`, `"container_id":"container_test","container_id":"container_test"`, 1)
				var nullTargets map[string]json.RawMessage
				if err := json.Unmarshal(raw, &nullTargets); err != nil {
					t.Fatal(err)
				}
				nullTargets["targets"] = json.RawMessage("null")
				nullRaw, err := json.Marshal(nullTargets)
				if err != nil {
					t.Fatal(err)
				}
				cases["null_targets"] = string(nullRaw)
				cases["wrong_cursor_type"] = strings.Replace(valid, `"next_cursor":""`, `"next_cursor":1`, 1)
				cases["null_timestamp"] = strings.Replace(valid, `"epoch_expires_at":"`+claims.Targets[0].EpochExpiresAt.Format(time.RFC3339Nano)+`"`, `"epoch_expires_at":null`, 1)
			} else {
				cases["missing_received_at"] = strings.Replace(valid, `,"received_at":"`+ack.ReceivedAt.Format(time.RFC3339Nano)+`"`, "", 1)
				cases["null_timestamp"] = strings.Replace(valid, `"received_at":"`+ack.ReceivedAt.Format(time.RFC3339Nano)+`"`, `"received_at":null`, 1)
			}
			for name, body := range cases {
				t.Run(name, func(t *testing.T) {
					if name != "valid" && body == valid {
						t.Fatal("malformed response fixture did not change")
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
					defer server.Close()
					client := New(server.URL, "token")
					var err error
					if kind == "claims" {
						_, err = client.ClaimRuntimeLogSessions(t.Context(), domain.HostingRuntimeLogClaimInput{InstanceID: batch.InstanceID, Limit: 4}, RuntimeLogTransport{TrustedLoopbackHTTP: true})
					} else {
						_, err = client.AppendRuntimeLogs(t.Context(), batch, RuntimeLogTransport{TrustedLoopbackHTTP: true})
					}
					if (err == nil) != (name == "valid") {
						t.Fatal("unexpected response acceptance", err)
					}
				})
			}
		})
	}
}

type runtimeLogCustomTransport struct{}

func (runtimeLogCustomTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("custom transport must not run")
}
func TestRuntimeLogProtectedTransportAndCapabilityParity(t *testing.T) {
	batch, _, _ := runtimeLogResponseFixtures()
	claim := domain.HostingRuntimeLogClaimInput{InstanceID: batch.InstanceID, Limit: 4}
	reached := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached++; w.WriteHeader(500) }))
	defer server.Close()
	client := New(server.URL, "token")
	if _, err := client.ClaimRuntimeLogSessions(t.Context(), claim); err == nil {
		t.Fatal("implicit cleartext claim accepted")
	}
	if _, err := client.AppendRuntimeLogs(t.Context(), batch); err == nil {
		t.Fatal("implicit cleartext append accepted")
	}
	if reached != 0 {
		t.Fatal("plaintext transport reached server")
	}
	for _, url := range []string{"http://example.com", "http://localhost:1234", "https://user:password@example.com", "https://example.com/a", "https://example.com?x=1", "https://example.com/#fragment"} {
		if ValidateRuntimeLogServer(url, RuntimeLogTransport{TrustedLoopbackHTTP: true}) == nil {
			t.Fatal("ambiguous or remote fixture URL accepted")
		}
	}
	redirected := 0
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			redirected++
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer tlsServer.Close()
	client = New(tlsServer.URL, "token")
	client.HTTP = tlsServer.Client()
	if _, err := client.ClaimRuntimeLogSessions(t.Context(), claim); err == nil {
		t.Fatal("claim redirect accepted")
	}
	if _, err := client.AppendRuntimeLogs(t.Context(), batch); err == nil {
		t.Fatal("append redirect accepted")
	}
	if redirected != 0 {
		t.Fatal("followed redirected bearer request")
	}
	transport := tlsServer.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true
	for _, bad := range []http.RoundTripper{transport, runtimeLogCustomTransport{}} {
		client.HTTP = &http.Client{Transport: bad}
		if _, err := client.ClaimRuntimeLogSessions(t.Context(), claim); err == nil {
			t.Fatal("unverified claim transport accepted")
		}
		if _, err := client.AppendRuntimeLogs(t.Context(), batch); err == nil {
			t.Fatal("unverified append transport accepted")
		}
	}
	for _, caps := range [][]string{{domain.HostingRuntimeLogsCapability}, {domain.HostingRuntimeLogsCapability, domain.HostingStackRuntimeLogsCapability}, {domain.HostingStackRuntimeLogsCapability}, {domain.HostingRuntimeLogsCapability, "shell"}, {"image_deploy"}} {
		identity := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_logs", "capabilities": caps})
		}))
		client = New(identity.URL, "token")
		client.HTTP = identity.Client()
		_, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{RuntimeLogs: true})
		if (err == nil) != (caps[0] == domain.HostingRuntimeLogsCapability && (len(caps) == 1 || caps[1] == domain.HostingStackRuntimeLogsCapability)) {
			t.Fatal("persisted capability parity failed")
		}
		if caps[0] == domain.HostingRuntimeLogsCapability {
			if _, err = client.RuntimeExecutionIdentity(t.Context(), false, false, false); err == nil {
				t.Fatal("disabled logs accepted persisted capability")
			}
		}
		identity.Close()
	}
}
