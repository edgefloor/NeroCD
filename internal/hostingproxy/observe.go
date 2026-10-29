package hostingproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func (e *Engine) raw(ctx context.Context) (data map[string]json.RawMessage, err error) {
	t := &http.Transport{Proxy: nil}
	defer t.CloseIdleConnections()
	client := &http.Client{Transport: t, Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/api/rawdata", e.settings.ManagementPort), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != 200 {
		return nil, errors.New("traefik management unavailable")
	}

	err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&data)
	return data, err
}
func (e *Engine) loaded(ctx context.Context, p Plan) error {
	return e.loadedConfiguration(ctx, p, nil)
}

func (e *Engine) loadedConfiguration(ctx context.Context, p Plan, interruptedStorage *Plan) error {
	if err := e.daemon(ctx, p); err != nil {
		return err
	}
	if p.transportPolicy > 1 {
		return errors.New("unknown proxy transport policy")
	}
	if p.transportPolicy == 1 {
		root, err := os.OpenRoot(e.settings.Directory)
		if err != nil {
			return err
		}
		state := &storage{root: root}
		body, readErr := state.read("dynamic/routes.yml")
		closeErr := root.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(body, p.dynamic()) {
			return errors.New("proxy transport configuration file differs")
		}
	}
	data, err := e.raw(ctx)
	if err != nil {
		return err
	}
	var routers map[string]struct {
		Rule, Service, Status    string
		Priority                 int
		EntryPoints, Middlewares []string
		TLS                      *struct{ CertResolver string }
	}
	var services map[string]struct {
		Status       string
		LoadBalancer struct {
			Servers          []struct{ URL string }
			ServersTransport string
		}
	}
	if json.Unmarshal(data["routers"], &routers) != nil || json.Unmarshal(data["services"], &services) != nil {
		return errors.New("traefik routers/services unavailable")
	}
	count := 0
	for n := range routers {
		if strings.HasSuffix(n, "@file") {
			count++
		}
	}
	if count != 2*len(p.routes)+1 {
		return errors.New("loaded router count differs")
	}
	generation, ok := routers[p.prefix()+"-generation@file"]
	if !ok || generation.Status != "enabled" || generation.Service != "noop@internal" || generation.Rule != "Path(`/_nerocd/proxy-generation`)" || len(generation.EntryPoints) != 1 || generation.EntryPoints[0] != "traefik" {
		return errors.New("generation marker router not loaded")
	}
	if err = e.generationProbe(ctx, p); err != nil {
		return err
	}
	for _, r := range p.routes {
		name := p.prefix() + "-" + r.ID
		rule := "Host(`" + r.Hostname + "`)"
		if r.PathPrefix != "/" {
			rule += " && (Path(`" + r.PathPrefix + "`) || PathPrefix(`" + r.PathPrefix + "/`))"
		}
		service, ok := services[name+"@file"]
		if !ok || service.Status != "enabled" || len(service.LoadBalancer.Servers) != 1 || service.LoadBalancer.Servers[0].URL != fmt.Sprintf("http://127.0.0.1:%d", r.HostPort) {
			return errors.New("desired service is not loaded")
		}
		if p.transportPolicy == 0 && service.LoadBalancer.ServersTransport != "" {
			return errors.New("legacy service transport differs")
		}
		if p.transportPolicy == 1 && service.LoadBalancer.ServersTransport != p.transportName()+"@file" {
			return errors.New("desired service transport is not loaded")
		}
		for _, suffix := range []string{"-http", "-https"} {
			router, ok := routers[name+suffix+"@file"]
			entry := "web"
			if suffix == "-https" {
				entry = "websecure"
			}
			if !ok || router.Status != "enabled" || router.Rule != rule || router.Service != name || len(router.EntryPoints) != 1 || router.EntryPoints[0] != entry || len(router.Middlewares) != 1 || router.Middlewares[0] != name+"@file" || router.Priority != 100+len(r.PathPrefix) {
				return errors.New("desired router is not loaded")
			}
			if suffix == "-https" && (router.TLS == nil || router.TLS.CertResolver != "managed") {
				return errors.New("TLS router is not loaded")
			}
		}
		// The generated response marker proves Host/path selected this generation even for app 401/404.
		if interruptedStorage != nil && interruptedStorage.required(r.ID) {
			if err := e.interruptedStorageRouteMarker(ctx, p, r); err != nil {
				return err
			}
		} else if !e.probe(ctx, p, r, false).HTTPReady {
			return errors.New("host/path generation probe failed")
		}
	}
	return nil
}
func (e *Engine) probe(ctx context.Context, p Plan, r Route, secure bool) RouteObservation {
	result := RouteObservation{RouteID: r.ID, Certificate: Certificate{Hostname: r.Hostname, Status: "pending"}}
	resp, closeResponse, err := e.routeResponse(ctx, r, secure)
	if err != nil {
		return result
	}
	defer closeResponse()
	if resp.Header.Get("X-NeroCD-Proxy-Generation") != p.hash || resp.Header.Get("X-NeroCD-Proxy-Route") != r.ID {
		return result
	}
	if p.required(r.ID) && resp.StatusCode >= http.StatusInternalServerError {
		return result
	}
	if !secure {
		result.HTTPReady = true
		return result
	}
	if resp.TLS == nil || len(resp.TLS.VerifiedChains) == 0 || len(resp.TLS.PeerCertificates) == 0 {
		return result
	}
	cert := resp.TLS.PeerCertificates[0]
	if cert.IsCA || bytes.Equal(cert.RawIssuer, cert.RawSubject) && cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil || cert.VerifyHostname(r.Hostname) != nil || !time.Now().Before(cert.NotAfter) || time.Now().Before(cert.NotBefore) {
		return result
	}
	h := sha256.Sum256(cert.Raw)
	result.HTTPSReady = true
	result.Certificate = Certificate{Hostname: r.Hostname, Issuer: cert.Issuer.String(), Fingerprint: hex.EncodeToString(h[:]), ExpiresAt: cert.NotAfter, Status: "trusted"}
	return result
}

// This internal fact establishes routing configuration for an explicitly stopped
// selected backend. It never publishes an HTTPReady or HTTPSReady observation.
func (e *Engine) interruptedStorageRouteMarker(ctx context.Context, p Plan, r Route) error {
	resp, closeResponse, err := e.routeResponse(ctx, r, false)
	if err != nil {
		return errors.New("interrupted predecessor route marker unavailable")
	}
	defer closeResponse()
	if resp.Header.Get("X-NeroCD-Proxy-Generation") != p.hash || resp.Header.Get("X-NeroCD-Proxy-Route") != r.ID {
		return errors.New("interrupted predecessor route marker mismatch")
	}
	return ctx.Err()
}

func (e *Engine) routeResponse(ctx context.Context, r Route, secure bool) (*http.Response, func(), error) {
	port, scheme := "80", "http"
	if secure {
		port, scheme = "443", "https"
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", "127.0.0.1:"+port)
	}, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: r.Hostname, RootCAs: e.fixtureRoots}}
	if e.fixtureProbeDial != nil {
		transport.DialContext = e.fixtureProbeDial
	}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", scheme+"://"+r.Hostname+r.PathPrefix, nil)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	closeResponse := func() {
		// Closing this read-only response cannot invalidate the route header or
		// verified TLS handshake. The private transport also closes idle
		// connections on return.
		_ = resp.Body.Close()
		transport.CloseIdleConnections()
	}
	return resp, closeResponse, nil
}
func (e *Engine) observation(ctx context.Context, p Plan, id string) Result {
	result := Result{Outcome: Applied, DesiredGeneration: p.generation, LoadedGeneration: p.generation, Hash: p.hash, ContainerID: id}
	for _, r := range p.routes {
		httpResult := e.probe(ctx, p, r, false)
		tlsResult := e.probe(ctx, p, r, true)
		tlsResult.HTTPReady = httpResult.HTTPReady
		result.Routes = append(result.Routes, tlsResult)
	}
	return result
}

func (e *Engine) generationProbe(ctx context.Context, p Plan) (err error) {
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/_nerocd/proxy-generation", e.settings.ManagementPort), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.Header.Get("X-NeroCD-Proxy-Generation") != p.hash {
		return errors.New("loaded generation marker mismatch")
	}
	return nil
}
