package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

func (s *Server) hostingSourceGrants(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		out, err := s.app.ListHostingSourceGrants(r.Context(), r.PathValue("id"), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(out))
		return
	}
	var in domain.HostingSourceGrantRequest
	if !decodeBody(w, r, &in) {
		return
	}
	out, err := s.app.RequestHostingSourceGrant(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"grant": out})
}
func (s *Server) hostingSourceGrantRevoke(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.app.RevokeHostingSourceGrant(r.Context(), r.PathValue("id"), r.PathValue("grant_id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}
func (s *Server) runnerBuildSourceCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in domain.HostingSourceCredentialRequest
	if !decodeBody(w, r, &in) {
		return
	}
	if !s.sourceCredentialTransport(r) {
		writeError(w, app.ErrHostingSourceCredentialUnavailable)
		return
	}
	out, err := s.app.RunnerHostingSourceCredential(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	defer out.Close()
	w.Header().Set("Content-Type", "application/json")
	if err = hostingbuildprotocol.WriteSourceCredential(w, out); err != nil {
		s.logger.Error("source credential response failed")
	}
}

func (s *Server) sourceCredentialTransport(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	// For this endpoint TrustedProxyCIDRs also grants TLS-termination trust.
	// Such proxies MUST strip and replace client forwarding headers. A single
	// exact assertion from the configured immediate socket peer is required.
	proto := r.Header.Values("X-Forwarded-Proto")
	if s.trustedProxy(peer) && len(proto) == 1 && proto[0] == "https" {
		return true
	}
	if !s.trustedLoopbackSourceCredentialHTTP || !literalCredentialLoopback(peer) {
		return false
	}
	for name := range r.Header {
		if strings.EqualFold(name, "Forwarded") || strings.HasPrefix(strings.ToLower(name), "x-forwarded-") {
			return false
		}
	}
	host, _, err = net.SplitHostPort(r.Host)
	if err != nil {
		host = strings.Trim(r.Host, "[]")
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && literalCredentialLoopback(ip)
}

func literalCredentialLoopback(ip netip.Addr) bool {
	return ip == netip.MustParseAddr("127.0.0.1") || ip == netip.IPv6Loopback()
}
