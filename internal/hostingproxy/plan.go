// Package hostingproxy manages one Linux host's file-provider Traefik instance.
// Its caller must hold the controller's fenced proxy lease before mutation.
package hostingproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const Image = "docker.io/library/traefik@sha256:0f6150202bc1fed72a7bd2107460eaa510e5b8a905f3fdb5b84f2c8619c33368"

type CA string

const (
	LetsEncrypt        CA = "letsencrypt"
	LetsEncryptStaging CA = "letsencrypt-staging"
)

type Identity struct{ ProxyID, ServerID, DestinationID, RunnerID, DaemonID string }
type Settings struct {
	Directory, DockerBinary, DockerSocket, Email string
	CA                                           CA
	ManagementPort                               uint16
}
type Route struct {
	ID, Hostname, PathPrefix string
	HostPort                 uint16
}

// Plan is immutable. Construct it using Prepare and persist its typed inputs.
type Plan struct {
	identity             Identity
	settings             Settings
	generation, previous uint64
	routes               []Route
	hash, staticHash     string
}

func (p Plan) Hash() string       { return p.hash }
func (p Plan) StaticHash() string { return p.staticHash }
func (p Plan) Generation() uint64 { return p.generation }
func (p Plan) Routes() []Route    { return append([]Route(nil), p.routes...) }

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)
var hostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
var pathPattern = regexp.MustCompile(`^/[a-zA-Z0-9/_~.-]*$`)

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validateSettings(s Settings) error {
	for _, p := range []string{s.Directory, s.DockerBinary, s.DockerSocket} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, "\x00\n\r,") {
			return errors.New("operator paths must be absolute canonical paths")
		}
	}
	if s.ManagementPort < 1024 {
		return errors.New("management port must be unprivileged")
	}
	if s.CA != LetsEncrypt && s.CA != LetsEncryptStaging {
		return errors.New("CA is not allowed")
	}
	a, e := mail.ParseAddress(s.Email)
	if e != nil || a.Address != s.Email {
		return errors.New("invalid ACME email")
	}
	return nil
}
func Prepare(i Identity, s Settings, generation, previous uint64, routes []Route) (Plan, error) {
	if err := validateSettings(s); err != nil {
		return Plan{}, err
	}
	for _, v := range []string{i.ProxyID, i.ServerID, i.DestinationID, i.RunnerID} {
		if !idPattern.MatchString(v) {
			return Plan{}, errors.New("invalid identity")
		}
	}
	if i.DaemonID == "" || len(i.DaemonID) > 128 || strings.ContainsAny(i.DaemonID, "\n\r\x00") {
		return Plan{}, errors.New("invalid daemon identity")
	}
	if generation == 0 || generation <= previous || len(routes) > 1000 {
		return Plan{}, errors.New("invalid generation or route count")
	}
	p := Plan{identity: i, settings: s, generation: generation, previous: previous, routes: append([]Route(nil), routes...)}
	ids, bindings := map[string]bool{}, map[string]bool{}
	for _, r := range p.routes {
		if !idPattern.MatchString(r.ID) || !hostPattern.MatchString(r.Hostname) || len(r.Hostname) > 253 || r.HostPort < 1024 || r.HostPort == s.ManagementPort {
			return Plan{}, errors.New("invalid route identity, hostname, or host port")
		}
		if len(r.PathPrefix) > 256 || !pathPattern.MatchString(r.PathPrefix) || path.Clean(r.PathPrefix) != r.PathPrefix || strings.Contains(r.PathPrefix, "//") || r.PathPrefix != "/" && strings.HasSuffix(r.PathPrefix, "/") {
			return Plan{}, errors.New("path must be a canonical literal origin path")
		}
		if strings.HasPrefix(r.PathPrefix, "/.well-known") || strings.HasPrefix(r.PathPrefix, "/dashboard") || strings.HasPrefix(r.PathPrefix, "/_nerocd") {
			return Plan{}, errors.New("reserved route path")
		}
		key := r.Hostname + " " + r.PathPrefix
		if ids[r.ID] || bindings[key] {
			return Plan{}, errors.New("duplicate route")
		}
		ids[r.ID] = true
		bindings[key] = true
	}
	sort.Slice(p.routes, func(a, b int) bool { return p.routes[a].ID < p.routes[b].ID })
	p.staticHash = digest(struct {
		Identity Identity
		Settings Settings
		Image    string
	}{i, s, Image})
	p.hash = digest(struct {
		Static               string
		Generation, Previous uint64
		Routes               []Route
	}{p.staticHash, generation, previous, p.routes})
	return p, nil
}
func (p Plan) name() string   { return "nerocd-proxy-" + p.identity.ProxyID }
func (p Plan) prefix() string { return fmt.Sprintf("g%d-%s", p.generation, p.hash[:16]) }
func (p Plan) dynamic() []byte {
	routers, services, middlewares := map[string]any{}, map[string]any{}, map[string]any{}
	marker := p.prefix()
	middlewares[marker] = map[string]any{"headers": map[string]any{"customResponseHeaders": map[string]string{"X-NeroCD-Proxy-Generation": p.hash}}}
	routers[marker+"-generation"] = map[string]any{"rule": "Path(`/_nerocd/proxy-generation`)", "entryPoints": []string{"traefik"}, "service": "noop@internal", "middlewares": []string{marker}}

	for _, r := range p.routes {
		name := marker + "-" + r.ID
		middlewares[name] = map[string]any{"headers": map[string]any{"customResponseHeaders": map[string]string{"X-NeroCD-Proxy-Generation": p.hash, "X-NeroCD-Proxy-Route": r.ID}}}
		rule := "Host(`" + r.Hostname + "`)"
		if r.PathPrefix != "/" {
			rule += " && (Path(`" + r.PathPrefix + "`) || PathPrefix(`" + r.PathPrefix + "/`))"
		}
		services[name] = map[string]any{"loadBalancer": map[string]any{"servers": []any{map[string]string{"url": fmt.Sprintf("http://127.0.0.1:%d", r.HostPort)}}}}
		for _, secure := range []bool{false, true} {
			suffix, entry := "-http", "web"
			if secure {
				suffix, entry = "-https", "websecure"
			}
			router := map[string]any{"rule": rule, "entryPoints": []string{entry}, "service": name, "middlewares": []string{name}, "priority": 100 + len(r.PathPrefix)}
			if secure {
				router["tls"] = map[string]any{"certResolver": "managed"}
			}
			routers[name+suffix] = router
		}
	}
	b, _ := json.Marshal(map[string]any{"http": map[string]any{"routers": routers, "services": services, "middlewares": middlewares}})
	return b
}
