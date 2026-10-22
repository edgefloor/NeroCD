package api

import (
	"nerocd/internal/domain"
	"net/http"
)

func (s *Server) hostingBuildDeploymentRequest(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingBuildDeploymentInput
	if !decodeBody(w, r, &in) {
		return
	}
	op, err := s.app.RequestHostingBuildDeployment(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}
