package source

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// NormalizeGitRepositoryURL preserves SSH relative paths through Git's home-relative URL form.
// TransportURL restores SCP spelling before Git connects to virtual Git shells.
func NormalizeGitRepositoryURL(raw string) (string, error) {
	invalid := errors.New("invalid Git repository URL")
	if raw == "" || len(raw) > 8192 || raw != strings.TrimSpace(raw) || strings.ContainsFunc(raw, unicode.IsControl) || strings.ContainsAny(raw, "\\?#") {
		return "", invalid
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "ssh" || u.Opaque != "" || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path == "" || strings.ContainsFunc(u.Path, unicode.IsControl) || u.User == nil || !validSSHUser(u.User.Username()) {
			return "", invalid
		}
		if _, password := u.User.Password(); password {
			return "", invalid
		}
		return u.String(), nil
	}
	userHost, path, ok := strings.Cut(raw, ":")
	user, host, hasUser := strings.Cut(userHost, "@")
	if !ok || !hasUser || !validSSHUser(user) || host == "" || strings.ContainsAny(host, "/@:[ ]") || path == "" || strings.HasPrefix(path, "~") {
		return "", invalid
	}
	if !strings.HasPrefix(path, "/") {
		path = "/~/" + path
	}
	u := &url.URL{Scheme: "ssh", User: url.User(user), Host: host, Path: path}
	return u.String(), nil
}

// TransportURL keeps home-relative SSH repository commands compatible with SCP inputs.
// Explicit custom-port URLs retain their original Git spelling.
func TransportURL(u *url.URL) string {
	if u.Scheme == "ssh" && u.Port() == "" && u.User != nil && strings.HasPrefix(u.Path, "/~/") {
		return u.User.Username() + "@" + u.Host + ":" + strings.TrimPrefix(u.Path, "/~/")
	}
	return u.String()
}
