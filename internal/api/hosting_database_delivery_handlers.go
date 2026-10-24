package api

import (
	"mime"
	"net/http"

	"nerocd/internal/app"
	"nerocd/internal/hostingdatabaseprotocol"
)

func (s *Server) runnerHostingDatabaseCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	content, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	in, err := hostingdatabaseprotocol.ReadRequest(r.Body)
	if err != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if !s.runtimeEnvironmentTransport(r) {
		writeError(w, app.ErrHostingDatabaseCredentialsUnavailable)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, app.ErrHostingDatabaseCredentialsUnavailable)
		return
	}
	out, err := s.app.RunnerHostingDatabaseCredentials(r.Context(), in, token)
	if err != nil {
		writeError(w, err)
		return
	}
	defer out.Close()
	w.Header().Set("Content-Type", "application/json")
	if hostingdatabaseprotocol.WriteCredentials(w, out) != nil {
		s.logger.Error("database credential response failed")
	}
}
