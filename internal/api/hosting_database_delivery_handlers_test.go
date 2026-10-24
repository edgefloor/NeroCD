package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type databaseCredentialHTTPRepository struct {
	*store.MemoryStore
	value             store.HostingDatabaseCredentialAuthorization
	calls, finalCalls int
	hash              string
}

func (r *databaseCredentialHTTPRepository) AuthorizeHostingDatabaseCredentials(_ context.Context, runner, hash string, in domain.HostingDatabaseCredentialRequest) (store.HostingDatabaseCredentialAuthorization, error) {
	r.calls++
	r.hash = hash
	if runner != r.value.Metadata.RunnerID || in.Authority != r.value.Metadata.Authority {
		return store.HostingDatabaseCredentialAuthorization{}, store.ErrConflict
	}
	v := r.value
	v.Records = map[domain.HostingDatabaseCredentialRef]store.ManagedSecretRecord{}
	for ref, record := range r.value.Records {
		record.Envelope.Data = bytes.Clone(record.Envelope.Data)
		v.Records[ref] = record
	}
	return v, nil
}

func (r *databaseCredentialHTTPRepository) RevalidateHostingDatabaseCredentials(_ context.Context, runner, hash string, in domain.HostingDatabaseCredentialRequest, m hostingdatabaseprotocol.Metadata) error {
	r.finalCalls++
	if runner != m.RunnerID || hash != r.hash || m.RequestHash != hostingdatabaseprotocol.RequestHash(runner, in) {
		return store.ErrConflict
	}
	return nil
}

func TestDatabaseCredentialHTTPAuthorizationSchemaAndNoStore(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			op := databaseOperationResponse(t, engine)
			in := domain.HostingDatabaseCredentialRequest{Authority: domain.HostingAuthority{OperationID: op.ID, RunID: op.RunID, LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: op.Plan.Hash, Mode: "deploy"}
			mem := newSeededTestStore()
			token := "database-http-bearer"
			hash := fmtRuntimeCredentialHash(token)
			if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: op.Snapshot.RunnerID, Name: op.Snapshot.RunnerID, TokenHash: hash, Status: domain.RunnerActive, Capabilities: []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}}); err != nil {
				t.Fatal(err)
			}
			ring, err := secretcrypto.New("database-http", map[string][]byte{"database-http": bytes.Repeat([]byte{7}, 32)})
			if err != nil {
				t.Fatal(err)
			}
			m := hostingdatabaseprotocol.Metadata{RunnerID: op.Snapshot.RunnerID, Authority: in.Authority, DeliveryID: in.DeliveryID, Mode: in.Mode, PlanHash: in.PlanHash, Database: op.Snapshot.Database, RequestHash: hostingdatabaseprotocol.RequestHash(op.Snapshot.RunnerID, in), Purpose: hostingdatabaseprotocol.Purpose, UseUntil: time.Now().Add(30 * time.Second), References: op.Plan.Database.Credentials}
			repo := &databaseCredentialHTTPRepository{MemoryStore: mem, value: store.HostingDatabaseCredentialAuthorization{ProjectID: op.ProjectID, Metadata: m, Records: map[domain.HostingDatabaseCredentialRef]store.ManagedSecretRecord{}}}
			for _, ref := range m.References {
				envelope, e := ring.Seal(secretcrypto.Binding{ProjectID: op.ProjectID, SecretID: ref.SecretID, Version: ref.Version}, []byte(strings.Repeat("A", 43)))
				if e != nil {
					t.Fatal(e)
				}
				repo.value.Records[ref] = store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: ref.SecretID, Version: ref.Version}, Envelope: envelope}
			}
			service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, SecretKeyring: ring, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			server := NewServer(service, slog.New(slog.NewTextHandler(&logs, nil)), nil)
			body, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			request := func(method, url, bearer, payload string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, url, strings.NewReader(payload))
				r.Header.Set("Content-Type", "application/json")
				if bearer != "" {
					r.Header.Set("Authorization", "Bearer "+bearer)
				}
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, r)
				if rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("no-store missing")
				}
				return rec
			}
			path := "https://127.0.0.1/api/v1/runners/hosting/database-credentials"
			for _, bearer := range []string{"", "wrong", createTestSession(t, server)} {
				if rec := request("POST", path, bearer, string(body)); rec.Code != 401 {
					t.Fatal("nonrunner bearer accepted", rec.Code)
				}
			}
			if repo.calls != 0 {
				t.Fatal("nonrunner reached authorization")
			}
			for _, payload := range []string{string(body) + "{}", strings.Replace(string(body), `"mode":`, `"mode":"observe","mode":`, 1), strings.TrimSuffix(string(body), "}") + `,"secret_id":"foreign"}`, strings.Repeat("x", 4097)} {
				if rec := request("POST", path, token, payload); rec.Code != 400 {
					t.Fatal("ambiguous intent accepted", rec.Code)
				}
			}
			if repo.calls != 0 {
				t.Fatal("invalid JSON reached repository")
			}
			if rec := request("POST", strings.Replace(path, "https:", "http:", 1), token, string(body)); rec.Code != 503 {
				t.Fatal("untrusted HTTP accepted", rec.Code)
			}
			rec := request("POST", path, token, string(body))
			if rec.Code != 200 || repo.hash != hash || repo.calls != 1 || repo.finalCalls != 1 {
				t.Fatal("dedicated delivery failed", rec.Code, rec.Body.String())
			}
			protected, e := hostingdatabaseprotocol.ReadCredentials(bytes.NewReader(rec.Body.Bytes()))
			if e != nil {
				t.Fatal(e)
			}
			defer protected.Close()
			var value any
			if json.Unmarshal(rec.Body.Bytes(), &value) != nil {
				t.Fatal("response JSON")
			}
			if e = document.Components.Schemas["HostingDatabaseCredentials"].Value.VisitJSON(value); e != nil {
				t.Fatal("actual handler response violates schema", e)
			}
			if e = document.Components.Schemas["HostingDatabaseCredentialRequest"].Value.VisitJSON(map[string]any{"authority": map[string]any{}, "delivery_id": in.DeliveryID, "plan_hash": in.PlanHash, "mode": "cleanup"}); e == nil {
				t.Fatal("schema accepts wrong mode")
			}
			if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), strings.Repeat("A", 43)) || strings.Contains(logs.String(), hash) {
				t.Fatal("protected data logged")
			}
			if rec := request("GET", path, token, string(body)); rec.Code != 404 {
				t.Fatal("wrong method accepted", rec.Code)
			}
		})
	}
}
