package api

import (
	"errors"
	"nerocd/internal/app"
	"net/http"
)

func (s *Server) servicesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listServices(w, r)
	case http.MethodPost:
		s.createService(w, r)
	default:
		http.NotFound(w, r)
	}
}
func (s *Server) listServices(w http.ResponseWriter, r *http.Request) {
	v, e := s.app.ListServices(r.Context(), r.URL.Query().Get("project_id"))
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, v))
}
func (s *Server) createService(w http.ResponseWriter, r *http.Request) {
	var q app.ServiceInput
	if !decodeBody(w, r, &q) {
		return
	}
	v, e := s.app.CreateService(r.Context(), q)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}
func (s *Server) environmentsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listEnvironments(w, r)
	case http.MethodPost:
		s.createEnvironment(w, r)
	default:
		http.NotFound(w, r)
	}
}
func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	v, e := s.app.ListEnvironments(r.Context(), r.URL.Query().Get("service_id"))
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, v))
}
func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var q app.EnvironmentInput
	if !decodeBody(w, r, &q) {
		return
	}
	v, e := s.app.CreateEnvironment(r.Context(), q)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}
func (s *Server) revisionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listRevisions(w, r)
	case http.MethodPost:
		s.createRevision(w, r)
	default:
		http.NotFound(w, r)
	}
}
func (s *Server) listRevisions(w http.ResponseWriter, r *http.Request) {
	v, e := s.app.ListRevisions(r.Context(), r.URL.Query().Get("service_id"))
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, v))
}
func (s *Server) createRevision(w http.ResponseWriter, r *http.Request) {
	var q app.RevisionInput
	if !decodeBody(w, r, &q) {
		return
	}
	v, e := s.app.CreateRevision(r.Context(), q)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}
func (s *Server) deploymentsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listDeployments(w, r)
	case http.MethodPost:
		s.createDeployment(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) deploymentByID(w http.ResponseWriter, r *http.Request) {
	deployment, err := s.app.GetDeployment(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deployment)
}
func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	environmentID := r.URL.Query().Get("environment_id")
	if environmentID == "" {
		writeError(w, errors.New("environment_id is required"))
		return
	}
	v, e := s.app.ListDeployments(r.Context(), environmentID)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, v))
}
func (s *Server) createDeployment(w http.ResponseWriter, r *http.Request) {
	var q app.DeploymentInput
	if !decodeBody(w, r, &q) {
		return
	}
	v, e := s.app.CreateDeployment(r.Context(), q)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}
func (s *Server) confirmDeployment(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID string `json:"id"`
	}
	if !decodeBody(w, r, &q) {
		return
	}
	v, e := s.app.ConfirmDeployment(r.Context(), q.ID)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	var q app.DeploymentCancelInput
	if !decodeBody(w, r, &q) {
		return
	}
	v, e := s.app.CancelDeployment(r.Context(), q)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) failPreAssignmentDeployment(w http.ResponseWriter, r *http.Request) {
	var q app.PreAssignmentFailureInput
	if !decodeBody(w, r, &q) {
		return
	}
	v, e := s.app.FailPreAssignmentDeployment(r.Context(), q)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) transitionDeploymentAttempt(w http.ResponseWriter, r *http.Request) {
	var q app.DeploymentTransitionInput
	if !decodeBody(w, r, &q) {
		return
	}
	v, e := s.app.TransitionDeploymentAttempt(r.Context(), q)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
