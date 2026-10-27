package api

import (
	"io"
	"mime"
	"net/http"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

func (s *Server) runnerHostingRegistryCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	content, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid JSON body")
		return
	}
	var in domain.HostingRegistryCredentialRequest
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(raw) > 4096 || domain.DecodeHostingStorageProtocolJSON(raw, &in) != nil || in.Validate() != nil {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid registry credential request")
		return
	}
	if r.URL.RawQuery != "" || !s.runtimeEnvironmentTransport(r) {
		writeError(w, app.ErrHostingRegistryCredentialUnavailable)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, app.ErrHostingRegistryCredentialUnavailable)
		return
	}
	out, err := s.app.RunnerHostingRegistryCredential(r.Context(), in, token)
	if err != nil {
		writeError(w, err)
		return
	}
	defer out.Close()
	w.Header().Set("Content-Type", "application/json")
	if domain.WriteHostingRegistryDelivery(w, out) != nil {
		s.logger.Error("registry credential response failed")
	}
}
