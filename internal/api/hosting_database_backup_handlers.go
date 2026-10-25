package api

import (
	"io"
	"net/http"
	"strings"

	"nerocd/internal/domain"
)

func hostingDatabaseBackupRunnerPath(path string) bool {
	return path == "/api/v1/runners/hosting/database-backup/plan" || path == "/api/v1/runners/hosting/database-backup/mutation"
}
func hostingDatabaseBackupPath(path string) bool {
	return hostingDatabaseBackupRunnerPath(path) || strings.HasPrefix(path, "/api/v1/database-backups/") || strings.HasSuffix(path, "/database-backups")
}
func databaseBackupParameters(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" || r.Method == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
		writeErrorEnvelope(w, 400, "bad_request", "database backup accepts only declared parameters")
		return false
	}
	return true
}
func decodeDatabaseBackup(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(raw) > 4096 || domain.DecodeHostingStorageProtocolJSON(raw, out) != nil {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid database backup document")
		return false
	}
	return true
}
func (s *Server) hostingDatabaseBackups(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		v, e := s.app.ListDatabaseBackups(r.Context(), r.PathValue("id"))
		proxyResponse(w, 200, v, e)
		return
	}
	var in domain.HostingDatabaseBackupInput
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.RequestDatabaseBackup(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, 202, v, e)
}
func (s *Server) hostingDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	v, e := s.app.GetDatabaseBackup(r.Context(), r.PathValue("id"))
	proxyResponse(w, 200, v, e)
}
func (s *Server) restoreHostingDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseBackupRestoreInput
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.RequestDatabaseBackupRestore(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, 202, v, e)
}
func (s *Server) runnerDatabaseBackupPlan(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingAuthority
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	if in.OperationID == "" || in.RunID == "" || in.LeaseID == "" || in.Fence == "" || in.Attempt < 1 {
		writeErrorEnvelope(w, 400, "bad_request", "database backup authority required")
		return
	}
	v, e := s.app.RunnerDatabaseBackupPlan(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerDatabaseBackupMutation(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseBackupMutation
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	if in.Validate() != nil {
		writeErrorEnvelope(w, 400, "bad_request", "invalid database backup evidence")
		return
	}
	v, e := s.app.RunnerDatabaseBackupMutation(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
