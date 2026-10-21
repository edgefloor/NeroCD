package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestAuditEventsHTTPFiltersMembershipBeforePaging(t *testing.T) {
	server, memory := newTestServer()
	viewerToken := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	createdAt := time.Now().UTC().Add(-time.Hour)
	if err := memory.CreateAuditEvent(t.Context(), domain.AuditEvent{ID: "audit_http_allowed_older", ActorID: "usr_other", Action: "allowed.http", TargetID: "proj_security", CreatedAt: createdAt}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 101; index++ {
		if err := memory.CreateAuditEvent(t.Context(), domain.AuditEvent{
			ID: fmt.Sprintf("audit_http_denied_newer_%03d", index), ActorID: "usr_other", Action: "denied.http",
			TargetID: "proj_platform", CreatedAt: createdAt.Add(time.Duration(index+1) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events?limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/audit-events returned %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Items  []domain.AuditEvent `json:"items"`
		Limit  int                 `json:"limit"`
		Offset int                 `json:"offset"`
		Count  int                 `json:"count"`
		Total  int                 `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Limit != 1 || response.Offset != 0 || response.Count != 1 || response.Total < 1 || len(response.Items) != 1 || response.Items[0].ID != "audit_http_allowed_older" {
		t.Fatalf("membership-scoped audit response = %#v", response)
	}
}
