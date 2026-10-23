package api

import (
	"nerocd/internal/app"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) serverBootstrapOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	v, e := s.app.ServerBootstrapOptions(r.Context(), r.URL.Query().Get("project_id"))
	proxyResponse(w, http.StatusOK, v, e)
}
func (s *Server) serverBootstraps(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost {
		var in app.ServerBootstrapCreateInput
		if !decodeBody(w, r, &in) {
			return
		}
		v, e := s.app.CreateServerBootstrap(r.Context(), in)
		proxyResponse(w, http.StatusCreated, v, e)
		return
	}
	limit, offset := 50, 0
	var e error
	if q := r.URL.Query().Get("limit"); q != "" {
		limit, e = strconv.Atoi(q)
		if e != nil {
			http.Error(w, "invalid limit", 400)
			return
		}
	}
	if q := r.URL.Query().Get("offset"); q != "" {
		offset, e = strconv.Atoi(q)
		if e != nil {
			http.Error(w, "invalid offset", 400)
			return
		}
	}
	v, e := s.app.ListServerBootstraps(r.Context(), r.URL.Query().Get("project_id"), limit, offset)
	proxyResponse(w, http.StatusOK, v, e)
}
func (s *Server) serverBootstrap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	v, e := s.app.GetServerBootstrap(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, v, e)
}
func (s *Server) serverBootstrapCommand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var in app.ServerBootstrapCommandInput
	switch action {
	case "review":
		var dto struct {
			CommandID       string `json:"command_id"`
			ExpectedVersion int64  `json:"expected_version"`
			ProbeReceiptID  string `json:"probe_receipt_id"`
		}
		if !decodeBody(w, r, &dto) {
			return
		}
		in.CommandID = dto.CommandID
		in.ExpectedVersion = dto.ExpectedVersion
		in.ProbeReceiptID = dto.ProbeReceiptID
	case "install":
		var dto struct {
			CommandID       string `json:"command_id"`
			ExpectedVersion int64  `json:"expected_version"`
			ManifestSHA256  string `json:"manifest_sha256"`
			Acknowledge     bool   `json:"acknowledge_install_and_start"`
			GrantProject    *bool  `json:"grant_project"`
		}
		if !decodeBody(w, r, &dto) {
			return
		}
		in.CommandID = dto.CommandID
		in.ExpectedVersion = dto.ExpectedVersion
		in.ManifestSHA256 = dto.ManifestSHA256
		in.AcknowledgeInstallAndStart = dto.Acknowledge
		in.GrantProject = dto.GrantProject
	default:
		var dto struct {
			CommandID       string `json:"command_id"`
			ExpectedVersion int64  `json:"expected_version"`
		}
		if !decodeBody(w, r, &dto) {
			return
		}
		in.CommandID = dto.CommandID
		in.ExpectedVersion = dto.ExpectedVersion
	}
	v, e := s.app.CommandServerBootstrap(r.Context(), r.PathValue("id"), action, in)
	status := http.StatusAccepted
	if action == "review" || action == "cancel" {
		status = http.StatusOK
	}
	proxyResponse(w, status, v, e)
}
