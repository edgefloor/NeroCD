package api

import (
	"net/http"
	"strings"

	"nerocd/internal/domain"
)

func hostingStorageRouteRecoveryPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) == 8 && parts[0] == "" && parts[1] == "api" && parts[2] == "v1" && parts[3] == "resources" && parts[4] != "" && parts[5] == "storage-route-continuity" && parts[6] != "" && parts[7] == "recover"
}

func (s *Server) recoverHostingStorageRouteContinuity(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "recovery accepts only path parameters and evidence fields")
		return
	}
	var in domain.HostingStorageRouteRecoveryInput
	if !decodeBody(w, r, &in) {
		return
	}
	view, err := s.app.RecoverHostingStorageRouteContinuity(r.Context(), r.PathValue("id"), r.PathValue("operation_id"), in)
	proxyResponse(w, http.StatusOK, view, err)
}
