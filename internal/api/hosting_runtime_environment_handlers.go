package api

import (
	"mime"
	"net/http"

	"nerocd/internal/app"
	"nerocd/internal/hostingruntimeprotocol"
)

func (s *Server) runnerHostingRuntimeEnvironment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	content, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	in, err := hostingruntimeprotocol.ReadRequest(r.Body)
	if err != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if !s.runtimeEnvironmentTransport(r) {
		writeError(w, app.ErrHostingRuntimeEnvironmentUnavailable)
		return
	}
	out, err := s.app.RunnerHostingRuntimeEnvironment(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	defer out.Close()
	w.Header().Set("Content-Type", "application/json")
	if hostingruntimeprotocol.WriteEnvironment(w, out) != nil {
		s.logger.Error("runtime environment response failed")
	}
}

func (s *Server) runtimeEnvironmentTransport(r *http.Request) bool {
	// Reuse the established trusted TLS termination policy with a separate
	// explicit fixture option. Copy configuration rather than mutating it.
	transport := Server{trustedProxies: s.trustedProxies, trustedLoopbackSourceCredentialHTTP: s.trustedLoopbackRuntimeEnvironmentHTTP}
	return transport.sourceCredentialTransport(r)
}
