package runnerclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"nerocd/internal/domain"
)

// ServerReadinessTransport permits literal-loopback HTTP only in explicit fixtures.
// The command never exposes this fixture option.
type ServerReadinessTransport struct{ TrustedLoopbackHTTP bool }

// ValidateServerReadinessServer requires HTTPS and an unambiguous controller URL.
func ValidateServerReadinessServer(server string, options ServerReadinessTransport) error {
	u, err := url.Parse(server)
	if err != nil || u.RawPath != "" || u.Path != "" && u.Path != "/" || ValidateRuntimeEnvironmentServer(server, RuntimeEnvironmentTransport(options)) != nil {
		return errors.New("invalid server readiness controller transport")
	}
	return nil
}

func (c Client) serverReadinessHTTP(options ServerReadinessTransport) (Client, func(), error) {
	if err := ValidateServerReadinessServer(c.Server, options); err != nil {
		return c, nil, err
	}
	secured, closeTransport, err := c.observationHTTP()
	if err != nil {
		return c, nil, err
	}
	// Never route dedicated host diagnostics or identity through ambient proxies.
	secured.HTTP.Transport.(*http.Transport).Proxy = nil
	secured.HTTP.Transport.(*http.Transport).MaxResponseHeaderBytes = 8 << 10
	secured.HTTP.Timeout = 2 * time.Second
	return secured, closeTransport, nil
}

func (c Client) serverReadinessPost(parent context.Context, path string, in, out any, keys []string, options []ServerReadinessTransport) error {
	if len(options) > 1 {
		return errors.New("invalid server readiness transport")
	}
	option := ServerReadinessTransport{}
	if len(options) == 1 {
		option = options[0]
	}
	secured, closeTransport, err := c.serverReadinessHTTP(option)
	if err != nil {
		return err
	}
	defer closeTransport()
	payload, err := json.Marshal(in)
	if err != nil || len(payload) > domain.ServerReadinessJSONLimit {
		return errors.New("invalid readiness request")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+path, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid readiness request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	response, err := secured.HTTP.Do(req)
	if err != nil {
		var verification *tls.CertificateVerificationError
		if errors.As(err, &verification) {
			return errors.New("readiness TLS verification failed")
		}
		return &APIError{Method: http.MethodPost, URL: RequestLabel(c.Server + path), Err: err}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return &APIError{Method: http.MethodPost, URL: RequestLabel(c.Server + path), StatusCode: response.StatusCode, Status: response.Status}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, domain.ServerReadinessJSONLimit+1))
	if err != nil {
		return &APIError{Method: http.MethodPost, URL: RequestLabel(c.Server + path), Err: err}
	}
	defer clear(raw)
	if len(raw) > domain.ServerReadinessJSONLimit || !observationUniqueJSON(json.NewDecoder(bytes.NewReader(raw))) || !observationObject(raw, keys...) {
		return errors.New("invalid readiness response")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return errors.New("invalid readiness response")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return errors.New("invalid readiness response")
	}
	return nil
}

var readinessResponseID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var readinessResponseSample = regexp.MustCompile(`^srd_[0-9a-f]{32}$`)

// ValidateServerReadinessChallenge checks the exact runner, profile and original window.
func ValidateServerReadinessChallenge(in domain.ServerReadinessChallenge, runner string, now time.Time) error {
	if !readinessResponseSample.MatchString(in.SampleID) || in.Profile != domain.ServerReadinessProfile || in.RunnerID != runner || !readinessResponseID.MatchString(in.RunnerID) || !readinessResponseID.MatchString(in.ServerID) || !readinessResponseID.MatchString(in.DestinationID) || in.IssuedAt.IsZero() || in.ExpiresAt.Sub(in.IssuedAt) != 10*time.Second || in.IssuedAt.After(now.Add(time.Second)) || !in.ExpiresAt.After(now) {
		return errors.New("invalid readiness challenge")
	}
	return nil
}

// ServerReadinessChallenge resolves one exact server-side challenge for this runner.
func (c Client) ServerReadinessChallenge(ctx context.Context, runner string, options ...ServerReadinessTransport) (domain.ServerReadinessChallenge, error) {
	var out domain.ServerReadinessChallenge
	err := c.serverReadinessPost(ctx, "/api/v1/runner/server-readiness/challenge", struct{}{}, &out, []string{"sample_id", "profile", "server_id", "destination_id", "runner_id", "issued_at", "expires_at"}, options)
	if err == nil {
		err = ValidateServerReadinessChallenge(out, runner, time.Now())
	}
	return out, err
}

// ReportServerReadiness sends immutable evidence and checks its receipt identity.
func (c Client) ReportServerReadiness(ctx context.Context, in domain.ServerReadinessReport, options ...ServerReadinessTransport) (domain.ServerReadinessAck, error) {
	var out domain.ServerReadinessAck
	if err := in.Validate(); err != nil {
		return out, err
	}
	err := c.serverReadinessPost(ctx, "/api/v1/runner/server-readiness/report", in, &out, []string{"sample_id", "observed_at", "accepted"}, options)
	if err == nil && (out.SampleID != in.SampleID || !out.Accepted || out.ObservedAt.IsZero() || out.ObservedAt.After(time.Now().Add(time.Second))) {
		err = errors.New("invalid readiness acknowledgment")
	}
	return out, err
}
