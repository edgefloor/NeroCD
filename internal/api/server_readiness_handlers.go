package api

import (
	"io"
	"net/http"

	"nerocd/internal/domain"
)

func decodeServerReadiness(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, domain.ServerReadinessJSONLimit+1))
	if err != nil || domain.DecodeServerReadinessJSON(raw, out) != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid readiness JSON")
		return false
	}
	return true
}

func (s *Server) runnerServerReadinessChallenge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct{}
	if !decodeServerReadiness(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerServerReadinessChallenge(r.Context(), token)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) runnerReportServerReadiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.ServerReadinessReport
	if !decodeServerReadiness(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerReportServerReadiness(r.Context(), token, in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) serverReadiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	out, err := s.app.ServerReadiness(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
