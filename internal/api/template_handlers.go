package api

import (
	"net/http"

	"nerocd/internal/app"
)

func (s *Server) templatesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.templates(w, r)
	case http.MethodPost:
		s.createTemplate(w, r)
	case http.MethodPatch:
		s.updateTemplate(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) templates(w http.ResponseWriter, r *http.Request) {
	templates, err := s.app.ListTemplates(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, templates))
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var req app.TemplateInput
	if !decodeBody(w, r, &req) {
		return
	}
	template, err := s.app.CreateTemplate(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, template)
}

func (s *Server) updateTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
		app.TemplateInput
	}
	if !decodeBody(w, r, &req) {
		return
	}
	template, err := s.app.UpdateTemplate(r.Context(), req.ID, req.TemplateInput)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, template)
}
