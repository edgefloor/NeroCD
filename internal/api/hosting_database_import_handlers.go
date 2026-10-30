package api

import (
	"io"
	"net/http"
	"strings"

	"nerocd/internal/domain"
)

func hostingDatabaseImportPath(path string) bool {
	return strings.Contains(path, "/database-import-stages") || strings.Contains(path, "/database-imports")
}

func (s *Server) requestDatabaseImportStage(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseImportStageInput
	if !decodeDatabaseImport(w, r, &in) {
		return
	}
	out, err := s.app.RequestDatabaseImportStage(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusAccepted, out, err)
}

func (s *Server) getDatabaseImportStage(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	out, err := s.app.GetDatabaseImportStage(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) requestDatabaseImport(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseImportInput
	if !decodeDatabaseImport(w, r, &in) {
		return
	}
	out, err := s.app.RequestDatabaseImport(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusAccepted, out, err)
}

func (s *Server) getDatabaseImport(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	out, err := s.app.GetDatabaseImport(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, out, err)
}

func hostingDatabaseImportRunnerPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/runners/hosting/database-import/") || strings.HasPrefix(path, "/api/v1/runners/hosting/database-import-stage/")
}
func (s *Server) runnerDatabaseImportStagePlan(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingAuthority
	if !decodeDatabaseImport(w, r, &in) {
		return
	}
	v, e := s.app.RunnerDatabaseImportStagePlan(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerDatabaseImportPlan(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingAuthority
	if !decodeDatabaseImport(w, r, &in) {
		return
	}
	v, e := s.app.RunnerDatabaseImportPlan(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
func (s *Server) runnerDatabaseImportMutation(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseImportMutation
	if !decodeDatabaseImport(w, r, &in) {
		return
	}
	stageRoute := strings.HasPrefix(r.URL.Path, "/api/v1/runners/hosting/database-import-stage/")
	if stageRoute != (in.RunID == "db_import_stage_"+in.OperationID) {
		writeErrorEnvelope(w, 400, "invalid_request", "import family mismatch")
		return
	}
	v, e := s.app.RunnerDatabaseImportMutation(r.Context(), in)
	proxyResponse(w, 200, v, e)
}

func decodeDatabaseImport(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	if e != nil || len(raw) > 4096 || domain.DecodeHostingDatabaseBackupJSON(raw, out) != nil {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid database import document")
		return false
	}
	return true
}
func (s *Server) runnerDatabaseImportStageCleanup(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseImportStageCleanup
	if !decodeDatabaseImport(w, r, &in) {
		return
	}
	v, e := s.app.RunnerDatabaseImportStageCleanup(r.Context(), in)
	proxyResponse(w, 200, v, e)
}
