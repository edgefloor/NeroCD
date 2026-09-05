package api

import (
	"net/http"

	"nerocd/internal/app"
)

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.ListUsersPage(r.Context(), pageFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginatedResult(result))
}

func (s *Server) offboardUser(w http.ResponseWriter, r *http.Request) {
	var input app.OffboardUserInput
	if !decodeBody(w, r, &input) {
		return
	}
	result, err := s.app.OffboardUser(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
