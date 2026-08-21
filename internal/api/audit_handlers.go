package api

import "net/http"

func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.app.ListAuditEventsPage(r.Context(), pageFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginatedResult(events))
}
