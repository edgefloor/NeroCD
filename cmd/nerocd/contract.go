package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

func validateContract(args []string) error {
	fs := flag.NewFlagSet("contract", flag.ExitOnError)
	openAPIPath := fs.String("openapi", "openapi.yaml", "OpenAPI contract path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	document, err := loadOpenAPIContract(*openAPIPath)
	if err != nil {
		return err
	}
	documented, err := readOpenAPIOperations(document, *openAPIPath)
	if err != nil {
		return err
	}
	implemented := make(map[string]struct{})
	for _, route := range api.PublicRoutes() {
		key := route.Method + " " + route.Path
		implemented[key] = struct{}{}
		if _, ok := documented[key]; !ok {
			return fmt.Errorf("%s is implemented but missing from %s", key, *openAPIPath)
		}
	}
	for key := range documented {
		if _, ok := implemented[key]; !ok {
			return fmt.Errorf("%s is documented in %s but not implemented", key, *openAPIPath)
		}
	}
	if err := validateConsumersUseDocumentedRoutes(documented); err != nil {
		return err
	}
	if err := validateOpenAPIOperations(documented); err != nil {
		return err
	}
	if err := validateContractResponses(documented); err != nil {
		return err
	}
	fmt.Printf("ok contract %s (%d routes)\n", *openAPIPath, len(documented))
	return nil
}

type documentedOperation struct {
	Method            string
	Path              string
	OperationID       bool
	SecurityEmpty     bool
	SecuritySchemes   map[string]bool
	RequestBody       bool
	JSONRequestBody   bool
	Responses         map[string]bool
	JSONResponseCodes map[string]bool
}

func loadOpenAPIContract(path string) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	if err := document.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return document, nil
}

func readOpenAPIOperations(document *openapi3.T, path string) (map[string]documentedOperation, error) {
	if document == nil || document.Paths == nil {
		return nil, fmt.Errorf("%s does not document any /api/v1 routes", path)
	}
	routes := make(map[string]documentedOperation)
	operationIDs := make(map[string]string)
	for _, routePath := range document.Paths.Keys() {
		if !strings.HasPrefix(routePath, "/api/v1/") {
			continue
		}
		pathItem := document.Paths.Value(routePath)
		if pathItem == nil {
			continue
		}
		for method, operation := range pathItem.Operations() {
			key := strings.ToUpper(method) + " " + routePath
			if operation != nil && operation.OperationID != "" {
				if previous, exists := operationIDs[operation.OperationID]; exists {
					return nil, fmt.Errorf("operationId %q is duplicated by %s and %s", operation.OperationID, previous, key)
				}
				operationIDs[operation.OperationID] = key
			}
			routes[key] = documentedOperationFromOperation(strings.ToUpper(method), routePath, operation)
		}
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("%s does not document any /api/v1 routes", path)
	}
	return routes, nil
}

func documentedOperationFromOperation(method string, path string, operation *openapi3.Operation) documentedOperation {
	op := documentedOperation{
		Method:            method,
		Path:              path,
		OperationID:       operation != nil && operation.OperationID != "",
		Responses:         map[string]bool{},
		JSONResponseCodes: map[string]bool{},
		SecuritySchemes:   map[string]bool{},
	}
	if operation == nil {
		return op
	}
	if operation.Security != nil && len(*operation.Security) == 0 {
		op.SecurityEmpty = true
	}
	if operation.Security != nil {
		for _, requirement := range *operation.Security {
			for scheme := range requirement {
				op.SecuritySchemes[scheme] = true
			}
		}
	}
	if operation.RequestBody != nil {
		op.RequestBody = true
		if requestBody := operation.RequestBody.Value; requestBody != nil {
			_, op.JSONRequestBody = requestBody.Content["application/json"]
		}
	}
	if operation.Responses != nil {
		for _, code := range operation.Responses.Keys() {
			op.Responses[code] = true
			responseRef := operation.Responses.Value(code)
			if responseRef == nil || responseRef.Value == nil {
				continue
			}
			if _, ok := responseRef.Value.Content["application/json"]; ok {
				op.JSONResponseCodes[code] = true
			}
		}
	}
	return op
}

func validateOpenAPIOperations(documented map[string]documentedOperation) error {
	for key, op := range documented {
		if !op.OperationID {
			return fmt.Errorf("%s is missing operationId", key)
		}
		public := !requiresContractAuth(op.Method, op.Path)
		if public && !op.SecurityEmpty {
			return fmt.Errorf("%s is public in implementation but missing security: []", key)
		}
		if !public && op.SecurityEmpty {
			return fmt.Errorf("%s is protected in implementation but documents security: []", key)
		}
		if !public && !op.Responses["401"] {
			return fmt.Errorf("%s is protected but does not document 401", key)
		}
		if mutates(op.Method, op.Path) && !op.RequestBody {
			return fmt.Errorf("%s mutates state but is missing requestBody", key)
		}
		if op.RequestBody && !op.JSONRequestBody {
			return fmt.Errorf("%s requestBody does not document application/json", key)
		}
		hasSuccess := false
		for code := range op.Responses {
			if strings.HasPrefix(code, "2") {
				hasSuccess = true
				if code == "204" {
					continue
				}
				if !op.JSONResponseCodes[code] {
					return fmt.Errorf("%s response %s does not document application/json content", key, code)
				}
			}
		}
		if !hasSuccess {
			return fmt.Errorf("%s does not document a 2xx response", key)
		}
	}
	return nil
}

func requiresContractAuth(method string, path string) bool {
	return path != "/api/v1/health" && path != "/api/v1/ready" && path != "/api/v1/bootstrap-status" && (method != http.MethodPost || (path != "/api/v1/sessions" && path != "/api/v1/browser-sessions"))
}

func requiresContractRunnerAuth(path string) bool {
	switch path {
	case "/api/v1/runners/heartbeat", "/api/v1/runners/claim", "/api/v1/runners/renew", "/api/v1/runners/lease", "/api/v1/runners/logs", "/api/v1/runners/events/batch", "/api/v1/runners/secrets/access", "/api/v1/runners/artifacts", "/api/v1/runners/complete":
		return true
	default:
		return false
	}
}

func mutates(method string, path string) bool {
	if method == http.MethodDelete && (path == "/api/v1/sessions" || path == "/api/v1/browser-sessions") {
		return false
	}
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func validateContractResponses(documented map[string]documentedOperation) error {
	mem := store.NewMemoryStore()
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		return err
	}
	server := api.NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())

	identitySuffix, err := randomRuntimeHex(16)
	if err != nil {
		return err
	}
	email := "contract-" + identitySuffix + "@example.invalid"
	password, err := randomRuntimeHex(32)
	if err != nil {
		return err
	}
	if _, err := service.BootstrapAdmin(context.Background(), app.BootstrapAdminInput{Email: email, Name: "Contract Operator", Password: password}); err != nil {
		return err
	}
	if err := seedContractMemory(context.Background(), mem); err != nil {
		return err
	}
	sessionBody, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return err
	}
	sessionPayload, err := contractRequest(server, http.MethodPost, "/api/v1/sessions", string(sessionBody), "")
	if err != nil {
		return err
	}
	token, ok := sessionPayload["token"].(string)
	if !ok || token == "" {
		return errors.New("POST /api/v1/sessions did not return token")
	}

	cases := []struct {
		method      string
		path        string
		body        string
		auth        bool
		shape       string
		useAPIToken bool
	}{
		{method: http.MethodGet, path: "/api/v1/health", shape: "health"},
		{method: http.MethodGet, path: "/api/v1/ready", shape: "ready"},
		{method: http.MethodGet, path: "/api/v1/me", auth: true, shape: "principal"},
		{method: http.MethodPost, path: "/api/v1/api-tokens", body: `{"name":"Contract Bootstrap","roles":["system_admin"]}`, auth: true, shape: "api-token-registration"},
		{method: http.MethodGet, path: "/api/v1/capabilities", auth: true, shape: "list"},
		{method: http.MethodGet, path: "/api/v1/projects", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/projects", body: `{"name":"Contract Project","description":"contract"}`, auth: true, shape: "project"},
		{method: http.MethodGet, path: "/api/v1/project-members", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/project-members", body: `{"project_id":"proj_platform","email":"{{contract_email}}","role":"maintainer"}`, auth: true, shape: "project-member"},
		{method: http.MethodGet, path: "/api/v1/project-role?project_id=proj_platform", auth: true, shape: "project-role"},
		{method: http.MethodGet, path: "/api/v1/repositories", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/repositories", body: `{"project_id":"proj_platform","name":"Contract Repo","url":"https://example.local/repo.git"}`, auth: true, shape: "repository"},
		{method: http.MethodPut, path: "/api/v1/repositories/repo_platform_runbooks/policy", body: `{"project_id":"proj_platform","configuration_id":"cfg_contract_policy_01","policy":{"version":1,"state":"configured","mode":"public","allowed_schemes":["https"],"allowed_hosts":["example.local"]}}`, auth: true, shape: "repository"},
		{method: http.MethodGet, path: "/api/v1/access-keys", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/access-keys", body: `{"project_id":"proj_platform","name":"Contract SSH","kind":"ssh","fingerprint":"SHA256:contract"}`, auth: true, shape: "access-key"},
		{method: http.MethodGet, path: "/api/v1/inventories", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/inventories", body: `{"project_id":"proj_platform","name":"Contract Inventory","kind":"static","source":"inventories/contract.ini"}`, auth: true, shape: "inventory"},
		{method: http.MethodGet, path: "/api/v1/templates", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/templates", body: `{"project_id":"proj_platform","name":"Contract Shell","run_spec":{"type":"shell","inputs":{"command":"echo ok"}},"runner_tags":["local"]}`, auth: true, shape: "template"},
		{method: http.MethodGet, path: "/api/v1/runs", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/runs", body: `{"project_id":"proj_platform","run_spec":{"type":"shell","inputs":{"command":"echo ok"},"process":{"command":["echo","ok"]},"artifacts":[{"name":"out","path":"out.txt","required":false}],"secrets":[{"name":"contract-secret","provider":"runner_file","reference":"contract-secret","target":"env:CONTRACT_SECRET","required":true,"version":"v1"}]},"runner_tags":["local"]}`, auth: true, shape: "run"},
		{method: http.MethodPost, path: "/api/v1/runs", body: `{"template_id":"tpl_patch"}`, auth: true, shape: "run"},
		{method: http.MethodPost, path: "/api/v1/runs/reject", body: `{"run_id":"{{reject_run_id}}"}`, auth: true, shape: "approval"},
		{method: http.MethodGet, path: "/api/v1/runners", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/runners/register", body: `{"id":"runner_contract","name":"Contract Runner","tags":["local"],"capabilities":["shell"]}`, auth: true, shape: "registered-runner", useAPIToken: true},
		{method: http.MethodPost, path: "/api/v1/runners/rotate-token", body: `{"runner_id":"runner_contract"}`, auth: true, shape: "registered-runner"},
		{method: http.MethodPost, path: "/api/v1/runners/heartbeat", body: `{}`, auth: true, shape: "runner"},
		{method: http.MethodPost, path: "/api/v1/runners/claim", body: `{}`, auth: true, shape: "claim"},
		{method: http.MethodPost, path: "/api/v1/runners/logs", body: `{"run_id":"{{run_id}}","lease_id":"{{lease_id}}","attempt":{{attempt}},"fence":"{{fence}}","event_key":"contract-event-one","sequence":4,"stream":"stdout","message":"ok"}`, auth: true, shape: "run-log"},
		{method: http.MethodPost, path: "/api/v1/runners/events/batch", body: `{"run_id":"{{run_id}}","lease_id":"{{lease_id}}","attempt":{{attempt}},"fence":"{{fence}}","events":[{"event_key":"contract-event-two","sequence":5,"stream":"stdout","message":"batch ok"}]}`, auth: true, shape: "runner-event-ack"},
		{method: http.MethodPost, path: "/api/v1/runners/secrets/access", body: `{"access_id":"secret_access_0123456789abcdef0123456789abcdef","run_id":"{{run_id}}","lease_id":"{{lease_id}}","attempt":{{attempt}},"fence":"{{fence}}","binding":"contract-secret","provider":"runner_file","version":"v1"}`, auth: true, shape: "secret-access"},
		{method: http.MethodPost, path: "/api/v1/runners/artifacts", body: `{"run_id":"{{run_id}}","lease_id":"{{lease_id}}","attempt":{{attempt}},"fence":"{{fence}}","name":"out","path":"out.txt","found":false,"required":false,"size":0,"kind":"file"}`, auth: true, shape: "artifact"},
		{method: http.MethodPost, path: "/api/v1/runners/complete", body: `{"lease_id":"{{lease_id}}","attempt":{{attempt}},"fence":"{{fence}}","completion_key":"contract-completion-one","status":"succeeded"}`, auth: true, shape: "lease"},
		{method: http.MethodPost, path: "/api/v1/runners/revoke-token", body: `{"runner_id":"runner_contract"}`, auth: true, shape: "runner"},
		{method: http.MethodPost, path: "/api/v1/runs", body: `{"project_id":"proj_platform","run_spec":{"type":"shell","inputs":{"command":"echo cancel"},"process":{"command":["echo","cancel"]}},"runner_tags":["local"]}`, auth: true, shape: "run"},
		{method: http.MethodPost, path: "/api/v1/runs/cancel", body: `{"run_id":"{{cancel_run_id}}"}`, auth: true, shape: "run"},
		{method: http.MethodGet, path: "/api/v1/run-logs", auth: true, shape: "list"},
		{method: http.MethodGet, path: "/api/v1/artifacts", auth: true, shape: "list"},
		{method: http.MethodGet, path: "/api/v1/runner-primitive-plan?run_id=run_001", auth: true, shape: "primitive-plan"},
		{method: http.MethodGet, path: "/api/v1/approvals", auth: true, shape: "list"},
		{method: http.MethodGet, path: "/api/v1/audit-events", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/api-tokens/revoke", body: `{"token_id":"{{api_token_id}}"}`, auth: true, shape: "api-token"},
	}
	leaseID := ""
	attempt := ""
	fence := ""
	runID := ""
	rejectRunID := ""
	cancelRunID := ""
	runnerToken := ""
	apiToken := ""
	apiTokenID := ""
	for _, tc := range cases {
		docPath := strings.SplitN(tc.path, "?", 2)[0]
		key := tc.method + " " + docPath
		if _, ok := documented[key]; !ok && !isDocumentedPath(documented, docPath) {
			return fmt.Errorf("contract response case references undocumented %s", key)
		}
		tokenValue := ""
		if tc.auth {
			tokenValue = token
		}
		if tc.useAPIToken {
			if apiToken == "" {
				return fmt.Errorf("%s requires api token before request", key)
			}
			tokenValue = apiToken
		}
		if requiresContractRunnerAuth(docPath) {
			if runnerToken == "" {
				return fmt.Errorf("%s requires runner token before registration response", key)
			}
			tokenValue = runnerToken
		}
		body := strings.ReplaceAll(tc.body, "{{lease_id}}", leaseID)
		body = strings.ReplaceAll(body, "{{attempt}}", attempt)
		body = strings.ReplaceAll(body, "{{fence}}", fence)
		body = strings.ReplaceAll(body, "{{run_id}}", runID)
		body = strings.ReplaceAll(body, "{{reject_run_id}}", rejectRunID)
		body = strings.ReplaceAll(body, "{{cancel_run_id}}", cancelRunID)
		body = strings.ReplaceAll(body, "{{api_token_id}}", apiTokenID)
		body = strings.ReplaceAll(body, "{{contract_email}}", email)
		payload, err := contractRequest(server, tc.method, tc.path, body, tokenValue)
		if err != nil {
			return err
		}
		if err := validatePayloadShape(key, tc.shape, payload); err != nil {
			return err
		}
		if tc.shape == "registered-runner" {
			value, ok := payload["token"].(string)
			if !ok || value == "" {
				return fmt.Errorf("%s response missing runner token", key)
			}
			runnerToken = value
		}
		if tc.shape == "api-token-registration" {
			value, ok := payload["token"].(string)
			if !ok || value == "" {
				return fmt.Errorf("%s response missing api token", key)
			}
			apiToken = value
			tokenPayload, ok := payload["api_token"].(map[string]any)
			if !ok {
				return fmt.Errorf("%s response missing api_token object", key)
			}
			apiTokenID, ok = tokenPayload["id"].(string)
			if !ok || apiTokenID == "" {
				return fmt.Errorf("%s response api_token missing id", key)
			}
		}
		if tc.shape == "claim" {
			lease, ok := payload["lease"].(map[string]any)
			if !ok {
				return fmt.Errorf("%s response missing lease object", key)
			}
			leaseID, ok = lease["id"].(string)
			if !ok || leaseID == "" {
				return fmt.Errorf("%s response lease missing id", key)
			}
			attempt = strconv.Itoa(int(lease["attempt"].(float64)))
			fence, _ = lease["fence"].(string)
			run, ok := payload["run"].(map[string]any)
			if !ok {
				return fmt.Errorf("%s response missing run object", key)
			}
			runID, ok = run["id"].(string)
			if !ok || runID == "" {
				return fmt.Errorf("%s response run missing id", key)
			}
		}
		if tc.shape == "run" {
			if status, _ := payload["status"].(string); status == "waiting_approval" {
				if id, ok := payload["id"].(string); ok {
					rejectRunID = id
				}
			}
			if status, _ := payload["status"].(string); status == "queued" {
				if id, ok := payload["id"].(string); ok {
					cancelRunID = id
				}
			}
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		return fmt.Errorf("GET /api/v1/projects without auth returned %d, want 401", rec.Code)
	}
	var errorPayload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&errorPayload); err != nil {
		return fmt.Errorf("decode unauthorized error response: %w", err)
	}
	if _, ok := errorPayload["error"].(string); !ok {
		return errors.New("unauthorized response did not match ErrorResponse envelope")
	}
	if code, ok := errorPayload["code"].(string); !ok || code != "unauthenticated" {
		return errors.New("unauthorized response did not include stable unauthenticated code")
	}
	return nil
}

func seedContractMemory(ctx context.Context, mem *store.MemoryStore) error {
	now := time.Now().UTC()
	if _, err := mem.CreateProject(ctx, domain.Project{ID: "proj_platform", Name: "Contract Platform", CreatedAt: now}); err != nil {
		return err
	}
	if _, err := mem.CreateRepository(ctx, domain.Repository{ID: "repo_platform_runbooks", ProjectID: "proj_platform", Name: "Contract Repository", URL: "https://example.invalid/contract.git", Provider: domain.ProviderGit, DefaultRef: "main", CreatedAt: now}); err != nil {
		return err
	}
	if _, err := mem.CreateTemplate(ctx, domain.TaskTemplate{ID: "tpl_patch", ProjectID: "proj_platform", Name: "Contract Template", Kind: "shell", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}, Process: &domain.ProcessSpec{Command: []string{"true"}}}, RunnerTags: []string{"local"}, RequiresAck: true}); err != nil {
		return err
	}
	_, err := mem.CreateRun(ctx, domain.TaskRun{ID: "run_001", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}, Repository: &domain.RepositoryRef{ID: "repo_platform_runbooks", Ref: "main", Path: ""}, Process: &domain.ProcessSpec{Command: []string{"true"}}, Secrets: []domain.SecretBinding{{Name: "contract-secret", Provider: "runner_file", Reference: "contract-secret", Target: "env:CONTRACT_SECRET", Required: true, Version: "v1"}}}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "contract"})
	return err
}

func contractRequest(server http.Handler, method string, path string, body string, token string) (map[string]any, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code >= 300 {
		return nil, fmt.Errorf("%s %s returned %d during contract response validation: %s", method, path, rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return payload, nil
}

func validatePayloadShape(key string, shape string, payload map[string]any) error {
	requireString := func(field string) error {
		if _, ok := payload[field].(string); !ok {
			return fmt.Errorf("%s response missing string field %q", key, field)
		}
		return nil
	}
	switch shape {
	case "health":
		if payload["status"] != "ok" {
			return fmt.Errorf("%s response status = %v, want ok", key, payload["status"])
		}
	case "ready":
		if payload["status"] != "ready" {
			return fmt.Errorf("%s response status = %v, want ready", key, payload["status"])
		}
	case "principal":
		for _, field := range []string{"id", "email", "name", "provider"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["roles"].([]any); !ok {
			return fmt.Errorf("%s response missing roles array", key)
		}
	case "list":
		if _, ok := payload["items"].([]any); !ok {
			return fmt.Errorf("%s response missing items array", key)
		}
	case "project":
		for _, field := range []string{"id", "name", "description", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "project-member":
		for _, field := range []string{"id", "project_id", "user_id", "email", "name", "role", "created_at", "updated_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "project-role":
		for _, field := range []string{"project_id", "role"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		for _, field := range []string{"can_view", "can_run", "can_admin"} {
			if _, ok := payload[field].(bool); !ok {
				return fmt.Errorf("%s response missing boolean field %q", key, field)
			}
		}
	case "template":
		for _, field := range []string{"id", "project_id", "name", "kind"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["run_spec"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing run_spec object", key)
		}
		if _, ok := payload["workflow"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing workflow object", key)
		}
		if _, ok := payload["runner_tags"].([]any); !ok {
			return fmt.Errorf("%s response missing runner_tags array", key)
		}
	case "repository":
		for _, field := range []string{"id", "project_id", "name", "url", "provider", "default_ref", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "access-key":
		for _, field := range []string{"id", "project_id", "name", "kind", "fingerprint", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "inventory":
		for _, field := range []string{"id", "project_id", "name", "kind", "source", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "run":
		for _, field := range []string{"id", "project_id", "status", "requested_by", "started_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["run_spec"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing run_spec object", key)
		}
		if _, ok := payload["workflow"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing workflow object", key)
		}
		if _, ok := payload["runner_tags"].([]any); !ok {
			return fmt.Errorf("%s response missing runner_tags array", key)
		}
	case "runner":
		for _, field := range []string{"id", "name", "status", "registered_at", "last_heartbeat_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["tags"].([]any); !ok {
			return fmt.Errorf("%s response missing tags array", key)
		}
		if _, ok := payload["capabilities"].([]any); !ok {
			return fmt.Errorf("%s response missing capabilities array", key)
		}
	case "registered-runner":
		if err := requireString("token"); err != nil {
			return err
		}
		runnerPayload, ok := payload["runner"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s response missing runner object", key)
		}
		return validatePayloadShape(key, "runner", runnerPayload)
	case "api-token-registration":
		if err := requireString("token"); err != nil {
			return err
		}
		tokenPayload, ok := payload["api_token"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s response missing api_token object", key)
		}
		return validatePayloadShape(key, "api-token", tokenPayload)
	case "api-token":
		for _, field := range []string{"id", "name", "kind", "status", "created_by", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["roles"].([]any); !ok {
			return fmt.Errorf("%s response missing roles array", key)
		}
	case "claim":
		if _, ok := payload["lease"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing lease object", key)
		}
		if _, ok := payload["run"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing run object", key)
		}
		if _, ok := payload["primitive_plan"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing primitive_plan object", key)
		}
	case "lease":
		for _, field := range []string{"id", "run_id", "runner_id", "status", "expires_at", "created_at", "completed_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "run-log":
		for _, field := range []string{"id", "run_id", "stream", "message", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["sequence"].(float64); !ok {
			return fmt.Errorf("%s response missing numeric sequence", key)
		}
	case "runner-event-ack":
		events, ok := payload["events"].([]any)
		if !ok || len(events) == 0 {
			return fmt.Errorf("%s response missing events array", key)
		}
		first, ok := events[0].(map[string]any)
		if !ok {
			return fmt.Errorf("%s response event is not an object", key)
		}
		return validatePayloadShape(key, "run-log", first)
	case "secret-access":
		for _, field := range []string{"access_id", "run_id", "lease_id", "binding", "provider", "authorized_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["attempt"].(float64); !ok {
			return fmt.Errorf("%s response missing numeric attempt", key)
		}
	case "artifact":
		for _, field := range []string{"id", "run_id", "lease_id", "name", "path", "kind", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		for _, field := range []string{"found", "required"} {
			if _, ok := payload[field].(bool); !ok {
				return fmt.Errorf("%s response missing boolean field %q", key, field)
			}
		}
		if _, ok := payload["size"].(float64); !ok {
			return fmt.Errorf("%s response missing numeric size", key)
		}
	case "approval":
		for _, field := range []string{"id", "run_id", "status", "requested_by", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "primitive-plan":
		if err := requireString("run_id"); err != nil {
			return err
		}
		if _, ok := payload["checkout"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing checkout object", key)
		}
		if _, ok := payload["process"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing process object", key)
		}
	default:
		return fmt.Errorf("unknown contract payload shape %q for %s", shape, key)
	}
	return nil
}

func validateConsumersUseDocumentedRoutes(documented map[string]documentedOperation) error {
	files := []string{"cmd/nerocd/main.go", "web/static/app.js", "web/app/src/api.ts"}
	apiPathPattern := regexp.MustCompile(`/api/v1/[A-Za-z0-9/_-]+`)
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		for _, match := range apiPathPattern.FindAllString(string(content), -1) {
			if isDocumentedPath(documented, match) {
				continue
			}
			return fmt.Errorf("%s consumes undocumented API route %s", file, match)
		}
	}
	return nil
}

func isDocumentedPath(documented map[string]documentedOperation, path string) bool {
	for route := range documented {
		documentedPath := strings.TrimPrefix(route[strings.Index(route, " ")+1:], " ")
		if documentedPath == path {
			return true
		}
		want, got := strings.Split(strings.Trim(documentedPath, "/"), "/"), strings.Split(strings.Trim(path, "/"), "/")
		if len(want) != len(got) {
			continue
		}
		matched := true
		for i := range want {
			if (!strings.HasPrefix(want[i], "{") || !strings.HasSuffix(want[i], "}")) && want[i] != got[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
