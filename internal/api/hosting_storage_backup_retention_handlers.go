package api

import (
	"net/http"
	"strings"

	"nerocd/internal/domain"
)

func hostingStorageBackupRetentionPath(path string) bool {
	return strings.Contains(path, "/storage-backup-retention")
}
func hostingStorageBackupRetentionRunnerPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/runners/hosting/storage-backup-retention/")
}
func (s *Server) hostingStorageBackupRetention(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		v, e := s.app.GetStorageBackupRetention(r.Context(), r.PathValue("id"))
		proxyResponse(w, 200, v, e)
		return
	}
	var in domain.HostingStorageBackupRetentionInput
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.SaveStorageBackupRetention(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) previewStorageBackupRetention(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingStorageBackupRetentionPreviewInput
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.PreviewStorageBackupRetention(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) executeStorageBackupRetention(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		v, e := s.app.ListStorageBackupRetentionExecutions(r.Context(), r.PathValue("id"))
		proxyResponse(w, 200, v, e)
		return
	}
	var in domain.HostingStorageBackupRetentionExecutionInput
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.ExecuteStorageBackupRetention(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, 202, v, e)
}
func (s *Server) getStorageBackupRetentionExecution(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	v, e := s.app.GetStorageBackupRetentionExecution(r.Context(), r.PathValue("id"), r.PathValue("execution_id"))
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerStorageBackupRetentionPlan(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingAuthority
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.RunnerStorageBackupRetentionPlan(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerStorageBackupRetentionMutation(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingStorageBackupRetentionMutation
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.RunnerStorageBackupRetentionMutation(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerStorageBackupRetentionRetirement(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingStorageBackupRetentionRetirementInput
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.RunnerStorageBackupRetentionRetirement(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
