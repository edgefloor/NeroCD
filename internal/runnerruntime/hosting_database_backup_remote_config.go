package runnerruntime

import (
	"errors"
	"net/netip"
	"net/url"

	"nerocd/internal/domain"
)

func databaseBackupRemotePolicy(c DatabaseExecutionConfig) (backupRemoteNetworkPolicy, error) {
	p := backupRemoteNetworkPolicy{privateOrigins: map[string][]netip.Prefix{}}
	invalid := errors.New("invalid protected remote backup network policy")
	if !c.RemoteBackups && (len(c.RemoteEndpoints) > 0 || len(c.RemoteCIDRs) > 0 || c.RemoteFixtureOrigin != "") {
		return p, invalid
	}
	if len(c.RemoteEndpoints) > 32 || len(c.RemoteCIDRs) > 32 {
		return p, invalid
	}
	var ranges []netip.Prefix
	for _, raw := range c.RemoteCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() || !prefix.Addr().IsPrivate() || prefix.Bits() < 8 {
			return p, invalid
		}
		ranges = append(ranges, prefix)
	}
	if (len(c.RemoteEndpoints) == 0) != (len(ranges) == 0) {
		return p, invalid
	}
	for _, endpoint := range c.RemoteEndpoints {
		if domain.ValidateBackupRemoteEndpoint(endpoint) != nil {
			return p, invalid
		}
		u, _ := url.Parse(endpoint)
		if u.Scheme != "https" {
			return p, invalid
		}
		origin := u.Scheme + "://" + u.Host
		p.privateOrigins[origin] = append([]netip.Prefix(nil), ranges...)
	}
	if c.RemoteFixtureOrigin != "" {
		if domain.ValidateBackupRemoteEndpoint(c.RemoteFixtureOrigin) != nil {
			return p, invalid
		}
		u, _ := url.Parse(c.RemoteFixtureOrigin)
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil || !ip.IsLoopback() || u.Scheme != "http" || u.Path != "" {
			return p, invalid
		}
		p.fixtureOrigin = c.RemoteFixtureOrigin
	}
	return p, nil
}
