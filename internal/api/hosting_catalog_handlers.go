package api

import (
	"errors"
	"nerocd/internal/app"
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

func (s *Server) hostingCatalogDestinationOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	for key, values := range q {
		if (key != "project_id" && key != "immutable_version" && key != "limit" && key != "offset") || len(values) != 1 || values[0] == "" {
			writeError(w, app.InvalidInput(errors.New("invalid catalog destination options query")))
			return
		}
	}
	if q.Get("project_id") == "" || q.Get("immutable_version") == "" {
		writeError(w, app.InvalidInput(errors.New("project_id and immutable_version are required")))
		return
	}
	result, err := s.app.HostingCatalogDestinationOptions(r.Context(), q.Get("project_id"), r.PathValue("id"), q.Get("immutable_version"), pageFromRequest(r))
	proxyResponse(w, http.StatusOK, result, err)
}
