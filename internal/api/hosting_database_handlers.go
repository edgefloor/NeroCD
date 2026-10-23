package api

import (
	"errors"
	"net/http"
	"strings"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func hostingDatabasePath(path string) bool {
	if path == "/api/v1/databases" {
		return true
	}
	parts := strings.Split(path, "/")
	return len(parts) == 6 && parts[0] == "" && parts[1] == "api" && parts[2] == "v1" && parts[3] == "resources" && parts[4] != "" && parts[5] == "database"
}

func hostingDatabaseResponse(w http.ResponseWriter, status int, view domain.HostingDatabase, err error) {
	if errors.Is(err, store.ErrHostingDatabaseUnavailable) {
		writeErrorEnvelope(w, http.StatusServiceUnavailable, "hosting_database_unavailable", store.ErrHostingDatabaseUnavailable.Error())
		return
	}
	proxyResponse(w, status, view, err)
}

func (s *Server) createHostingDatabase(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "database creation accepts only a request body")
		return
	}
	var in domain.HostingDatabaseCreateInput
	if !decodeBody(w, r, &in) {
		return
	}
	view, err := s.app.CreateHostingDatabase(r.Context(), in)
	hostingDatabaseResponse(w, http.StatusCreated, view, err)
}

func (s *Server) getHostingDatabase(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "database metadata accepts only path parameters")
		return
	}
	view, err := s.app.GetHostingDatabase(r.Context(), r.PathValue("id"))
	hostingDatabaseResponse(w, http.StatusOK, view, err)
}
