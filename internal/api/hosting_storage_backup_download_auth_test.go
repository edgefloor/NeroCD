package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStorageBackupArchiveRequiresAuthentication(t *testing.T) {
	server, _ := newTestServer()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/storage-backups/backup/archive", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("archive authentication status %d", response.Code)
	}
}
