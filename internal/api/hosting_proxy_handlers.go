package api

import (
	"net/http"

	"nerocd/internal/domain"
)

func (s *Server) hostingRetainedReleases(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.ListHostingRetainedReleases(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if result == nil {
		result = []domain.HostingRetainedRelease{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) hostingReleaseCleanup(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingCleanupInput
	if !decodeBody(w, r, &in) {
		return
	}
	op, err := s.app.RequestHostingReleaseCleanup(r.Context(), r.PathValue("id"), r.PathValue("operation_id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}
