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
			_, err := New(server.URL, "token").HostingObservationTargets(t.Context(), domain.HostingObservationTargetsInput{Limit: 50})
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
	if ack, err := New(server.URL, "token").ReportHostingObservation(t.Context(), body); err != nil || ack.SampleID != "s" {
		t.Fatal(ack, err)
	}
}
