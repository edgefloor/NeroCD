package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/store"
)

type Server struct {
	app     *app.Service
	logger  *slog.Logger
	mux     *http.ServeMux
	metrics *metrics
}

type PublicRoute struct {
	Method string
	Path   string
}

type metrics struct {
	mu             sync.Mutex
	requests       map[string]int64
	totalLatencyMS int64
}

func newMetrics() *metrics {
	return &metrics{requests: map[string]int64{}}
}

func (m *metrics) record(method string, path string, status int, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := method + "|" + path + "|" + strconv.Itoa(status)
	m.requests[key]++
	m.totalLatencyMS += duration.Milliseconds()
}

func (m *metrics) render() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out strings.Builder
	out.WriteString("# HELP nerocd_http_requests_total Total HTTP requests by method, path, and status.\n")
	out.WriteString("# TYPE nerocd_http_requests_total counter\n")
	for key, count := range m.requests {
		parts := strings.Split(key, "|")
		out.WriteString(`nerocd_http_requests_total{method="`)
		out.WriteString(parts[0])
		out.WriteString(`",path="`)
		out.WriteString(parts[1])
		out.WriteString(`",status="`)
		out.WriteString(parts[2])
		out.WriteString(`"} `)
		out.WriteString(strconv.FormatInt(count, 10))
		out.WriteString("\n")
	}
	out.WriteString("# HELP nerocd_http_request_duration_milliseconds_sum Total request duration in milliseconds.\n")
	out.WriteString("# TYPE nerocd_http_request_duration_milliseconds_sum counter\n")
	out.WriteString("nerocd_http_request_duration_milliseconds_sum ")
	out.WriteString(strconv.FormatInt(m.totalLatencyMS, 10))
	out.WriteString("\n")
	return out.String()
}

var publicRoutes = []PublicRoute{
	{Method: http.MethodGet, Path: "/api/v1/health"},
	{Method: http.MethodGet, Path: "/api/v1/ready"},
	{Method: http.MethodGet, Path: "/api/v1/me"},
	{Method: http.MethodPost, Path: "/api/v1/sessions"},
	{Method: http.MethodDelete, Path: "/api/v1/sessions"},
	{Method: http.MethodPost, Path: "/api/v1/api-tokens"},
	{Method: http.MethodPost, Path: "/api/v1/api-tokens/revoke"},
	{Method: http.MethodGet, Path: "/api/v1/capabilities"},
	{Method: http.MethodGet, Path: "/api/v1/projects"},
	{Method: http.MethodPost, Path: "/api/v1/projects"},
	{Method: http.MethodPatch, Path: "/api/v1/projects"},
	{Method: http.MethodPost, Path: "/api/v1/projects/archive"},
	{Method: http.MethodGet, Path: "/api/v1/project-members"},
	{Method: http.MethodPost, Path: "/api/v1/project-members"},
	{Method: http.MethodGet, Path: "/api/v1/project-role"},
	{Method: http.MethodGet, Path: "/api/v1/repositories"},
	{Method: http.MethodPost, Path: "/api/v1/repositories"},
	{Method: http.MethodGet, Path: "/api/v1/access-keys"},
	{Method: http.MethodPost, Path: "/api/v1/access-keys"},
	{Method: http.MethodGet, Path: "/api/v1/inventories"},
	{Method: http.MethodPost, Path: "/api/v1/inventories"},
	{Method: http.MethodGet, Path: "/api/v1/templates"},
	{Method: http.MethodPost, Path: "/api/v1/templates"},
	{Method: http.MethodPatch, Path: "/api/v1/templates"},
	{Method: http.MethodGet, Path: "/api/v1/runs"},
	{Method: http.MethodPost, Path: "/api/v1/runs"},
	{Method: http.MethodPost, Path: "/api/v1/runs/approve"},
	{Method: http.MethodPost, Path: "/api/v1/runs/reject"},
	{Method: http.MethodPost, Path: "/api/v1/runs/cancel"},
	{Method: http.MethodGet, Path: "/api/v1/runners"},
	{Method: http.MethodPost, Path: "/api/v1/runners/register"},
	{Method: http.MethodPost, Path: "/api/v1/runner-enrollments"},
	{Method: http.MethodPost, Path: "/api/v1/runner-enrollments/revoke"},
	{Method: http.MethodPost, Path: "/api/v1/runner-enrollments/consume"},
	{Method: http.MethodPost, Path: "/api/v1/runners/rotate-token"},
	{Method: http.MethodPost, Path: "/api/v1/runners/revoke-token"},
	{Method: http.MethodPost, Path: "/api/v1/runners/heartbeat"},
	{Method: http.MethodPost, Path: "/api/v1/runners/claim"},
	{Method: http.MethodPost, Path: "/api/v1/runners/renew"},
	{Method: http.MethodGet, Path: "/api/v1/runners/lease"},
	{Method: http.MethodPost, Path: "/api/v1/runners/logs"},
	{Method: http.MethodPost, Path: "/api/v1/runners/events/batch"},
	{Method: http.MethodPost, Path: "/api/v1/runners/secrets/access"},
	{Method: http.MethodPost, Path: "/api/v1/runners/artifacts"},
	{Method: http.MethodPost, Path: "/api/v1/runners/complete"},
	{Method: http.MethodGet, Path: "/api/v1/run-logs"},
	{Method: http.MethodGet, Path: "/api/v1/artifacts"},
	{Method: http.MethodGet, Path: "/api/v1/runner-primitive-plan"},
	{Method: http.MethodGet, Path: "/api/v1/approvals"},
	{Method: http.MethodGet, Path: "/api/v1/audit-events"},
}

func PublicRoutes() []PublicRoute {
	routes := make([]PublicRoute, len(publicRoutes))
	copy(routes, publicRoutes)
	return routes
}

func NewServer(appService *app.Service, logger *slog.Logger, static fs.FS) *Server {
	s := &Server{app: appService, logger: logger, mux: http.NewServeMux(), metrics: newMetrics()}
	s.routes(static)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := requestID(r)
	r = r.WithContext(app.WithRequestID(r.Context(), requestID))
	w.Header().Set("X-Request-ID", requestID)
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.mux.ServeHTTP(rec, r)
	s.metrics.record(r.Method, r.URL.Path, rec.status, time.Since(start))
	s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration_ms", time.Since(start).Milliseconds(), "request_id", requestID)
}

func (s *Server) routes(static fs.FS) {
	for _, route := range publicRoutes {
		handler := http.HandlerFunc(s.handlerFor(route.Path))
		if requiresRunnerAuth(route.Path) {
			handler = s.authenticateRunner(handler)
		} else if requiresAuth(route.Path) {
			handler = s.authenticate(handler)
		}
		s.mux.Handle(route.Method+" "+route.Path, handler)
	}
	s.mux.Handle(http.MethodGet+" /metrics", http.HandlerFunc(s.metricsHandler))
	s.mux.Handle("/", spaFileServer(static))
}

func requiresAuth(path string) bool {
	switch path {
	case "/api/v1/health", "/api/v1/ready", "/api/v1/sessions", "/api/v1/runner-enrollments/consume":
		return false
	default:
		return true
	}
}

func requiresRunnerAuth(path string) bool {
	switch path {
	case "/api/v1/runners/heartbeat", "/api/v1/runners/claim", "/api/v1/runners/renew", "/api/v1/runners/lease", "/api/v1/runners/logs", "/api/v1/runners/events/batch", "/api/v1/runners/secrets/access", "/api/v1/runners/artifacts", "/api/v1/runners/complete":
		return true
	default:
		return false
	}
}

func (s *Server) authenticate(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, auth.ErrUnauthenticated)
			return
		}
		principal, err := s.app.AuthenticateSessionToken(r.Context(), token)
		if err != nil {
			writeError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	}
}

func (s *Server) authenticateRunner(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, auth.ErrUnauthenticated)
			return
		}
		principal, err := s.app.AuthenticateRunnerToken(r.Context(), token)
		if err != nil {
			writeError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	}
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

func (s *Server) handlerFor(path string) http.HandlerFunc {
	switch path {
	case "/api/v1/health":
		return s.health
	case "/api/v1/ready":
		return s.ready
	case "/api/v1/me":
		return s.me
	case "/api/v1/sessions":
		return s.sessionsHandler
	case "/api/v1/api-tokens":
		return s.createAPIToken
	case "/api/v1/api-tokens/revoke":
		return s.revokeAPIToken
	case "/api/v1/capabilities":
		return s.capabilities
	case "/api/v1/projects":
		return s.projectsHandler
	case "/api/v1/projects/archive":
		return s.archiveProject
	case "/api/v1/project-members":
		return s.projectMembersHandler
	case "/api/v1/project-role":
		return s.projectRole
	case "/api/v1/repositories":
		return s.repositoriesHandler
	case "/api/v1/access-keys":
		return s.accessKeysHandler
	case "/api/v1/inventories":
		return s.inventoriesHandler
	case "/api/v1/templates":
		return s.templatesHandler
	case "/api/v1/runs":
		return s.runsHandler
	case "/api/v1/runs/approve":
		return s.approveRun
	case "/api/v1/runs/reject":
		return s.rejectRun
	case "/api/v1/runs/cancel":
		return s.cancelRun
	case "/api/v1/runners":
		return s.runnersHandler
	case "/api/v1/runners/register":
		return s.registerRunner
	case "/api/v1/runner-enrollments":
		return s.createRunnerEnrollment
	case "/api/v1/runner-enrollments/revoke":
		return s.revokeRunnerEnrollment
	case "/api/v1/runner-enrollments/consume":
		return s.consumeRunnerEnrollment
	case "/api/v1/runners/rotate-token":
		return s.rotateRunnerToken
	case "/api/v1/runners/revoke-token":
		return s.revokeRunnerToken
	case "/api/v1/runners/heartbeat":
		return s.heartbeatRunner
	case "/api/v1/runners/claim":
		return s.claimRun
	case "/api/v1/runners/renew":
		return s.renewLease
	case "/api/v1/runners/lease":
		return s.runnerLease
	case "/api/v1/runners/logs":
		return s.appendRunLog
	case "/api/v1/runners/events/batch":
		return s.appendRunEvents
	case "/api/v1/runners/secrets/access":
		return s.authorizeSecretAccess
	case "/api/v1/runners/artifacts":
		return s.createArtifact
	case "/api/v1/runners/complete":
		return s.completeLease
	case "/api/v1/run-logs":
		return s.runLogs
	case "/api/v1/artifacts":
		return s.artifacts
	case "/api/v1/runner-primitive-plan":
		return s.runnerPrimitivePlan
	case "/api/v1/approvals":
		return s.approvalsHandler
	case "/api/v1/audit-events":
		return s.auditEvents
	default:
		panic("missing handler for public route " + path)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Ready(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(s.metrics.render()))
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	principal, err := s.app.CurrentPrincipal(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, principal)
}

func (s *Server) sessionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createSession(w, r)
	case http.MethodDelete:
		s.revokeSession(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	session, err := s.app.CreateSession(r.Context(), req.Email, req.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, auth.ErrUnauthenticated)
		return
	}
	if err := s.app.RevokeSessionToken(r.Context(), token); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request) {
	var req app.APITokenInput
	if !decodeBody(w, r, &req) {
		return
	}
	token, err := s.app.CreateAPIToken(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, token)
}

func (s *Server) revokeAPIToken(w http.ResponseWriter, r *http.Request) {
	var req app.RevokeAPITokenInput
	if !decodeBody(w, r, &req) {
		return
	}
	token, err := s.app.RevokeAPIToken(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, token)
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, paginated(r, s.app.Capabilities()))
}

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

func (s *Server) runsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.runs(w, r)
	case http.MethodPost:
		s.requestRun(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	runs, err := s.app.ListRunsPage(r.Context(), r.URL.Query().Get("project_id"), pageFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginatedResult(runs))
}

func (s *Server) requestRun(w http.ResponseWriter, r *http.Request) {
	var req app.RunRequestInput
	if !decodeBody(w, r, &req) {
		return
	}
	run, err := s.app.RequestRunWithSpec(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) approveRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	approval, err := s.app.ApproveRun(r.Context(), req.RunID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

func (s *Server) rejectRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	approval, err := s.app.RejectRun(r.Context(), req.RunID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	run, err := s.app.CancelRun(r.Context(), req.RunID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) runnersHandler(w http.ResponseWriter, r *http.Request) {
	runners, err := s.app.ListRunners(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, runners))
}

func (s *Server) registerRunner(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerInput
	if !decodeBody(w, r, &req) {
		return
	}
	registered, err := s.app.RegisterRunner(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, registered)
}

func (s *Server) createRunnerEnrollment(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerEnrollmentInput
	if !decodeBody(w, r, &req) {
		return
	}
	created, err := s.app.CreateRunnerEnrollment(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) revokeRunnerEnrollment(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerEnrollmentRevokeInput
	if !decodeBody(w, r, &req) {
		return
	}
	revoked, err := s.app.RevokeRunnerEnrollment(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, revoked)
}

func (s *Server) consumeRunnerEnrollment(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, auth.ErrUnauthenticated)
		return
	}
	var req app.RunnerEnrollmentConsumeInput
	if !decodeBody(w, r, &req) {
		return
	}
	consumed, err := s.app.ConsumeRunnerEnrollment(r.Context(), token, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, consumed)
}

func (s *Server) rotateRunnerToken(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerTokenInput
	if !decodeBody(w, r, &req) {
		return
	}
	rotated, err := s.app.RotateRunnerToken(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rotated)
}

func (s *Server) revokeRunnerToken(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerTokenInput
	if !decodeBody(w, r, &req) {
		return
	}
	runner, err := s.app.RevokeRunnerToken(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runner)
}

func (s *Server) heartbeatRunner(w http.ResponseWriter, r *http.Request) {
	runner, err := s.app.HeartbeatRunner(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runner)
}

func (s *Server) claimRun(w http.ResponseWriter, r *http.Request) {
	claim, err := s.app.ClaimRun(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, claim)
}

func (s *Server) renewLease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LeaseID string `json:"lease_id"`
		Fence   string `json:"fence"`
		Attempt int    `json:"attempt"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	lease, err := s.app.RenewLease(r.Context(), req.LeaseID, req.Fence, req.Attempt)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) runnerLease(w http.ResponseWriter, r *http.Request) {
	attempt, err := strconv.Atoi(r.URL.Query().Get("attempt"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "attempt is required"})
		return
	}
	lease, err := s.app.RunnerLease(r.Context(), r.URL.Query().Get("lease_id"), attempt, r.URL.Query().Get("fence"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) appendRunLog(w http.ResponseWriter, r *http.Request) {
	var req app.RunLogInput
	if !decodeBody(w, r, &req) {
		return
	}
	log, err := s.app.AppendRunLog(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, log)
}

func (s *Server) appendRunEvents(w http.ResponseWriter, r *http.Request) {
	var req app.RunEventBatchInput
	if !decodeBody(w, r, &req) {
		return
	}
	ack, err := s.app.AppendRunEvents(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ack)
}

func (s *Server) authorizeSecretAccess(w http.ResponseWriter, r *http.Request) {
	var req app.SecretAccessInput
	if !decodeBody(w, r, &req) {
		return
	}
	grant, err := s.app.AuthorizeSecretAccess(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (s *Server) createArtifact(w http.ResponseWriter, r *http.Request) {
	var req app.ArtifactInput
	if !decodeBody(w, r, &req) {
		return
	}
	artifact, err := s.app.CreateArtifact(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, artifact)
}

func (s *Server) completeLease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LeaseID       string `json:"lease_id"`
		Status        string `json:"status"`
		Fence         string `json:"fence"`
		Attempt       int    `json:"attempt"`
		CompletionKey string `json:"completion_key"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Attempt <= 0 || req.Fence == "" || strings.TrimSpace(req.CompletionKey) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "attempt, fence, and completion_key are required"})
		return
	}
	lease, err := s.app.CompleteLease(r.Context(), req.LeaseID, req.Status, req.Attempt, req.Fence, req.CompletionKey)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) runLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.app.ListRunLogsPage(r.Context(), r.URL.Query().Get("run_id"), pageFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginatedResult(logs))
}

func (s *Server) artifacts(w http.ResponseWriter, r *http.Request) {
	artifacts, err := s.app.ListArtifactsPage(r.Context(), r.URL.Query().Get("run_id"), pageFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginatedResult(artifacts))
}

func (s *Server) runnerPrimitivePlan(w http.ResponseWriter, r *http.Request) {
	plan, err := s.app.RunnerPrimitivePlan(r.Context(), r.URL.Query().Get("run_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) approvalsHandler(w http.ResponseWriter, r *http.Request) {
	approvals, err := s.app.ListApprovals(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, approvals))
}

func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.app.ListAuditEventsPage(r.Context(), pageFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginatedResult(events))
}

func paginated[T any](r *http.Request, items []T) map[string]any {
	total := len(items)
	limit := parseNonNegativeInt(r.URL.Query().Get("limit"), total)
	offset := parseNonNegativeInt(r.URL.Query().Get("offset"), 0)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	if limit == 0 {
		end = offset
	}
	page := items[offset:end]
	return map[string]any{"items": page, "limit": limit, "offset": offset, "count": len(page), "total": total}
}

func pageFromRequest(r *http.Request) store.Page {
	return store.Page{
		Limit:   parseNonNegativeInt(r.URL.Query().Get("limit"), 0),
		Offset:  parseNonNegativeInt(r.URL.Query().Get("offset"), 0),
		Enabled: r.URL.Query().Has("limit") || r.URL.Query().Has("offset"),
	}
}

func paginatedResult[T any](result store.PageResult[T]) map[string]any {
	items := result.Items
	if items == nil {
		items = []T{}
	}
	return map[string]any{"items": items, "limit": result.Limit, "offset": result.Offset, "count": len(items), "total": result.Total}
}

func parseNonNegativeInt(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func decodeBody(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

func requestID(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); value != "" {
		return value
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err == nil {
		return "req_" + hex.EncodeToString(buf)
	}
	return "req_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		status = http.StatusUnauthorized
		code = "unauthenticated"
	case errors.Is(err, auth.ErrForbidden):
		status = http.StatusForbidden
		code = "forbidden"
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
		code = "not_found"
	case errors.Is(err, store.ErrConflict):
		status = http.StatusConflict
		code = "conflict"
	case strings.Contains(err.Error(), "required"), strings.Contains(err.Error(), "invalid"):
		status = http.StatusBadRequest
		code = "bad_request"
	}
	writeErrorEnvelope(w, status, code, err.Error())
}

func writeErrorEnvelope(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]string{"code": code, "error": message})
}

func spaFileServer(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(static, path); err != nil {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
