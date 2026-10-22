package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

func (s *Server) projectEnvironmentsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := s.app.ListProjectEnvironments(r.Context(), r.URL.Query().Get("project_id"), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(result))
		return
	}
	var in app.ProjectEnvironmentInput
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.CreateProjectEnvironment(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
func (s *Server) resourcesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := s.app.ListResources(r.Context(), r.URL.Query().Get("project_id"), r.URL.Query().Get("project_environment_id"), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(result))
		return
	}
	var req struct {
		ProjectID            string                     `json:"project_id"`
		ProjectEnvironmentID string                     `json:"project_environment_id"`
		Name                 string                     `json:"name"`
		Kind                 string                     `json:"kind"`
		Source               domain.ResourceSourceInput `json:"source"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	in := app.ResourceInput{ProjectID: req.ProjectID, ProjectEnvironmentID: req.ProjectEnvironmentID, Name: req.Name, Kind: req.Kind, Source: req.Source.Source()}
	result, err := s.app.CreateResource(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
func (s *Server) resourceByID(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := s.app.GetResource(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	// Raw fields distinguish omission from explicit null, which is invalid
	// for the mutable string/object fields in the public patch contract.
	var req struct {
		Name               json.RawMessage `json:"name"`
		Source             json.RawMessage `json:"source"`
		ExpectedGeneration int64           `json:"expected_generation"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	in := app.ResourcePatch{ExpectedGeneration: req.ExpectedGeneration}
	if len(req.Name) > 0 {
		var name string
		if err := decodeJSONDocument(bytes.NewReader(req.Name), &name); err != nil {
			writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid name")
			return
		}
		in.Name = &name
	}
	if len(req.Source) > 0 {
		var source domain.ResourceSourceInput
		if err := decodeJSONDocument(bytes.NewReader(req.Source), &source); err != nil {
			writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid source")
			return
		}
		value := source.Source()
		in.Source = &value
	}
	result, err := s.app.UpdateResource(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
