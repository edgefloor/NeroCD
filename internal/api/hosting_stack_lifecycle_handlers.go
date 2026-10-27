package api

import (
	"nerocd/internal/domain"
	"net/http"
)

func (s *Server) hostingStackLifecycle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	out, err := s.app.HostingStackLifecycle(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) hostingStackStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingStackStopInput
	if !decodeBody(w, r, &in) {
		return
	}
	out, err := s.app.RequestHostingStackStop(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}
