package api

import (
	"io"
	"nerocd/internal/domain"
	"net/http"
)

func decodeHostingObservation(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, domain.HostingObservationJSONLimit+1))
	if err != nil || domain.DecodeHostingObservationJSON(raw, out) != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid observation JSON")
		return false
	}
	return true
}

func (s *Server) runnerHostingObservationTargets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingObservationTargetsInput
	if !decodeHostingObservation(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	result, err := s.app.RunnerHostingObservationTargets(r.Context(), token, in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) runnerReportHostingObservation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingRuntimeObservation
	if !decodeHostingObservation(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	result, err := s.app.RunnerReportHostingObservation(r.Context(), token, in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) hostingRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := s.app.HostingRuntimeStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
