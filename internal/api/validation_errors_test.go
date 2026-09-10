package api

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/store"
)

func TestWriteErrorMapsInvalidInput(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, app.InvalidInput(errors.New("missing field")))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("writeError(InvalidInput) status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got, want := rec.Body.String(), "{\"code\":\"bad_request\",\"error\":\"invalid request\"}\n"; got != want {
		t.Errorf("writeError(InvalidInput) body = %q, want %q", got, want)
	}
}

func TestWriteErrorJoinedInvalidInputPreservesExistingPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		existing   error
		wantStatus int
		wantCode   string
	}{
		{name: "unauthenticated", existing: auth.ErrUnauthenticated, wantStatus: http.StatusUnauthorized, wantCode: "unauthenticated"},
		{name: "forbidden", existing: auth.ErrForbidden, wantStatus: http.StatusForbidden, wantCode: "forbidden"},
		{name: "rate limited", existing: auth.ErrRateLimited, wantStatus: http.StatusTooManyRequests, wantCode: "rate_limited"},
		{name: "invalid credentials", existing: auth.ErrInvalidCredentials, wantStatus: http.StatusUnauthorized, wantCode: "invalid_credentials"},
		{name: "filesystem not found", existing: fs.ErrNotExist, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "store not found", existing: store.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "conflict", existing: store.ErrConflict, wantStatus: http.StatusConflict, wantCode: "conflict"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(rec, errors.Join(app.InvalidInput(errors.New("validation failed")), test.existing))

			if rec.Code != test.wantStatus {
				t.Errorf("writeError(joined %s) status = %d, want %d", test.name, rec.Code, test.wantStatus)
			}
			if got, want := rec.Body.String(), errorEnvelope(test.wantCode); got != want {
				t.Errorf("writeError(joined %s) body = %q, want %q", test.name, got, want)
			}
		})
	}
}

func errorEnvelope(code string) string {
	messages := map[string]string{
		"unauthenticated":     "authentication is required",
		"forbidden":           "access is denied",
		"rate_limited":        "request rate is limited",
		"invalid_credentials": "invalid credentials",
		"not_found":           "resource not found",
		"conflict":            "request conflicts with current state",
	}
	return "{\"code\":\"" + code + "\",\"error\":\"" + messages[code] + "\"}\n"
}
