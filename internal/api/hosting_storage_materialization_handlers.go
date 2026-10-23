package api

import (
	"io"
	"net/http"

	"nerocd/internal/domain"
)

func decodeHostingStorageProtocol(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, domain.HostingStorageProtocolJSONLimit+1))
	if len(raw) > domain.HostingStorageProtocolJSONLimit {
		writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, "request_too_large", "storage document exceeds limit")
		return false
	}
	if err != nil || domain.DecodeHostingStorageProtocolJSON(raw, out) != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid storage JSON")
		return false
	}
	return true
}

func (s *Server) runnerHostingStorageCreateBegin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingStorageCreateRequest
	if !decodeHostingStorageProtocol(w, r, &in) {
		return
	}
	out, err := s.app.RunnerHostingStorageCreateBegin(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) runnerHostingStorageMaterialized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingStorageMaterializedRequest
	if !decodeHostingStorageProtocol(w, r, &in) {
		return
	}
	out, err := s.app.RunnerHostingStorageMaterialized(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
