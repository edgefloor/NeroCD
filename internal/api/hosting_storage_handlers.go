package api

import (
	"io"
	"net/http"
	"strings"

	"nerocd/internal/domain"
)

func hostingStoragePath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) == 6 && parts[0] == "" && parts[1] == "api" && parts[2] == "v1" && parts[3] == "resources" && parts[4] != "" && parts[5] == "storage"
}

func (s *Server) hostingStorage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		result, err := s.app.GetHostingStorage(r.Context(), r.PathValue("id"))
		proxyResponse(w, http.StatusOK, result, err)
		return
	}
	// The domain decoder rejects duplicates, unknown fields and server-owned fields.
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if len(raw) > 4096 {
		writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, "invalid_request", "storage request exceeds limit")
		return
	}
	var in domain.HostingStorageInput
	if err != nil || decodeJSONDocument(strings.NewReader(string(raw)), &in) != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid storage request")
		return
	}
	result, err := s.app.CreateHostingStorage(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusCreated, result, err)
}
