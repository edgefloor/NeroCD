package api

import (
	"net/http"
	"strings"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Server) storageBackupTargetValidations(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		targets, err := s.app.ListDatabaseBackupTargets(r.Context(), r.PathValue("project_id"))
		if err != nil {
			proxyResponse(w, 200, nil, err)
			return
		}
		found := false
		for _, target := range targets {
			found = found || target.ID == r.PathValue("target_id")
		}
		if !found {
			proxyResponse(w, 200, nil, store.ErrNotFound)
			return
		}
		v, e := s.app.ListStorageBackupTransfers(r.Context(), r.PathValue("target_id"), true)
		proxyResponse(w, 200, v, e)
		return
	}
	var in domain.HostingDatabaseBackupTargetValidationInput
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.ValidateStorageBackupTarget(r.Context(), r.PathValue("project_id"), r.PathValue("target_id"), in)
	proxyResponse(w, 202, v, e)
}
func (s *Server) storageBackupTransfers(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	v, e := s.app.ListStorageBackupTransfers(r.Context(), r.PathValue("id"), false)
	proxyResponse(w, 200, v, e)
}
func (s *Server) storageBackupTransfer(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	v, e := s.app.GetStorageBackupTransfer(r.Context(), r.PathValue("id"))
	proxyResponse(w, 200, v, e)
}
func (s *Server) retryStorageBackupTransfer(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingStorageBackupTransferRetryInput
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.RetryStorageBackupTransfer(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, 202, v, e)
}
func (s *Server) runnerStorageBackupTransferPlan(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingAuthority
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.RunnerStorageBackupTransferPlan(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerStorageBackupTransferMutation(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingStorageBackupTransferMutation
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.RunnerStorageBackupTransferMutation(r.Context(), in)
	proxyResponse(w, 200, v, e)
}

func hostingStorageBackupRemotePath(path string) bool {
	return strings.Contains(path, "/storage-validations") || strings.Contains(path, "/storage-backup-transfers/") || strings.HasPrefix(path, "/api/v1/storage-backups/") && strings.Contains(path, "/transfers") || hostingStorageBackupTransferRunnerPath(path)
}
func hostingStorageBackupTransferRunnerPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/runners/hosting/storage-backup-transfer/")
}
