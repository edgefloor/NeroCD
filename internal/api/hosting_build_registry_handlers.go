package api

import (
	"io"
	"mime"
	"net/http"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

func (s *Server) runnerBuildRegistryCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	content, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid JSON body")
		return
	}
	var in domain.HostingBuildRegistryCredentialRequest
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(raw) > 4096 || domain.DecodeHostingStorageProtocolJSON(raw, &in) != nil || hostingbuildprotocol.ValidateAuthority(in.Authority) != nil || !domain.ValidHostingSourceDeliveryID(in.DeliveryID) {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid registry credential request")
		return
	}
	if r.URL.RawQuery != "" || !s.sourceCredentialTransport(r) {
		writeError(w, app.ErrHostingRegistryCredentialUnavailable)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, app.ErrHostingRegistryCredentialUnavailable)
		return
	}
	out, err := s.app.RunnerHostingBuildRegistryCredential(r.Context(), in, token)
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
