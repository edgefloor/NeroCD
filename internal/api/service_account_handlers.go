package api

import (
	"net/http"

	"nerocd/internal/app"
)

func (s *Server) serviceAccountsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		result, err := s.app.ListServiceAccounts(r.Context(), r.URL.Query().Get("project_id"), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(result))
	case http.MethodPost:
		var input app.ServiceAccountCreateInput
		if !decodeBody(w, r, &input) {
			return
		}
		result, err := s.app.CreateServiceAccount(r.Context(), input)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	case http.MethodPatch:
		var input app.ServiceAccountUpdateInput
		if !decodeBody(w, r, &input) {
			return
		}
		result, err := s.app.UpdateServiceAccount(r.Context(), input)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) rotateServiceAccountCredential(w http.ResponseWriter, r *http.Request) {
	var input app.ServiceAccountRotateInput
	if !decodeBody(w, r, &input) {
		return
	}
	result, err := s.app.RotateServiceAccountCredential(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) revokeServiceAccount(w http.ResponseWriter, r *http.Request) {
	var input app.ServiceAccountRevokeInput
	if !decodeBody(w, r, &input) {
		return
	}
	result, err := s.app.RevokeServiceAccount(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
