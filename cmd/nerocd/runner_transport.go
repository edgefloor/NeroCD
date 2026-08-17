package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
)

type runnerAPIError struct {
	Method     string
	URL        string
	StatusCode int
	Status     string
	Detail     string
	Err        error
}

func (e *runnerAPIError) Error() string {
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

func (e *runnerAPIError) Unwrap() error { return e.Err }

type runnerFailureClass uint8

const (
	runnerFailurePermanent runnerFailureClass = iota
	runnerFailureTransient
	runnerFailureAuthority
)

func classifyRunnerFailure(err error) runnerFailureClass {
	if err == nil {
		return runnerFailurePermanent
	}
	var apiErr *runnerAPIError
	if errors.As(err, &apiErr) && apiErr.StatusCode != 0 {
		switch {
		case apiErr.StatusCode == 408, apiErr.StatusCode == 429, apiErr.StatusCode >= 500:
			return runnerFailureTransient
		case apiErr.StatusCode == 401, apiErr.StatusCode == 403, apiErr.StatusCode == 404, apiErr.StatusCode == 409:
			return runnerFailureAuthority
		default:
			return runnerFailurePermanent
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return runnerFailureTransient
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return runnerFailureTransient
	}
	if apiErr != nil && apiErr.Err != nil {
		return runnerFailureTransient
	}
	return runnerFailurePermanent
}

func runnerHTTPStatus(err error, status int) bool {
	var apiErr *runnerAPIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

func runnerRequestLabel(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<runner-api>"
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}
