package api

import "net/http"

func (s *Server) runnerIdentity(w http.ResponseWriter, r *http.Request) {
	identity, err := s.app.CurrentRunnerIdentity(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, identity)
}
