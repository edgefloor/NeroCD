package api

import (
	"net/http"

	"nerocd/internal/domain"
)

func proxyResponse(w http.ResponseWriter, status int, result any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, status, result)
}
func (s *Server) hostingProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		p, err := s.app.HostingProxy(r.Context(), r.PathValue("id"))
		proxyResponse(w, http.StatusOK, p, err)
		return
	}
	var in domain.HostingProxyInput
	if !decodeBody(w, r, &in) {
		return
	}
	p, err := s.app.ConfigureHostingProxy(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusAccepted, p, err)
}
func (s *Server) hostingProxyDisable(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingProxyMutationInput
	if !decodeBody(w, r, &in) {
		return
	}
	p, err := s.app.DisableHostingProxy(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusAccepted, p, err)
}
func (s *Server) hostingHostnameGrants(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		grants, err := s.app.HostingHostnameGrants(r.Context(), r.PathValue("id"))
		proxyResponse(w, http.StatusOK, grants, err)
		return
	}
	var in domain.HostingHostnameGrantInput
	if !decodeBody(w, r, &in) {
		return
	}
	grant, err := s.app.GrantHostingHostname(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusCreated, grant, err)
}
func (s *Server) hostingHostnameRevoke(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingProxyMutationInput
	if !decodeBody(w, r, &in) {
		return
	}
	p, err := s.app.RevokeHostingHostname(r.Context(), r.PathValue("id"), r.PathValue("grant_id"), in)
	proxyResponse(w, http.StatusAccepted, p, err)
}
func (s *Server) hostingRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		routes, err := s.app.HostingRoutes(r.Context(), r.PathValue("id"))
		proxyResponse(w, http.StatusOK, routes, err)
		return
	}
	var in domain.HostingRouteInput
	if !decodeBody(w, r, &in) {
		return
	}
	route, err := s.app.RequestHostingRoute(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusAccepted, route, err)
}

func (s *Server) hostingRouteSwitch(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingRouteSwitchInput
	if !decodeBody(w, r, &in) {
		return
	}
	operation, err := s.app.RequestHostingRouteSwitch(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusAccepted, operation, err)
}
func (s *Server) hostingRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		route, err := s.app.HostingRoute(r.Context(), r.PathValue("id"))
		proxyResponse(w, http.StatusOK, route, err)
	case http.MethodPatch:
		var in domain.HostingRouteInput
		if !decodeBody(w, r, &in) {
			return
		}
		route, err := s.app.UpdateHostingRoute(r.Context(), r.PathValue("id"), in)
		proxyResponse(w, http.StatusAccepted, route, err)
	case http.MethodDelete:
		var in domain.HostingRouteDeletionInput
		if !decodeBody(w, r, &in) {
			return
		}
		route, err := s.app.DeleteHostingRoute(r.Context(), r.PathValue("id"), in)
		proxyResponse(w, http.StatusAccepted, route, err)
	}
}
func (s *Server) proxyOperation(w http.ResponseWriter, r *http.Request) {
	operation, err := s.app.ProxyOperation(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, operation, err)
}
func (s *Server) proxyOperationCancel(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decodeBody(w, r, &in) {
		return
	}
	operation, err := s.app.CancelProxyOperation(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, operation, err)
}
func (s *Server) runnerProxyPlan(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingAuthority
	if !decodeBody(w, r, &in) {
		return
	}
	operation, err := s.app.RunnerProxyPlan(r.Context(), in)
	proxyResponse(w, http.StatusOK, operation, err)
}
func (s *Server) runnerProxyPrepared(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingProxyPreparedRequest
	if !decodeBody(w, r, &in) {
		return
	}
	operation, err := s.app.RunnerProxyPrepared(r.Context(), in)
	proxyResponse(w, http.StatusOK, operation, err)
}
func (s *Server) runnerProxyApplyBegin(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingAuthority
	if !decodeBody(w, r, &in) {
		return
	}
	operation, err := s.app.RunnerProxyApplyBegin(r.Context(), in)
	proxyResponse(w, http.StatusOK, operation, err)
}
func (s *Server) runnerProxyComplete(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingProxyCompleteRequest
	if !decodeBody(w, r, &in) {
		return
	}
	operation, err := s.app.RunnerProxyComplete(r.Context(), in)
	proxyResponse(w, http.StatusOK, operation, err)
}
func (s *Server) runnerProxyStatus(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingAuthority
	if !decodeBody(w, r, &in) {
		return
	}
	operation, err := s.app.RunnerProxyStatus(r.Context(), in)
	proxyResponse(w, http.StatusOK, operation, err)
}

func (s *Server) hostingRetainedReleases(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.ListHostingRetainedReleases(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if result == nil {
		result = []domain.HostingRetainedRelease{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) hostingReleaseCleanup(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingCleanupInput
	if !decodeBody(w, r, &in) {
		return
	}
	op, err := s.app.RequestHostingReleaseCleanup(r.Context(), r.PathValue("id"), r.PathValue("operation_id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}
