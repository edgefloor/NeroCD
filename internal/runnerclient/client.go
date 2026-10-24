package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIError describes a sanitized runner protocol transport or HTTP failure.
type APIError struct {
	Method     string
	URL        string
	StatusCode int
	Status     string
	Detail     string
	Err        error
}

func (e *APIError) Error() string {
	if e.Err != nil {
		// net/http transport errors can themselves embed the complete request URL.
		// Keep the cause for errors.Is/As classification, but never render it where
		// a lease fence or other query capability could reach runner logs.
		return fmt.Sprintf("%s %s: transport failure", e.Method, e.URL)
	}
	// Detail remains available to narrowly validate structured server outcomes,
	// but is not rendered because an upstream or proxy may reflect capabilities.
	return fmt.Sprintf("%s %s returned %s", e.Method, e.URL, e.Status)
}

func (e *APIError) Unwrap() error { return e.Err }

// FailureClass identifies how the runtime should handle a protocol failure.
type FailureClass uint8

const (
	// FailurePermanent cannot succeed without changing the request or configuration.
	FailurePermanent FailureClass = iota
	// FailureTransient may succeed when retried under the same authority.
	FailureTransient
	// FailureAuthority means the current runner authority is absent or stale.
	FailureAuthority
)

// ClassifyFailure maps a transport or protocol error to its retry behavior.
func ClassifyFailure(err error) FailureClass {
	if err == nil {
		return FailurePermanent
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode != 0 {
		switch {
		case apiErr.StatusCode == 408, apiErr.StatusCode == 429, apiErr.StatusCode >= 500:
			return FailureTransient
		case apiErr.StatusCode == 401, apiErr.StatusCode == 403, apiErr.StatusCode == 404, apiErr.StatusCode == 409:
			return FailureAuthority
		default:
			return FailurePermanent
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return FailureTransient
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return FailureTransient
	}
	if apiErr != nil && apiErr.Err != nil {
		return FailureTransient
	}
	return FailurePermanent
}

// HTTPStatus reports whether err is a runner API response with status.
func HTTPStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

// RequestLabel removes credentials and query data from a request URL.
func RequestLabel(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<runner-api>"
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

// Client performs typed runner protocol operations.
type Client struct {
	Server string
	Token  string
	HTTP   *http.Client
}

// New constructs a runner protocol client for server and token.
func New(server, token string) Client {
	return Client{Server: strings.TrimRight(server, "/"), Token: token, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func (c Client) post(ctx context.Context, path string, body, result any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(payload), result)
}

func (c Client) get(ctx context.Context, path string, result any) error {
	return c.do(ctx, http.MethodGet, path, nil, result)
}

func (c Client) do(ctx context.Context, method, path string, body io.Reader, result any) error {
	policy, database := ctx.Value(databaseTransportKey{}).(DatabaseTransport)
	if database {
		secured, closeTransport, err := c.serverReadinessHTTP(ServerReadinessTransport(policy))
		if err != nil {
			return errors.New("invalid database controller transport")
		}
		defer closeTransport()
		c = secured
	}
	rawURL := c.Server + path
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if database {
			// Keep cancellation classification without retaining a transport's
			// URL-bearing diagnostic or a reflected capability.
			cause := errors.New("database controller request failed")
			if errors.Is(err, context.Canceled) {
				cause = context.Canceled
			}
			if errors.Is(err, context.DeadlineExceeded) {
				cause = context.DeadlineExceeded
			}
			return &APIError{Method: method, URL: RequestLabel(rawURL), Err: cause}
		}
		return &APIError{Method: method, URL: RequestLabel(rawURL), Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 || database && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		status, diagnostic := resp.Status, ""
		if database {
			status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
			diagnostic = ""
			// Replay needs only the closed not-found code to recognize fencing.
			var envelope struct {
				Code string `json:"code"`
			}
			if resp.StatusCode == http.StatusNotFound && json.Unmarshal(detail, &envelope) == nil && envelope.Code == "not_found" {
				diagnostic = `{"code":"not_found"}`
			}
			clear(detail)
		} else {
			diagnostic = strings.TrimSpace(string(detail))
		}
		return &APIError{Method: method, URL: RequestLabel(rawURL), StatusCode: resp.StatusCode, Status: status, Detail: diagnostic}
	}
	if result == nil {
		return nil
	}
	// Identity opts into whole-document validation before runtime startup.
	// Other protocol responses retain their existing decoding behavior.
	if decoder, ok := result.(interface{ decodeResponse(io.Reader) error }); ok {
		err = decoder.decodeResponse(resp.Body)
	} else {
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result)
	}
	if err != nil {
		if database {
			err = errors.New("invalid database controller response")
		}
		return &APIError{Method: method, URL: RequestLabel(rawURL), Err: err}
	}
	return nil
}
