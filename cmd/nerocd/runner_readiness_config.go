package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

func runnerReadinessCapabilities(enabled, execute, once bool, complete string, specified bool, capabilities []string) ([]string, error) {
	found := false
	result := make([]string, 0, len(capabilities)+1)
	for _, capability := range capabilities {
		found = found || capability == domain.ServerReadinessCapability
		if enabled && capability == "shell" {
			if specified {
				return nil, errors.New("hosting readiness cannot share explicit shell capability")
			}
			continue
		}
		result = append(result, capability)
	}
	if !enabled {
		if found {
			return nil, errors.New("hosting_readiness_v1 requires --hosting-readiness")
		}
		return capabilities, nil
	}
	if !execute || once || complete != "" {
		return nil, errors.New("hosting readiness requires --execute and forbids --once and --complete-status")
	}
	if !found {
		result = append(result, domain.ServerReadinessCapability)
	}
	return result, nil
}

// Readiness bootstrap isolates credential-bearing enrollment and registration
// from legacy transports. Only enrollment has an idempotent request identity.
func postRunnerReadinessBootstrap(ctx context.Context, server, path string, in any, token string, out any) error {
	return runnerReadinessBootstrap(ctx, server, path, in, token, out, nil)
}

func runnerReadinessBootstrap(parent context.Context, server, path string, in any, token string, out any, configured *http.Client) error {
	if err := runnerclient.ValidateServerReadinessServer(server, runnerclient.ServerReadinessTransport{}); err != nil {
		return errors.New("invalid readiness bootstrap controller")
	}
	attempts := 1
	switch path {
	case "/api/v1/runner-enrollments/consume":
		attempts = 5
	case "/api/v1/runners/register":
	default:
		return errors.New("invalid readiness bootstrap route")
	}
	payload, err := json.Marshal(in)
	if err != nil || len(payload) > 8<<10 {
		return errors.New("invalid readiness bootstrap request")
	}
	client := http.Client{Timeout: 2 * time.Second}
	transport := http.DefaultTransport
	if configured != nil && configured.Transport != nil {
		transport = configured.Transport
	}
	base, ok := transport.(*http.Transport)
	if !ok || base.TLSClientConfig != nil && base.TLSClientConfig.InsecureSkipVerify {
		return errors.New("invalid readiness bootstrap TLS transport")
	}
	secured := base.Clone()
	secured.Proxy = nil
	secured.DisableKeepAlives = true
	secured.MaxResponseHeaderBytes = 8 << 10
	defer secured.CloseIdleConnections()
	client.Transport = secured
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	endpoint := strings.TrimRight(server, "/") + path
	delay := 100 * time.Millisecond
	for attempt := 0; attempt < attempts; attempt++ {
		if ctx.Err() != nil {
			return errors.New("readiness bootstrap cancelled")
		}
		err = runnerReadinessBootstrapRequest(ctx, &client, endpoint, payload, token, out)
		if err == nil || runnerclient.ClassifyFailure(err) != runnerclient.FailureTransient || attempt == attempts-1 {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("readiness bootstrap cancelled")
		case <-timer.C:
		}
		delay *= 2
	}
	return err
}
func runnerReadinessBootstrapRequest(parent context.Context, client *http.Client, endpoint string, payload []byte, token string, out any) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid readiness bootstrap request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	// Deliberately omit URL, headers, body and the transport's nested error.
	if err != nil {
		var verification *tls.CertificateVerificationError
		if errors.As(err, &verification) {
			return errors.New("readiness bootstrap TLS verification failed")
		}
		return &runnerclient.APIError{Method: http.MethodPost, URL: "<readiness-bootstrap>", Err: errors.New("transport failure")}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &runnerclient.APIError{Method: http.MethodPost, URL: "<readiness-bootstrap>", StatusCode: response.StatusCode, Status: fmt.Sprintf("%d %s", response.StatusCode, http.StatusText(response.StatusCode))}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (8<<10)+1))
	defer clear(raw)
	if err != nil {
		return &runnerclient.APIError{Method: http.MethodPost, URL: "<readiness-bootstrap>", Err: errors.New("response unavailable")}
	}
	if len(raw) > 8<<10 {
		return errors.New("readiness bootstrap response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return errors.New("invalid readiness bootstrap response")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return errors.New("invalid readiness bootstrap response")
	}
	return nil
}
