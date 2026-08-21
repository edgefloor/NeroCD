package api

import "net/http"

func (s *Server) approvalsHandler(w http.ResponseWriter, r *http.Request) {
	approvals, err := s.app.ListApprovals(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, approvals))
}
