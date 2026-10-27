package api

import (
	"nerocd/internal/domain"
	"net/http"
	"strings"
)

func hostingDatabaseBackupSchedulePath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/resources/") && strings.Contains(path, "/database-backup-schedules")
}
func (s *Server) hostingDatabaseBackupSchedules(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		v, e := s.app.ListDatabaseBackupSchedules(r.Context(), r.PathValue("id"))
		proxyResponse(w, 200, v, e)
		return
	}
	s.saveDatabaseBackupSchedule(w, r)
}
func (s *Server) saveDatabaseBackupSchedule(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	// Explicit pointers distinguish disabling from an omitted/null boolean and
	// creation's version zero from an omitted compare-and-swap precondition.
	var in struct {
		Component                   string `json:"component,omitempty"`
		ExpectedDeployedOperationID string `json:"expected_deployed_operation_id,omitempty"`
		RequestID                   string `json:"request_id"`
		ExpectedResourceGeneration  int64  `json:"expected_resource_generation"`
		ExpectedVersion             *int64 `json:"expected_version"`
		Frequency                   string `json:"frequency"`
		Timezone                    string `json:"timezone"`
		Enabled                     *bool  `json:"enabled"`
	}
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	if in.ExpectedVersion == nil || in.Enabled == nil {
		writeErrorEnvelope(w, 400, "invalid_request", "backup schedule preconditions required")
		return
	}
	v, e := s.app.SaveDatabaseBackupSchedule(r.Context(), r.PathValue("id"), r.PathValue("schedule_id"), domain.HostingDatabaseBackupScheduleInput{Component: in.Component, ExpectedDeployedOperationID: in.ExpectedDeployedOperationID, RequestID: in.RequestID, ExpectedResourceGeneration: in.ExpectedResourceGeneration, ExpectedVersion: *in.ExpectedVersion, Frequency: in.Frequency, Timezone: in.Timezone, Enabled: *in.Enabled})
	proxyResponse(w, 200, v, e)
}
func (s *Server) hostingDatabaseBackupOccurrences(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	v, e := s.app.ListDatabaseBackupOccurrences(r.Context(), r.PathValue("id"), r.PathValue("schedule_id"))
	proxyResponse(w, 200, v, e)
}
