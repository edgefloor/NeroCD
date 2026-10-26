package api

import (
	"net/http"

	"nerocd/internal/domain"
)

func (s *Server) hostingCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := s.app.HostingCatalog(r.Context(), r.URL.Query().Get("project_id"))
	proxyResponse(w, http.StatusOK, result, err)
}
func (s *Server) hostingCatalogInstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingCatalogInstallInput
	if decodeJSONDocument(http.MaxBytesReader(w, r.Body, 4096), &in) != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid catalog install request")
		return
	}
	result, err := s.app.InstallHostingCatalog(r.Context(), in)
	proxyResponse(w, http.StatusCreated, result, err)
}
func (s *Server) hostingCatalogInstallation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := s.app.GetHostingCatalogInstallation(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, result, err)
}
