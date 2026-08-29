package source

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

var metadataServiceIPs = map[string]struct{}{
	"169.254.169.254": {},
}

// ValidateRepositoryURL rejects repository URLs that name blocked hosts.
func ValidateRepositoryURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("repository url is required")
	}
	host, err := repositoryHost(value)
	if err != nil {
		return err
	}
	if host == "" {
		return errors.New("repository url host is required")
	}
	if isBlockedRepositoryHost(host) {
		return fmt.Errorf("repository url host %q is not allowed", host)
	}
	return nil
}

func repositoryHost(value string) (string, error) {
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "", errors.New("repository url is invalid")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "https", "http", "ssh", "git":
		default:
			return "", fmt.Errorf("repository url scheme %q is not allowed", parsed.Scheme)
		}
		return parsed.Hostname(), nil
	}

	beforePath, _, ok := strings.Cut(value, ":")
	if !ok || strings.Contains(beforePath, "/") {
		return "", errors.New("repository url must include a network host")
	}
	if userHost := strings.TrimSpace(beforePath); userHost != "" {
		if _, host, ok := strings.Cut(userHost, "@"); ok {
			return strings.TrimSpace(host), nil
		}
		return userHost, nil
	}
	return "", errors.New("repository url host is required")
}

func isBlockedRepositoryHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" {
		return true
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			return true
		}
		addr = addr.Unmap()
		if _, ok := metadataServiceIPs[addr.String()]; ok {
			return true
		}
		return addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsPrivate() || addr.IsUnspecified()
	}
	return false
}
