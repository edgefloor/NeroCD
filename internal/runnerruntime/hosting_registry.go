package runnerruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

// RegistryPullConfig explicitly enables protected private-image pulls.
type RegistryPullConfig struct{ TrustedLoopbackHTTP bool }

// prepareRegistryImage keeps auth only in memory and never changes Docker login files.
func (e *imageEngine) prepareRegistryImage(ctx context.Context, s domain.HostingSnapshot, d *domain.HostingRegistryCredentialDelivery) (domain.HostingRelease, error) {
	normalized, err := domain.NormalizeHostingImage(s.ImageReference)
	if err != nil {
		return domain.HostingRelease{}, err
	}
	registry, err := domain.HostingImageRegistry(normalized)
	if err != nil || registry != d.Credential.Registry || !time.Now().Before(d.UseUntil) {
		return domain.HostingRelease{}, errors.New("registry credential unavailable")
	}
	inspection, cancelInspection := context.WithDeadline(ctx, d.UseUntil)
	defer cancelInspection()
	daemon, err := e.daemonID(inspection)
	if err != nil {
		return domain.HostingRelease{}, err
	}
	// The standard Engine auth header is scoped to this request on the pinned Unix socket.
	u, _ := url.Parse(e.endpoint)
	transport := &http.Transport{Proxy: nil, DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, "unix", u.Path)
	}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	type auth struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Server   string `json:"serveraddress"`
	}
	raw, err := json.Marshal(auth{d.Credential.Username, d.Credential.Password, registry})
	if err != nil {
		return domain.HostingRelease{}, errors.New("registry credential unavailable")
	}
	defer clear(raw)
	req, err := http.NewRequestWithContext(ctx, "POST", "http://docker/images/create?fromImage="+url.QueryEscape(normalized), nil)
	if err != nil {
		return domain.HostingRelease{}, errors.New("registry pull failed")
	}
	req.Header.Set("X-Registry-Auth", base64.URLEncoding.EncodeToString(raw))
	// As with runtime environment create, the window bounds starting use.
	// Once started, the lease supervisor owns cancellation of the pull.
	if !time.Now().Before(d.UseUntil) || ctx.Err() != nil {
		return domain.HostingRelease{}, errors.New("registry pull window expired")
	}
	resp, err := client.Do(req)
	req.Header.Del("X-Registry-Auth")
	if err != nil {
		return domain.HostingRelease{}, errors.New("registry pull failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return domain.HostingRelease{}, errors.New("registry pull failed")
	}
	limited := &io.LimitedReader{R: resp.Body, N: 8 << 20}
	dec := json.NewDecoder(limited)
	digest := ""
	count := 0
	for {
		var progress struct {
			Status      string          `json:"status"`
			Error       string          `json:"error"`
			ErrorDetail json.RawMessage `json:"errorDetail"`
		}
		err = dec.Decode(&progress)
		if err == io.EOF {
			break
		}
		if err != nil || limited.N <= 0 || progress.Error != "" || len(progress.ErrorDetail) > 0 || count >= 100000 {
			return domain.HostingRelease{}, errors.New("registry pull failed")
		}
		count++
		if strings.HasPrefix(progress.Status, "Digest: ") {
			if digest != "" {
				return domain.HostingRelease{}, errors.New("ambiguous pulled digest")
			}
			digest = strings.TrimPrefix(progress.Status, "Digest: ")
		}
	}
	if ctx.Err() != nil {
		return domain.HostingRelease{}, errors.New("registry pull expired")
	}
	return e.preparedPulledImage(ctx, normalized, daemon, digest)
}

func validateRegistryPullConfig(c Config) error {
	if c.RegistryPull == nil {
		return nil
	}
	if !c.Execute || c.CompleteStatus != "" {
		return errors.New("registry pull requires execution and forbids generic completion")
	}
	return runnerclient.ValidateRuntimeEnvironmentServer(c.Server, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: c.RegistryPull.TrustedLoopbackHTTP})
}

// registryPullDeliveryID preserves ordinary-image history and fences each stack component window.
func registryPullDeliveryID(a domain.HostingAuthority, component string) string {
	purpose := "pull"
	if component != "" {
		purpose = domain.HostingDocumentHash(struct {
			Authority domain.HostingAuthority
			Component string
		}{a, component})
	}
	key := attemptMutationKey("registry", a.LeaseID, a.Attempt, purpose)
	return domain.HostingDocumentHash(key)[7:39]
}
