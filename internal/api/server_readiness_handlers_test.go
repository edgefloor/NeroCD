package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/getkin/kin-openapi/openapi3"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerReadinessHTTPAuthorizationAndStrictDecoder(t *testing.T) {
	s, mem := newTestServer()
	user := createTestSession(t, s)
	token := "readiness_runner_token"
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_readiness", Name: "readiness", TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token))), Status: domain.RunnerActive, Capabilities: []string{domain.ServerReadinessCapability}, RegisteredAt: time.Now(), LastHeartbeatAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, credential, body string, want int) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		if rec.Code != want {
			t.Fatalf("%s %d want %d %s", path, rec.Code, want, rec.Body.String())
		}
	}
	challenge := "/api/v1/runner/server-readiness/challenge"
	report := "/api/v1/runner/server-readiness/report"
	for _, path := range []string{challenge, report} {
		call("POST", path, "", `{}`, 401)
		call("POST", path, user, `{}`, 401)
		call("POST", path, token, strings.Repeat(" ", 8193)+`{}`, 413)
	}
	call("POST", challenge, token, `{}`, 503)
	for _, body := range []string{`null`, `{"server_id":"client"}`, `{"profile":1,"profile":2}`, `{"Profile":1}`, `{} {}`} {
		call("POST", challenge, token, body, 400)
	}
	call("POST", report, token, `{}`, 400)
	call("GET", "/api/v1/servers/server/readiness", "", "", 401)
	call("GET", "/api/v1/servers/server/readiness", token, "", 401)
	call("GET", "/api/v1/servers/server/readiness", user, "", 503)
}

func TestServerReadinessHTTPPostgresAuthorityAndSchema(t *testing.T) {
	server, _, database := newHostingBuildAPIFixture(t)
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	enrollment, err := server.app.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_readiness_api", RunnerName: "readiness", Capabilities: []string{domain.RunTypeComposeDeploy, domain.ServerReadinessCapability}})
	if err != nil {
		t.Fatal(err)
	}
	token := "readiness_api_token"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	enrolled, err := server.app.ConsumeRunnerEnrollment(t.Context(), enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := server.app.CreateServer(admin, app.ServerInput{Name: "readiness", RunnerID: enrolled.Runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	user := createTestSession(t, server)
	document, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, credential, body string, want int, schema string) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+credential)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %d want %d %s", path, rec.Code, want, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		if schema != "" {
			var value any
			if e := json.Unmarshal(rec.Body.Bytes(), &value); e != nil {
				t.Fatal(e)
			}
			if e := document.Components.Schemas[schema].Value.VisitJSON(value); e != nil {
				t.Fatal("schema equality", e)
			}
		}
		return rec.Body.Bytes()
	}
	path := "/api/v1/runner/server-readiness/challenge"
	var challenge domain.ServerReadinessChallenge
	if err = json.Unmarshal(request("POST", path, token, `{}`, 200, "ServerReadinessChallenge"), &challenge); err != nil {
		t.Fatal(err)
	}
	body := domain.ServerReadinessReport{SampleID: challenge.SampleID, Profile: challenge.Profile, OS: "linux", Architecture: "amd64", Execution: "enabled", Configuration: "matched", Docker: "available", DockerAPIVersion: "1.54", DaemonID: "daemon_api", DaemonOS: "linux", DaemonArchitecture: "amd64", CLI: "available", WorkRoot: "accessible", JournalRoot: "accessible", WorkFreeBytes: 1 << 30, JournalFreeBytes: 1 << 30}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	reportPath := "/api/v1/runner/server-readiness/report"
	ack := request("POST", reportPath, token, string(raw), 200, "ServerReadinessAck")
	if again := request("POST", reportPath, token, string(raw), 200, "ServerReadinessAck"); string(again) != string(ack) {
		t.Fatal("ack changed")
	}
	request("GET", "/api/v1/servers/"+detail.Server.ID+"/readiness", user, "", 200, "ServerReadiness")
	for _, mutation := range []string{`UPDATE runners SET capabilities=array_append(capabilities,'shell')`, `UPDATE runners SET capabilities=array_remove(array_remove(capabilities,'shell'),'hosting_readiness_v1')`, `UPDATE runners SET capabilities=array_append(capabilities,'hosting_readiness_v1'); UPDATE runner_enrollments SET revoked_at=clock_timestamp()`} {
		if _, err = database.Exec(t.Context(), mutation); err != nil {
			t.Fatal(err)
		}
		request("POST", path, token, `{}`, 409, "")
		request("POST", reportPath, token, string(raw), 409, "")
	}
}
