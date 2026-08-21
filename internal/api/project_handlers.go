package api

import (
	"bytes"
	"encoding/json"
	"nerocd/internal/app"
	"nerocd/internal/domain"
	"net/http"
)

func (s *Server) projectsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.projects(w, r)
	case http.MethodPost:
		s.createProject(w, r)
	case http.MethodPatch:
		s.updateProject(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.app.ListProjects(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, projects))
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req app.ProjectInput
	if !decodeBody(w, r, &req) {
		return
	}
	project, err := s.app.CreateProject(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
		app.ProjectInput
	}
	if !decodeBody(w, r, &req) {
		return
	}
	project, err := s.app.UpdateProject(r.Context(), req.ID, req.ProjectInput)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) archiveProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	project, err := s.app.ArchiveProject(r.Context(), req.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) projectMembersHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.projectMembers(w, r)
	case http.MethodPost:
		s.upsertProjectMember(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) projectMembers(w http.ResponseWriter, r *http.Request) {
	members, err := s.app.ListProjectMembers(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, members))
}

func (s *Server) upsertProjectMember(w http.ResponseWriter, r *http.Request) {
	var req app.ProjectMemberInput
	if !decodeBody(w, r, &req) {
		return
	}
	member, err := s.app.UpsertProjectMember(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, member)
}

func (s *Server) projectRole(w http.ResponseWriter, r *http.Request) {
	role, err := s.app.ProjectRole(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, role)
}

func (s *Server) repositoriesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.repositories(w, r)
	case http.MethodPost:
		s.createRepository(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) repositories(w http.ResponseWriter, r *http.Request) {
	repositories, err := s.app.ListRepositories(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, repositories))
}

func (s *Server) createRepository(w http.ResponseWriter, r *http.Request) {
	var req app.RepositoryInput
	if !decodeBody(w, r, &req) {
		return
	}
	repository, err := s.app.CreateRepository(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, repository)
}
func (s *Server) repositoryPolicyHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		ProjectID       string          `json:"project_id"`
		ConfigurationID string          `json:"configuration_id"`
		Policy          json.RawMessage `json:"policy"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	var policy domain.RepositoryPolicy
	if len(req.Policy) == 0 || decodeJSONDocument(bytes.NewReader(req.Policy), &policy) != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid repository policy")
		return
	}
	v, err := s.app.ConfigureRepositoryPolicy(r.Context(), app.RepositoryPolicyInput{ID: id, ProjectID: req.ProjectID, ConfigurationID: req.ConfigurationID, Policy: policy})
	if err != nil {
		writeError(w, err)
		return
	}
	// Credential references are useful to the server and runner but are never
	// disclosed by this configuration endpoint (or its audit metadata).
	v.Policy.CredentialReferenceID = ""
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) accessKeysHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.accessKeys(w, r)
	case http.MethodPost:
		s.createAccessKey(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) accessKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.app.ListAccessKeys(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, keys))
}

func (s *Server) createAccessKey(w http.ResponseWriter, r *http.Request) {
	var req app.AccessKeyInput
	if !decodeBody(w, r, &req) {
		return
	}
	key, err := s.app.CreateAccessKey(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

func (s *Server) inventoriesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.inventories(w, r)
	case http.MethodPost:
		s.createInventory(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) inventories(w http.ResponseWriter, r *http.Request) {
	inventories, err := s.app.ListInventories(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, inventories))
}

func (s *Server) createInventory(w http.ResponseWriter, r *http.Request) {
	var req app.InventoryInput
	if !decodeBody(w, r, &req) {
		return
	}
	inventory, err := s.app.CreateInventory(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, inventory)
}
