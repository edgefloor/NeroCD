package api

import (
	"errors"
	"net/http"

	"nerocd/internal/app"
)

func (s *Server) hostingServers(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := s.app.ListServers(r.Context(), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(result))
		return
	}
	var in app.ServerInput
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.CreateServer(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
func (s *Server) hostingServerByID(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) hostingDestinations(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Has("project_id") == query.Has("server_id") || len(query["project_id"]) > 1 || len(query["server_id"]) > 1 {
		writeError(w, app.InvalidInput(errors.New("exactly one project_id or server_id value is required")))
		return
	}
	result, err := s.app.ListDestinations(r.Context(), query.Get("project_id"), query.Get("server_id"), pageFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginatedResult(result))
}
func (s *Server) destinationProjectGrants(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := s.app.ListDestinationProjectGrants(r.Context(), r.PathValue("id"), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(result))
		return
	}
	var in app.DestinationProjectInput
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.GrantDestinationProject(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
func (s *Server) revokeDestinationProjectGrant(w http.ResponseWriter, r *http.Request) {
	var in app.DestinationProjectInput
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.RevokeDestinationProject(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) resourceBinding(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := s.app.GetResourceBinding(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	var in app.ResourceBindingInput
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.BindResourceDestination(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
