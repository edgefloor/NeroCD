package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

// decodeGitHubPushCommand requires exact field names and explicit non-null values,
// including nested secret selectors. The request contains metadata only.
func decodeGitHubPushCommand(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, domain.GitHubPushControlLimit+1))
	if len(raw) > domain.GitHubPushControlLimit {
		writeErrorEnvelope(w, 413, "request_too_large", "request body exceeds the limit")
		return false
	}
	if err != nil || !validManagedJSONUnicode(raw) || !closedPushObject(raw, reflect.TypeOf(out).Elem()) || json.Unmarshal(raw, out) != nil {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid GitHub push command")
		return false
	}
	return true
}
func closedPushObject(raw []byte, typ reflect.Type) bool {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	if typ.Kind() == reflect.Slice {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		for _, value := range values {
			if !closedPushObject(value, typ.Elem()) {
				return false
			}
		}
		return true
	}
	if typ.Kind() != reflect.Struct {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	fields := map[string]reflect.StructField{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fields[strings.Split(field.Tag.Get("json"), ",")[0]] = field
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		field, allowed := fields[key]
		if err != nil || !ok || !allowed || seen[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || !closedPushObject(value, field.Type) {
			return false
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return false
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return false
	}
	for key, field := range fields {
		if !seen[key] && !strings.Contains(field.Tag.Get("json"), ",omitempty") {
			return false
		}
	}
	return true
}
func writeGitHubPushError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrGitHubPushCapacity):
		w.Header().Set("Retry-After", "10")
		writeErrorEnvelope(w, 503, "github_push_capacity", "GitHub push capacity is full; retry later")
	case errors.Is(err, store.ErrGitHubPushUnavailable):
		writeErrorEnvelope(w, 503, "github_push_unavailable", "GitHub push builds require a configured PostgreSQL controller and managed secrets")
	case errors.Is(err, store.ErrGitHubPushRunnerUnsupported):
		writeErrorEnvelope(w, 409, "conflict", "authorize the current runner enrollment with hosting_build_trigger_revision_v1; restarting with flags does not update persisted capabilities")
	case errors.Is(err, app.ErrGitHubPushSignature):
		writeErrorEnvelope(w, 401, "unauthenticated", "invalid webhook signature")
	case errors.Is(err, app.ErrInvalidInput):
		writeErrorEnvelope(w, 400, "invalid_request", "invalid GitHub push request; select the required current metadata and provider-read secret for a private source")
	default:
		writeError(w, err)
	}
}
func (s *Server) pushPolicyResponse(w http.ResponseWriter, p domain.GitHubPushPolicy, err error) {
	if err != nil {
		writeGitHubPushError(w, err)
		return
	}
	if p.ID != "" && s.publicOrigin != "" {
		p.CallbackURL = s.publicOrigin + "/api/v1/hosting/github-push/" + p.ID
	}
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) githubPushPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		p, err := s.app.GetGitHubPushPolicy(r.Context(), r.PathValue("id"))
		s.pushPolicyResponse(w, p, err)
		return
	}
	var in domain.GitHubPushConfigure
	if !decodeGitHubPushCommand(w, r, &in) {
		return
	}
	p, err := s.app.ConfigureGitHubPushPolicy(r.Context(), r.PathValue("id"), in)
	s.pushPolicyResponse(w, p, err)
}
func (s *Server) githubPushVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.GitHubPushCommand
	if !decodeGitHubPushCommand(w, r, &in) {
		return
	}
	p, err := s.app.VerifyGitHubPushPolicy(r.Context(), r.PathValue("id"), in)
	s.pushPolicyResponse(w, p, err)
}
func (s *Server) githubPushEnabled(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.GitHubPushEnable
	if !decodeGitHubPushCommand(w, r, &in) {
		return
	}
	p, err := s.app.SetGitHubPushEnabled(r.Context(), r.PathValue("id"), in)
	s.pushPolicyResponse(w, p, err)
}
func (s *Server) githubPushRetry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.GitHubPushRetry
	if !decodeGitHubPushCommand(w, r, &in) {
		return
	}
	item, err := s.app.RetryGitHubPushDelivery(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeGitHubPushError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) githubPushDeliveries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	values := r.URL.Query()
	limit := 50
	for key, value := range values {
		if (key != "limit" && key != "cursor") || len(value) != 1 {
			writeErrorEnvelope(w, 400, "invalid_request", "invalid delivery pagination")
			return
		}
	}
	if text, ok := values["limit"]; ok {
		value, err := strconv.Atoi(text[0])
		if err != nil {
			writeErrorEnvelope(w, 400, "invalid_request", "invalid delivery limit")
			return
		}
		limit = value
	}
	page, err := s.app.ListGitHubPushDeliveries(r.Context(), r.PathValue("id"), values.Get("cursor"), limit)
	if err != nil {
		writeGitHubPushError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func (s *Server) githubPushReceive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeErrorEnvelope(w, 415, "unsupported_media_type", "application/json is required")
		return
	}
	headers := []string{"X-Hub-Signature-256", "X-GitHub-Event", "X-GitHub-Delivery"}
	for _, key := range headers {
		value := r.Header.Values(key)
		if len(value) != 1 || strings.TrimSpace(value[0]) != value[0] || value[0] == "" || strings.ContainsAny(value[0], ",\r\n") {
			writeErrorEnvelope(w, 400, "invalid_request", "invalid GitHub webhook headers")
			return
		}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, domain.GitHubPushPayloadLimit+1))
	if len(raw) > domain.GitHubPushPayloadLimit {
		writeErrorEnvelope(w, 413, "request_too_large", "request body exceeds the limit")
		return
	}
	if err != nil {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid webhook body")
		return
	}
	receipt, err := s.app.ReceiveGitHubPush(r.Context(), r.PathValue("id"), r.Header.Get(headers[2]), r.Header.Get(headers[1]), r.Header.Get(headers[0]), raw)
	if err != nil {
		writeGitHubPushError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, receipt)
}
