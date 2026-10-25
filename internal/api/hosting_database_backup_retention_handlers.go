package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"nerocd/internal/domain"
)

func hostingDatabaseBackupRetentionPath(path string) bool {
	return strings.Contains(path, "/database-backup-retention")
}
func hostingDatabaseBackupRetentionRunnerPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/runners/hosting/database-backup-retention/")
}
func (s *Server) hostingDatabaseBackupRetention(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		v, e := s.app.GetDatabaseBackupRetention(r.Context(), r.PathValue("id"))
		proxyResponse(w, 200, v, e)
		return
	}
	var in domain.HostingDatabaseBackupRetentionInput
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.SaveDatabaseBackupRetention(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) previewDatabaseBackupRetention(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseBackupRetentionPreviewInput
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.PreviewDatabaseBackupRetention(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) executeDatabaseBackupRetention(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		v, e := s.app.ListDatabaseBackupRetentionExecutions(r.Context(), r.PathValue("id"))
		proxyResponse(w, 200, v, e)
		return
	}
	var in domain.HostingDatabaseBackupRetentionExecutionInput
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.ExecuteDatabaseBackupRetention(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, 202, v, e)
}
func (s *Server) getDatabaseBackupRetentionExecution(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	v, e := s.app.GetDatabaseBackupRetentionExecution(r.Context(), r.PathValue("id"), r.PathValue("execution_id"))
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerDatabaseBackupRetentionPlan(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingAuthority
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.RunnerDatabaseBackupRetentionPlan(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerDatabaseBackupRetentionMutation(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseBackupRetentionMutation
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.RunnerDatabaseBackupRetentionMutation(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerDatabaseBackupRetentionRetirement(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseBackupRetentionRetirementInput
	if !decodeBackupRetention(w, r, &in) {
		return
	}
	v, e := s.app.RunnerDatabaseBackupRetentionRetirement(r.Context(), in)
	proxyResponse(w, 200, v, e)
}

// Required zero-valued policy fields must be explicit, including disabling and
// unlimited limits. Closed decoding still rejects unknown/duplicate members.
func decodeBackupRetention(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(raw) > 4096 || domain.DecodeHostingStorageProtocolJSON(raw, out) != nil {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid retention document")
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid retention document")
		return false
	}
	keys := []string{}
	switch out.(type) {
	case *domain.HostingDatabaseBackupRetentionInput:
		keys = []string{"request_id", "expected_revision", "expected_resource_generation", "enabled", "automatic", "local", "remote"}
	case *domain.HostingDatabaseBackupRetentionPreviewInput:
		keys = []string{"request_id", "expected_policy_revision"}
	case *domain.HostingDatabaseBackupRetentionExecutionInput:
		keys = []string{"request_id", "preview_id", "expected_policy_revision", "expected_plan_hash"}
	}
	for _, key := range keys {
		v, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			writeErrorEnvelope(w, 400, "invalid_request", "retention preconditions required")
			return false
		}
	}
	if _, ok := out.(*domain.HostingDatabaseBackupRetentionInput); ok {
		for _, location := range []string{"local", "remote"} {
			var limits map[string]json.RawMessage
			if json.Unmarshal(fields[location], &limits) != nil {
				writeErrorEnvelope(w, 400, "invalid_request", "retention limits required")
				return false
			}
			for _, key := range []string{"keep_count", "keep_days", "max_bytes"} {
				v, ok := limits[key]
				if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
					writeErrorEnvelope(w, 400, "invalid_request", "retention limits required")
					return false
				}
			}
		}
	}
	return true
}
