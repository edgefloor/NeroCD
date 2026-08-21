package api

import (
	"errors"
	"net/http"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/observability"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if err := s.app.Ready(r.Context()); err != nil {
		// Readiness is intentionally a binary, non-disclosing operator signal;
		// schema/connection detail belongs in authenticated logs and metrics.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.app.PublicBootstrapStatus(r.Context())
	if err != nil {
		// This public pre-auth endpoint is intentionally non-diagnostic.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bootstrap_status_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) operationsStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.app.OperationsStatus(r.Context())
	if err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, auth.ErrForbidden) {
			writeError(w, err)
			return
		}
		// Do not leak schema, connection, or snapshot internals and never send
		// a partial aggregate when the authoritative read cannot complete.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "operations_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	principal, err := s.app.CurrentPrincipal(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	allowed := false
	for _, role := range principal.Roles {
		if role == "system_admin" {
			allowed = true
			break
		}
	}
	if !allowed {
		writeError(w, auth.ErrForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	snapshot, err := s.app.OperationalSnapshot(r.Context())
	if err != nil {
		http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	rendered, err := observability.Render(s.metrics.render(), snapshot)
	if err != nil {
		http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte(rendered))
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
	case http.MethodGet:
		s.listSessions(w, r)
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
	if !decodeBody(w, r, &req) {
		return
	}
	session, err := s.app.CreateSessionWithMetadata(r.Context(), req.Email, req.Password, app.SessionCreateMetadata{SourceIP: s.clientSource(r), UserAgent: r.UserAgent()})
	if err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			s.metrics.recordLoginRateLimit()
			w.Header().Set("Retry-After", "60")
		}
		writeError(w, err)
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

func (s *Server) browserSessionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createBrowserSession(w, r)
	case http.MethodDelete:
		s.revokeBrowserSession(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) createBrowserSession(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeError(w, auth.ErrForbidden)
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	created, err := s.app.CreateSessionWithMetadata(r.Context(), req.Email, req.Password, app.SessionCreateMetadata{SourceIP: s.clientSource(r), UserAgent: r.UserAgent()})
	if err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			s.metrics.recordLoginRateLimit()
			w.Header().Set("Retry-After", "60")
		}
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "nerocd_session", Value: created.Token, Path: "/api/v1", Expires: created.Session.ExpiresAt, HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusCreated, struct {
		Session any `json:"session"`
	}{Session: created.Session})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.app.ListSessions(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, sessions))
}

func (s *Server) revokeSessionByID(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	session, err := s.app.RevokeSession(r.Context(), req.SessionID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) revokeBrowserSession(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie("nerocd_session")
	if err := s.app.RevokeSessionToken(r.Context(), cookie.Value); err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "nerocd_session", Value: "", Path: "/api/v1", Expires: time.Unix(0, 0), MaxAge: -1, HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteStrictMode})
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
