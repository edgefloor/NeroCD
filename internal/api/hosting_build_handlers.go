package api

import (
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"net/http"
)

func (s *Server) hostingBuilds(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := s.app.ListHostingBuilds(r.Context(), r.PathValue("id"), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(result))
		return
	}
	var in domain.HostingBuildRequest
	if !decodeBody(w, r, &in) {
		return
	}
	b, err := s.app.RequestHostingBuild(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"build": b})
}
func (s *Server) hostingBuild(w http.ResponseWriter, r *http.Request) {
	b, err := s.app.HostingBuild(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"build": b})
}
func (s *Server) hostingBuildCancel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	b, err := s.app.CancelHostingBuild(r.Context(), r.PathValue("id"), in.ExpectedRevision)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"build": b})
}
func (s *Server) runnerBuildStatus(w http.ResponseWriter, r *http.Request) {
	var in hostingbuildprotocol.StatusRequest
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.RunnerHostingBuildStatus(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) runnerBuildSourceBegin(w http.ResponseWriter, r *http.Request) {
	var in hostingbuildprotocol.SourceBeginRequest
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.RunnerHostingBuildSourceBegin(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) runnerBuildSourceCommit(w http.ResponseWriter, r *http.Request) {
	var in hostingbuildprotocol.SourceCommitRequest
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.RunnerHostingBuildSourceCommit(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) runnerBuildPrepare(w http.ResponseWriter, r *http.Request) {
	var in hostingbuildprotocol.PrepareRequest
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.RunnerHostingBuildPrepare(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) runnerBuildComplete(w http.ResponseWriter, r *http.Request) {
	var in hostingbuildprotocol.CompleteRequest
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.RunnerHostingBuildComplete(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
