package api

import (
	"errors"
	"io"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"net/http"
	"strconv"
)

func decodeRuntimeLogs(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, domain.HostingRuntimeLogByteLimit+1))
	if err != nil || domain.DecodeHostingRuntimeLogsJSON(raw, out) != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid runtime log JSON")
		return false
	}
	return true
}

func writeRuntimeLogError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrRuntimeLogsExpired) {
		writeErrorEnvelope(w, http.StatusGone, "runtime_logs_expired", "runtime log session expired")
		return
	}
	writeError(w, err)
}

func (s *Server) runtimeLogOpen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingRuntimeLogOpen
	if !decodeRuntimeLogs(w, r, &in) {
		return
	}
	result, err := s.app.OpenRuntimeLogSession(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeRuntimeLogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) runtimeLogRenew(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !decodeRuntimeLogs(w, r, &struct{}{}) {
		return
	}
	result, err := s.app.RenewRuntimeLogSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRuntimeLogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) runtimeLogClose(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.ContentLength != 0 {
		if !decodeRuntimeLogs(w, r, &struct{}{}) {
			return
		}
	}
	result, err := s.app.CloseRuntimeLogSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRuntimeLogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) runtimeLogPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	for key, values := range query {
		if key != "after_sequence" && key != "limit" || len(values) != 1 {
			writeErrorEnvelope(w, 400, "invalid_request", "invalid runtime log pagination")
			return
		}
	}
	after, limit := 0, 200
	var err error
	if value := query.Get("after_sequence"); value != "" {
		after, err = strconv.Atoi(value)
		if err != nil {
			writeErrorEnvelope(w, 400, "invalid_request", "invalid runtime log pagination")
			return
		}
	}
	if value := query.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil {
			writeErrorEnvelope(w, 400, "invalid_request", "invalid runtime log pagination")
			return
		}
	}
	result, err := s.app.RuntimeLogPage(r.Context(), r.PathValue("id"), after, limit)
	if err != nil {
		writeRuntimeLogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) runtimeLogClaim(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingRuntimeLogClaimInput
	if !decodeRuntimeLogs(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	result, err := s.app.ClaimRuntimeLogSessions(r.Context(), token, in)
	if err != nil {
		writeRuntimeLogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) runtimeLogEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingRuntimeLogBatch
	if !decodeRuntimeLogs(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	result, err := s.app.AppendRuntimeLogs(r.Context(), token, in)
	if err != nil {
		writeRuntimeLogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
