package api

import (
	"net/http"

	"nerocd/internal/app"
)

func (s *Server) runsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.runs(w, r)
	case http.MethodPost:
		s.requestRun(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	runs, err := s.app.ListRunsPage(r.Context(), r.URL.Query().Get("project_id"), pageFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginatedResult(runs))
}

func (s *Server) requestRun(w http.ResponseWriter, r *http.Request) {
	var req app.RunRequestInput
	if !decodeBody(w, r, &req) {
		return
	}
	run, err := s.app.RequestRunWithSpec(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) approveRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	approval, err := s.app.ApproveRun(r.Context(), req.RunID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

func (s *Server) rejectRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	approval, err := s.app.RejectRun(r.Context(), req.RunID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	run, err := s.app.CancelRun(r.Context(), req.RunID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
