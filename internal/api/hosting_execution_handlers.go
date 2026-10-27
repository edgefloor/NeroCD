package api

import (
	"net/http"

	"nerocd/internal/domain"
)

func (s *Server) hostingDeploymentRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := s.app.ListHostingDeployments(r.Context(), r.PathValue("id"), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(result))
		return
	}
	var in domain.HostingDeploymentInput
	if !decodeBody(w, r, &in) {
		return
	}
	op, err := s.app.RequestHostingDeployment(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}
func (s *Server) hostingOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.app.HostingOperation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}
func (s *Server) hostingOperationCancel(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decodeBody(w, r, &in) {
		return
	}
	op, err := s.app.CancelHostingOperation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}
func (s *Server) hostingOperationReconcile(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decodeBody(w, r, &in) {
		return
	}
	op, err := s.app.ReconcileHostingOperation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}
func (s *Server) runnerHostingPlan(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingAuthority
	if !decodeBody(w, r, &in) {
		return
	}
	r, ok := s.databaseConnectionRunnerRequest(w, r)
	if !ok {
		return
	}
	op, err := s.app.RunnerHostingPlan(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}
func (s *Server) runnerHostingPrepared(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingPreparedRequest
	if !decodeBody(w, r, &in) {
		return
	}
	r, ok := s.databaseConnectionRunnerRequest(w, r)
	if !ok {
		return
	}
	op, err := s.app.RunnerHostingPrepared(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}
func (s *Server) runnerHostingApplyBegin(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingAuthority
	if !decodeBody(w, r, &in) {
		return
	}
	r, ok := s.databaseConnectionRunnerRequest(w, r)
	if !ok {
		return
	}
	op, err := s.app.RunnerHostingApplyBegin(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}
func (s *Server) runnerHostingComplete(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingCompleteRequest
	if !decodeBody(w, r, &in) {
		return
	}
	r, ok := s.databaseConnectionRunnerRequest(w, r)
	if !ok {
		return
	}
	op, err := s.app.RunnerHostingComplete(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}
func (s *Server) runnerHostingStatus(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingStatusRequest
	if !decodeBody(w, r, &in) {
		return
	}
	r, ok := s.databaseConnectionRunnerRequest(w, r)
	if !ok {
		return
	}
	op, err := s.app.RunnerHostingStatus(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}
func (s *Server) runnerHostingReconciliation(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingObservation
	if !decodeBody(w, r, &in) {
		return
	}
	r, ok := s.databaseConnectionRunnerRequest(w, r)
	if !ok {
		return
	}
	op, err := s.app.RunnerHostingReconciliation(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (s *Server) runnerHostingStackVolumes(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingStackVolumeRequest
	if !decodeBody(w, r, &in) {
		return
	}
	r, ok := s.databaseConnectionRunnerRequest(w, r)
	if !ok {
		return
	}
	op, err := s.app.RunnerHostingStackVolumes(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}
