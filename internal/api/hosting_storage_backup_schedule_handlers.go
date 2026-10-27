package api

import (
	"net/http"
	"strings"

	"nerocd/internal/domain"
)

func hostingStorageBackupSchedulePath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/resources/") && strings.Contains(path, "/storage-backup-schedules")
}
func (s *Server) hostingStorageBackupSchedules(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		v, e := s.app.ListStorageBackupSchedules(r.Context(), r.PathValue("id"))
		proxyResponse(w, 200, v, e)
		return
	}
	s.saveStorageBackupSchedule(w, r)
}
func (s *Server) saveStorageBackupSchedule(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	// Explicit pointers distinguish disabling from an omitted/null boolean and
	// creation's version zero from an omitted compare-and-swap precondition.
	var in struct {
		Volume                      string                        `json:"volume,omitempty"`
		ExpectedDeployedOperationID string                        `json:"expected_deployed_operation_id,omitempty"`
		AcknowledgeInterruption     bool                          `json:"acknowledge_interruption,omitempty"`
		RemoteTarget                *domain.BackupTargetSelection `json:"remote_target,omitempty"`
		RequestID                   string                        `json:"request_id"`
		ExpectedResourceGeneration  int64                         `json:"expected_resource_generation"`
		ExpectedVersion             *int64                        `json:"expected_version"`
		Frequency                   string                        `json:"frequency"`
		Timezone                    string                        `json:"timezone"`
		Enabled                     *bool                         `json:"enabled"`
	}
	if !decodeStorageArchive(w, r, &in) {
		return
	}
	if in.ExpectedVersion == nil || in.Enabled == nil {
		writeErrorEnvelope(w, 400, "invalid_request", "backup schedule preconditions required")
		return
	}
	v, e := s.app.SaveStorageBackupSchedule(r.Context(), r.PathValue("id"), r.PathValue("schedule_id"), domain.HostingStorageBackupScheduleInput{Volume: in.Volume, ExpectedDeployedOperationID: in.ExpectedDeployedOperationID, AcknowledgeInterruption: in.AcknowledgeInterruption, RemoteTarget: in.RemoteTarget, RequestID: in.RequestID, ExpectedResourceGeneration: in.ExpectedResourceGeneration, ExpectedVersion: *in.ExpectedVersion, Frequency: in.Frequency, Timezone: in.Timezone, Enabled: *in.Enabled})
	proxyResponse(w, 200, v, e)
}
func (s *Server) hostingStorageBackupOccurrences(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	v, e := s.app.ListStorageBackupOccurrences(r.Context(), r.PathValue("id"), r.PathValue("schedule_id"))
	proxyResponse(w, 200, v, e)
}
