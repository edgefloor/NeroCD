package api

import (
	"net/http"
	"strings"
)

func hostingStorageDeploymentReadPath(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) < 6 || parts[0] != "" || parts[1] != "api" || parts[2] != "v1" || parts[3] != "resources" || parts[4] == "" {
		return false
	}
	return len(parts) == 6 && parts[5] == "storage-deployment-options" || len(parts) == 7 && parts[5] == "storage-route-continuity" && parts[6] != ""
}

func storageDeploymentReadInput(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "this read accepts only path parameters")
		return false
	}
	return true
}

func (s *Server) hostingStorageDeploymentOptions(w http.ResponseWriter, r *http.Request) {
	if !storageDeploymentReadInput(w, r) {
		return
	}
	view, err := s.app.HostingStorageDeploymentOptions(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, view, err)
}

func (s *Server) hostingStorageRouteContinuityStatus(w http.ResponseWriter, r *http.Request) {
	if !storageDeploymentReadInput(w, r) {
		return
	}
	view, err := s.app.HostingStorageRouteContinuityStatus(r.Context(), r.PathValue("id"), r.PathValue("operation_id"))
	proxyResponse(w, http.StatusOK, view, err)
}
