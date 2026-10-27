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

func TestHostingObservationClientUsesBoundedClosedProtocol(t *testing.T) {
	for _, response := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", `{"targets":[],"next_cursor":"","end_of_sweep":true}`, true},
		{"unknown", `{"targets":[],"socket":"hidden"}`, false},
		{"concatenated", `{"targets":[]} {}`, false},
		{"oversized", strings.Repeat(" ", 512<<10) + `{}`, false},
	} {
		t.Run(response.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/runners/hosting-observations/targets" || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("wrong protocol")
				}
				_, _ = w.Write([]byte(response.body))
			}))
			defer server.Close()
			_, err := New(server.URL, "token").HostingObservationTargets(t.Context(), domain.HostingObservationTargetsInput{Limit: 50}, ObservationTransport{TrustedLoopbackHTTP: true})
			if (err == nil) != response.valid {
				t.Fatal(err)
			}
		})
	}
	body := domain.HostingRuntimeObservation{BindingID: "b", SampleID: "s", ContainerState: "exited", Readiness: "not_checked", ExitCode: new(int)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/hosting-observations/report" {
			t.Error(r.URL.Path)
		}
		var actual domain.HostingRuntimeObservation
		if err := json.NewDecoder(r.Body).Decode(&actual); err != nil || actual.SampleID != "s" {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(domain.HostingObservationAck{SampleID: "s", BodyHash: domain.HostingDocumentHash(body), ReceivedAt: time.Now().UTC()})
	}))
	defer server.Close()
	if ack, err := New(server.URL, "token").ReportHostingObservation(t.Context(), body, ObservationTransport{TrustedLoopbackHTTP: true}); err != nil || ack.SampleID != "s" {
		t.Fatal(ack, err)
	}
}

func TestHostingObservationIdentityParity(t *testing.T) {
	for _, capabilities := range [][]string{{domain.HostingObservationCapability}, {domain.HostingObservationCapability, "shell"}, {"image_deploy"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_observer", "capabilities": capabilities})
		}))
		client := New(server.URL, "token")
		_, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Observation: true})
		if (err == nil) != (len(capabilities) == 1 && capabilities[0] == domain.HostingObservationCapability) {
			t.Fatal("observer capability did not match explicit mode")
		}
		if capabilities[0] == domain.HostingObservationCapability {
			if _, err = client.RuntimeExecutionIdentity(t.Context(), false, false, false); err == nil {
				t.Fatal("disabled observation accepted persisted capability")
			}
		}
		server.Close()
	}
}
func TestHostingObservationClientStrictResponseAndTransport(t *testing.T) {
	for _, body := range []string{`{"targets":[],"targets":[],"next_cursor":"","end_of_sweep":true}`, `{"Targets":[],"next_cursor":"","end_of_sweep":true}`, `{"targets":null,"next_cursor":"","end_of_sweep":true}`, `{"targets":[],"next_cursor":""}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := New(server.URL, "token").HostingObservationTargets(t.Context(), domain.HostingObservationTargetsInput{Limit: 4}, ObservationTransport{TrustedLoopbackHTTP: true})
		server.Close()
		if err == nil {
			t.Fatal("malformed page accepted")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/redirect", http.StatusFound) }))
	defer server.Close()
	if _, err := New(server.URL, "token").HostingObservationTargets(t.Context(), domain.HostingObservationTargetsInput{Limit: 4}); err == nil {
		t.Fatal("implicit cleartext accepted")
	}
	if _, err := New(server.URL, "token").HostingObservationTargets(t.Context(), domain.HostingObservationTargetsInput{Limit: 4}, ObservationTransport{TrustedLoopbackHTTP: true}); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestObservationIdentityDisallowsRedirectsAndInsecureTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) }))
	defer server.Close()
	client := New(server.URL, "token")
	client.HTTP = server.Client()
	if _, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Observation: true}); err == nil {
		t.Fatal("observation identity followed redirect")
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true
	client.HTTP = &http.Client{Transport: transport}
	if _, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Observation: true}); err == nil {
		t.Fatal("observation identity accepted disabled TLS verification")
	}
}

func TestHostingObservationClientStorageEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, storage string
		valid         bool
	}{
		{"legacy-stateless", "", true},
		{"managed", `,"storage":{"volume_name":"ncdv_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mount_path":"/data"}`, true},
		{"null", `,"storage":null`, false},
		{"missing-path", `,"storage":{"volume_name":"ncdv_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, false},
		{"unknown", `,"storage":{"volume_name":"ncdv_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mount_path":"/data","bind":"/"}`, false},
		{"case-alias", `,"storage":{"Volume_name":"ncdv_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mount_path":"/data"}`, false},
		{"duplicate", `,"storage":{"volume_name":"a","volume_name":"b","mount_path":"/data"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(domain.HostingObservationTarget{})
			if err != nil {
				t.Fatal(err)
			}
			body := `{"targets":[` + strings.TrimSuffix(string(raw), "}") + tc.storage + `}],"next_cursor":"","end_of_sweep":true}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			page, err := New(server.URL, "token").HostingObservationTargets(t.Context(), domain.HostingObservationTargetsInput{Limit: 4}, ObservationTransport{TrustedLoopbackHTTP: true})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if tc.valid && tc.storage != "" && (len(page.Targets) != 1 || page.Targets[0].Storage == nil || page.Targets[0].Storage.MountPath != "/data") {
				t.Fatal("authorized mount evidence lost in transport")
			}
		})
	}
}
