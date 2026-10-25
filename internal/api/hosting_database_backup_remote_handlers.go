package api

import (
	"io"
	"mime"
	"net/http"
	"strings"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func hostingDatabaseBackupRemotePath(path string) bool {
	return strings.Contains(path, "/database-backup-targets") || strings.HasPrefix(path, "/api/v1/database-backup-transfers/") || hostingDatabaseBackupTransferRunnerPath(path)
}
func hostingDatabaseBackupTransferRunnerPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/runners/hosting/database-backup-transfer/")
}

func (s *Server) runnerBackupRemoteCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	content, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid JSON body")
		return
	}
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.BackupRemoteCredentialRequest
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	if !s.runtimeEnvironmentTransport(r) {
		writeError(w, app.ErrHostingDatabaseCredentialsUnavailable)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, app.ErrHostingDatabaseCredentialsUnavailable)
		return
	}
	out, err := s.app.RunnerBackupRemoteCredentials(r.Context(), in, token)
	if err != nil {
		writeError(w, err)
		return
	}
	defer out.Close()
	w.Header().Set("Content-Type", "application/json")
	if domain.WriteBackupRemoteCredentials(w, out) != nil {
		s.logger.Error("remote backup credential response failed")
	}
}

func (s *Server) databaseBackupTargets(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		v, e := s.app.ListDatabaseBackupTargets(r.Context(), r.PathValue("project_id"))
		proxyResponse(w, 200, v, e)
		return
	}
	var wire struct {
		domain.HostingDatabaseBackupTargetInput
		Credentials *struct {
			Access string `json:"access_key"`
			Secret string `json:"secret_key"`
		} `json:"credentials,omitempty"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	defer clear(raw)
	if err != nil || len(raw) > 8192 || domain.DecodeHostingStorageProtocolJSON(raw, &wire) != nil {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid backup target document")
		return
	}
	var credentials *app.BackupTargetCredentials
	if wire.Credentials != nil {
		credentials, err = app.NewBackupTargetCredentials([]byte(wire.Credentials.Access), []byte(wire.Credentials.Secret))
		wire.Credentials.Access = ""
		wire.Credentials.Secret = ""
		if err != nil {
			writeErrorEnvelope(w, 400, "invalid_request", "invalid backup target credentials")
			return
		}
		defer credentials.Close()
	}
	v, e := s.app.SaveDatabaseBackupTarget(r.Context(), r.PathValue("project_id"), r.PathValue("target_id"), wire.HostingDatabaseBackupTargetInput, credentials)
	proxyResponse(w, 200, v, e)
}
func (s *Server) databaseBackupTargetValidations(w http.ResponseWriter, r *http.Request) {
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
		v, e := s.app.ListDatabaseBackupTransfers(r.Context(), r.PathValue("target_id"), true)
		proxyResponse(w, 200, v, e)
		return
	}
	var in domain.HostingDatabaseBackupTargetValidationInput
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.ValidateDatabaseBackupTarget(r.Context(), r.PathValue("project_id"), r.PathValue("target_id"), in)
	proxyResponse(w, 202, v, e)
}
func (s *Server) databaseBackupTransfers(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	v, e := s.app.ListDatabaseBackupTransfers(r.Context(), r.PathValue("id"), false)
	proxyResponse(w, 200, v, e)
}
func (s *Server) databaseBackupTransfer(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	v, e := s.app.GetDatabaseBackupTransfer(r.Context(), r.PathValue("id"))
	proxyResponse(w, 200, v, e)
}
func (s *Server) retryDatabaseBackupTransfer(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseBackupTransferRetryInput
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.RetryDatabaseBackupTransfer(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, 202, v, e)
}
func (s *Server) runnerDatabaseBackupTransferPlan(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingAuthority
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.RunnerDatabaseBackupTransferPlan(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerDatabaseBackupTransferMutation(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseBackupTransferMutation
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.RunnerDatabaseBackupTransferMutation(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
