package api

import (
	"net/http"

	"nerocd/internal/domain"
)

func (s *Server) runnerApplicationDatabaseStorageWriter(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingApplicationDatabaseStorageWriterRequest
	if !decodeDatabaseConnection(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerHostingApplicationDatabaseStorageWriter(r.Context(), token, in)
	databaseConnectionResponse(w, http.StatusOK, out, err)
}
func (s *Server) runnerApplicationDatabaseStorageAuthorize(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingApplicationDatabaseStorageEffectRequest
	if !decodeDatabaseConnection(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerHostingApplicationDatabaseStorageAuthorize(r.Context(), token, in)
	databaseConnectionResponse(w, http.StatusOK, out, err)
}
func (s *Server) runnerApplicationDatabaseStorageProxyPlan(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingAuthority
	if !decodeDatabaseConnectionAuthority(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerHostingApplicationDatabaseStorageProxyPlan(r.Context(), token, in)
	databaseConnectionResponse(w, http.StatusOK, out, err)
}
