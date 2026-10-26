package api

import (
	"net/http"
	"strings"

	"nerocd/internal/domain"
)

func hostingStorageBackupDeletionPath(path string) bool {
	return strings.Contains(path, "/storage-backups/") && (strings.HasSuffix(path, "/deletion-previews") || strings.Contains(path, "/deletions"))
}
func (s *Server) previewStorageBackupDeletion(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingStorageBackupDeletionPreviewInput
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.PreviewStorageBackupDeletion(r.Context(), r.PathValue("id"), r.PathValue("backup_id"), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) executeStorageBackupDeletion(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		v, e := s.app.ListStorageBackupDeletions(r.Context(), r.PathValue("id"), r.PathValue("backup_id"))
		proxyResponse(w, 200, v, e)
		return
	}
	var in domain.HostingStorageBackupDeletionInput
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.ExecuteStorageBackupDeletion(r.Context(), r.PathValue("id"), r.PathValue("backup_id"), in)
	proxyResponse(w, 202, v, e)
}
func (s *Server) getStorageBackupDeletion(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	v, e := s.app.GetStorageBackupDeletion(r.Context(), r.PathValue("id"), r.PathValue("backup_id"), r.PathValue("execution_id"))
	proxyResponse(w, 200, v, e)
}
