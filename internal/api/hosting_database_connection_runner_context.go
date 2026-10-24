package api

import "net/http"

// Bind the original authenticated HTTP credential before any repository wait.
// The service retains only its fingerprint in a private request context value.
func (s *Server) databaseConnectionRunnerRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	token, _ := bearerToken(r.Header.Get("Authorization"))
	ctx, err := s.app.HostingDatabaseConnectionRunnerContext(r.Context(), token)
	if err != nil {
		writeError(w, err)
		return r, false
	}
	return r.WithContext(ctx), true
}
