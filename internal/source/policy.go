package source

// This file contains the runner-side source admission policy.  It is kept
// independent from git and net/http so the same decision is made before a
// checkout and by the dialer that establishes every outbound connection.

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// RepositoryPolicy contains only configuration and opaque credential IDs.
// It must never contain a token, private key, password, or SSH known-hosts
// material. Empty policy is deliberately the safe public HTTPS policy.
type RepositoryPolicy struct {
	Version               int      `json:"version"`
	State                 string   `json:"state"`
	Mode                  string   `json:"mode"`
	AllowedSchemes        []string `json:"allowed_schemes"`
	AllowedHosts          []string `json:"allowed_hosts"`
	AllowedCIDRs          []string `json:"allowed_cidrs"`
	RedirectHosts         []string `json:"redirect_hosts"`
	SSHHostFingerprints   []string `json:"ssh_host_fingerprints"`
	CredentialReferenceID string   `json:"credential_reference_id,omitempty"`
	AllowInternal         bool     `json:"allow_internal"`
}

func (p RepositoryPolicy) normalizedSchemes() map[string]bool {
	v := p.AllowedSchemes
	if len(v) == 0 {
		v = []string{"https"}
	}
	out := make(map[string]bool, len(v))
	for _, scheme := range v {
		out[strings.ToLower(strings.TrimSpace(scheme))] = true
	}
	return out
}

// ValidatePolicy rejects absent or ambiguous source admission. A legacy empty
// policy is intentionally non-deployable rather than silently becoming public.
func (p RepositoryPolicy) ValidatePolicy() error {
	if p.Version != 1 || p.State != "configured" {
		return errors.New("repository policy must be configured version 1")
	}
	if p.Mode != "public" && p.Mode != "internal" {
		return errors.New("repository policy mode must be public or internal")
	}
	if len(p.AllowedSchemes) == 0 || len(p.AllowedSchemes) > 2 || len(p.AllowedHosts) == 0 || len(p.AllowedHosts) > 32 || len(p.AllowedCIDRs) > 32 || len(p.RedirectHosts) > 32 || len(p.SSHHostFingerprints) > 32 {
		return errors.New("repository policy bounds are invalid")
	}
	if p.Mode == "public" && (p.AllowInternal || len(p.AllowedCIDRs) > 0) {
		return errors.New("public repository policy cannot allow internal addresses")
	}
	for scheme := range p.normalizedSchemes() {
		if scheme != "https" && scheme != "ssh" && !(scheme == "http" && p.Mode == "internal" && p.AllowInternal) {
			return errors.New("repository policy has unsupported scheme")
		}
	}
	if p.normalizedSchemes()["ssh"] && len(p.SSHHostFingerprints) == 0 {
		return errors.New("repository SSH policy requires host fingerprints")
	}
	for _, host := range append(append([]string{}, p.AllowedHosts...), p.RedirectHosts...) {
		if strings.TrimSpace(host) == "" || strings.ContainsAny(host, "/\\@:\x00") {
			return errors.New("repository policy host is invalid")
		}
	}
	for _, cidr := range p.AllowedCIDRs {
		if _, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err != nil {
			return errors.New("repository policy CIDR is invalid")
		}
	}
	for _, fingerprint := range p.SSHHostFingerprints {
		if !strings.HasPrefix(fingerprint, "SHA256:") || len(strings.TrimSpace(fingerprint)) < 16 {
			return errors.New("repository SSH fingerprint is invalid")
		}
	}
	if strings.ContainsAny(p.CredentialReferenceID, "\x00\r\n") {
		return errors.New("repository credential reference is invalid")
	}
	return nil
}

// ValidateURL admits only network URLs authorized by this exact policy.
// HTTP is never inferred: it needs both an explicit scheme and internal mode.
func (p RepositoryPolicy) ValidateURL(raw string, redirect bool) (*url.URL, error) {
	if err := p.ValidatePolicy(); err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("repository URL is invalid")
	}
	// Git accepts several URL spellings that have subtly different formatting
	// and credential semantics. Resolve only a plain hierarchical network URL;
	// query, fragment and opaque forms never belong in source identity.
	if u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("repository URL contains unsupported components")
	}
	// Credentials must be supplied through a separately permissioned transport.
	// SSH's username is a protocol routing identity (not a secret) and is the
	// sole exception; passwords are always forbidden.
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword || strings.ToLower(u.Scheme) != "ssh" || !validSSHUser(u.User.Username()) {
			return nil, errors.New("repository URL credentials are not permitted")
		}
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" || !p.normalizedSchemes()[scheme] || (scheme != "https" && scheme != "ssh" && !(scheme == "http" && p.AllowInternal)) {
		return nil, errors.New("repository URL scheme is not permitted")
	}
	if redirect && !matchesHost(u.Hostname(), p.RedirectHosts) && !matchesHost(u.Hostname(), p.AllowedHosts) {
		return nil, errors.New("repository redirect host is not permitted")
	}
	if !redirect && !matchesHost(u.Hostname(), p.AllowedHosts) && len(p.AllowedHosts) > 0 {
		return nil, errors.New("repository host is not permitted")
	}
	return u, nil
}

func validSSHUser(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func matchesHost(host string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, item := range allowed {
		item = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(item), "."))
		if item != "" && (host == item || strings.HasPrefix(item, ".") && strings.HasSuffix(host, item)) {
			return true
		}
	}
	return false
}

// ValidateAddress applies at connection time, after DNS lookup. Private,
// loopback, link-local, multicast, unspecified and metadata addresses are
// denied unless the exact address is in an explicitly configured CIDR.
func (p RepositoryPolicy) ValidateAddress(addr netip.Addr) error {
	addr = addr.Unmap()
	allowed := false
	for _, raw := range p.AllowedCIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err == nil && prefix.Contains(addr) {
			allowed = true
			break
		}
	}
	blocked := addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsPrivate() || addr.IsMulticast() || addr.IsUnspecified() || addr.String() == "169.254.169.254"
	if blocked && (!p.AllowInternal || !allowed) {
		return fmt.Errorf("repository address %s is not permitted", addr)
	}
	return nil
}

// ValidateResolvedHost is intentionally separate from URL parsing so callers
// must validate every resolved address used by a controlled dialer.
func (p RepositoryPolicy) ValidateResolvedHost(host string, ips []net.IPAddr) error {
	if !matchesHost(host, p.AllowedHosts) && len(p.AllowedHosts) > 0 {
		return errors.New("repository host is not permitted")
	}
	if len(ips) == 0 {
		return errors.New("repository host did not resolve")
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok {
			return errors.New("repository address is invalid")
		}
		if err := p.ValidateAddress(addr); err != nil {
			return err
		}
	}
	return nil
}
