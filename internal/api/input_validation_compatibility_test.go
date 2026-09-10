package api

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestWriteErrorInputValidationCompatibility(t *testing.T) {
	typed := app.InvalidInput(errors.New("private validation detail"))
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "typed direct", err: typed, wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		{name: "typed wrapped", err: fmt.Errorf("request context: %w", typed), wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		{name: "typed joined", err: errors.Join(errors.New("other failure"), typed), wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		{name: "untyped required", err: errors.New("private field is required"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
		{name: "untyped invalid", err: errors.New("private value is invalid"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
		{name: "unauthenticated precedence", err: errors.Join(typed, auth.ErrUnauthenticated), wantStatus: http.StatusUnauthorized, wantCode: "unauthenticated"},
		{name: "forbidden precedence", err: errors.Join(typed, auth.ErrForbidden), wantStatus: http.StatusForbidden, wantCode: "forbidden"},
		{name: "not found precedence", err: errors.Join(typed, fs.ErrNotExist), wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "conflict precedence", err: errors.Join(typed, store.ErrConflict), wantStatus: http.StatusConflict, wantCode: "conflict"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(rec, test.err)
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, test.wantStatus)
			}
			if got, want := rec.Body.String(), errorEnvelopeWithFallback(test.wantCode); got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
			if strings.Contains(rec.Body.String(), "private") {
				t.Errorf("response leaked private error detail: %q", rec.Body.String())
			}
		})
	}
}

func TestInputValidationProducerEndpointsReturnGenericBadRequest(t *testing.T) {
	server, _ := newTestServer()
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Roles: []string{domain.RoleSystemAdmin}, Provider: domain.PrincipalLocal})
	tests := []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		body string
	}{
		{
			name: "project producer",
			call: server.createProject,
			body: `{}`,
		},
		{
			name: "template producer",
			call: server.createTemplate,
			body: `{"name":"missing project","kind":"shell","run_spec":{"type":"shell","inputs":{"command":"true"}}}`,
		},
		{
			name: "environment blank service",
			call: server.createEnvironment,
			body: "{\"service_id\":\" \",\"name\":\"prod\",\"compose_project\":\"validation\",\"timeout_seconds\":60}",
		},
		{
			name: "revision blank service",
			call: server.createRevision,
			body: "{\"service_id\":\" \",\"requested_ref\":\"main\"}",
		},
		{
			name: "deployment blank environment",
			call: server.createDeployment,
			body: "{\"environment_id\":\" \",\"desired_revision_id\":\"revision\",\"idempotency_key\":\"request\"}",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/test", bytes.NewBufferString(test.body)).WithContext(admin)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			test.call(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if got, want := rec.Body.String(), errorEnvelopeWithFallback("bad_request"); got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
}

func TestRequiredResourceIdentifierEndpointsReturnGenericBadRequest(t *testing.T) {
	server, _ := newTestServer()
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Roles: []string{domain.RoleSystemAdmin}, Provider: domain.PrincipalLocal})
	tests := []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		url  string
		body string
	}{
		{name: "update template absent ID", call: server.updateTemplate, url: "/api/v1/templates", body: `{}`},
		{name: "update template whitespace ID", call: server.updateTemplate, url: "/api/v1/templates", body: `{"id":" "}`},
		{name: "list revisions absent service", call: server.listRevisions, url: "/api/v1/revisions"},
		{name: "list revisions whitespace service", call: server.listRevisions, url: "/api/v1/revisions?service_id=%20"},
		{name: "list deployments absent environment", call: server.listDeployments, url: "/api/v1/deployments"},
		{name: "list deployments whitespace environment", call: server.listDeployments, url: "/api/v1/deployments?environment_id=%20"},
		{name: "confirm deployment absent ID", call: server.confirmDeployment, url: "/api/v1/deployments/confirm", body: `{}`},
		{name: "confirm deployment whitespace ID", call: server.confirmDeployment, url: "/api/v1/deployments/confirm", body: `{"id":" "}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, test.url, bytes.NewBufferString(test.body)).WithContext(admin)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			test.call(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if got, want := rec.Body.String(), errorEnvelopeWithFallback("bad_request"); got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
}

func errorEnvelopeWithFallback(code string) string {
	messages := map[string]string{
		"bad_request":     "invalid request",
		"unauthenticated": "authentication is required",
		"forbidden":       "access is denied",
		"not_found":       "resource not found",
		"conflict":        "request conflicts with current state",
		"internal_error":  "internal server error",
	}
	return `{"code":"` + code + `","error":"` + messages[code] + `"}` + "\n"
}
