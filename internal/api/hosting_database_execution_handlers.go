package api

import (
	"errors"
	"net/http"
	"strings"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func hostingDatabaseDeploymentPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) == 7 && parts[0] == "" && parts[1] == "api" && parts[2] == "v1" && parts[3] == "resources" && parts[4] != "" && parts[5] == "database" && parts[6] == "deployments"
}

func (s *Server) hostingDatabaseDeploymentRequest(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "database deployment accepts only a request body")
		return
	}
	var in domain.HostingDatabaseDeploymentInput
	if !decodeBody(w, r, &in) {
		return
	}
	op, err := s.app.RequestHostingDatabaseDeployment(r.Context(), r.PathValue("id"), in)
	if errors.Is(err, store.ErrHostingDatabaseUnavailable) {
		writeErrorEnvelope(w, http.StatusServiceUnavailable, "hosting_database_unavailable", store.ErrHostingDatabaseUnavailable.Error())
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}
