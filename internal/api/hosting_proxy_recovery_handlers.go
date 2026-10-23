package api

import (
	"net/http"

	"nerocd/internal/domain"
)

func (s *Server) hostingProxyRecovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	status, err := s.app.HostingProxyRecovery(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, status, err)
}

func (s *Server) hostingProxyRecoveryPlan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingProxyRecoveryInput
	if !decodeBody(w, r, &in) {
		return
	}
	plan, err := s.app.PlanHostingProxyRecovery(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusCreated, plan, err)
}

func (s *Server) hostingProxyRecoveryApply(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingProxyRecoveryApplyInput
	if !decodeBody(w, r, &in) {
		return
	}
	resolution, err := s.app.ApplyHostingProxyRecovery(r.Context(), r.PathValue("id"), r.PathValue("plan_id"), in)
	proxyResponse(w, http.StatusOK, resolution, err)
}
