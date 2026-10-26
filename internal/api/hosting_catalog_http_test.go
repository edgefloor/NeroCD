package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The exact v1.47.0 API checks query-key presence, not a boolean value:
// https://github.com/sissbruecker/linkding/blob/v1.47.0/bookmarks/api/routes.py#L67-L75
// Its CSRF cookie is ld_csrftoken, and SessionAuthentication requires the header:
// https://github.com/sissbruecker/linkding/blob/v1.47.0/bookmarks/settings/base.py#L110-L112
func catalogBookmarkRequest(client *http.Client, base string, create bool) (*http.Request, error) {
	var body io.Reader
	method, path := "GET", "/api/bookmarks/"
	if create {
		method = "POST"
		path += "?disable_scraping&disable_html_snapshot"
		body = strings.NewReader(`{"url":"http://127.0.0.1:9090/health","title":"NeroCD retained catalog bookmark","tag_names":["acceptance"]}`)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Referer", base+"/bookmarks/")
	if client.Jar != nil {
		for _, cookie := range client.Jar.Cookies(req.URL) {
			if cookie.Name == "ld_csrftoken" {
				req.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
	}
	return req, nil
}

// Return fixed classes only. url.Error and net.OpError can contain URLs,
// credentials or server-provided text and must never be formatted into logs.
func catalogHTTPFailureClass(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "timeout"
	}
	var operation *net.OpError
	var dns *net.DNSError
	if errors.As(err, &operation) || errors.As(err, &dns) {
		return "network"
	}
	return "transport"
}
func catalogHTTPStatus(response *http.Response) int {
	if response == nil || response.StatusCode < 100 || response.StatusCode > 599 {
		return 0
	}
	return response.StatusCode
}

func TestCatalogBookmarkRequestMatchesLinkding147SessionContract(t *testing.T) {
	const base = "http://127.0.0.1:20101"
	origin, _ := url.Parse(base)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(origin, []*http.Cookie{{Name: "ld_csrftoken", Value: "fixture-csrf", Path: "/"}, {Name: "ld_sessionid", Value: "fixture-session", Path: "/"}, {Name: "csrftoken", Value: "wrong-cookie", Path: "/"}})
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	for _, create := range []bool{false, true} {
		req, err := catalogBookmarkRequest(client, base, create)
		if err != nil {
			t.Fatal("request construction failed")
		}
		if req.URL.Path != "/api/bookmarks/" || req.Header.Get("X-CSRFToken") != "fixture-csrf" || req.Header.Get("Referer") != base+"/bookmarks/" || req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Authorization") != "" {
			t.Fatal("session request contract differs")
		}
		if create {
			query := req.URL.Query()
			if req.Method != "POST" || len(query) != 2 || !query.Has("disable_scraping") || !query.Has("disable_html_snapshot") {
				t.Fatal("scraping or snapshot suppression omitted")
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != `{"url":"http://127.0.0.1:9090/health","title":"NeroCD retained catalog bookmark","tag_names":["acceptance"]}` {
				t.Fatal("bookmark payload changed")
			}
		} else if req.Method != "GET" || req.URL.RawQuery != "" || req.Body != nil {
			t.Fatal("persistence read contract changed")
		}
	}
	if client.Timeout != 10*time.Second {
		t.Fatal("client deadline changed")
	}
}

func TestCatalogHTTPFailureDiagnosticsAreBoundedAndRedacted(t *testing.T) {
	const secret = "must-not-appear-in-diagnostics"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"none", nil, "none"},
		{"deadline", &url.Error{Op: "Post", URL: "http://user:" + secret + "@host/", Err: context.DeadlineExceeded}, "timeout"},
		{"cancelled", context.Canceled, "cancelled"},
		{"network", &url.Error{Op: "Post", URL: secret, Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secret)}}, "network"},
		{"dns", &net.DNSError{Err: secret, Name: secret}, "network"},
		{"network_timeout", &net.DNSError{Err: secret, Name: secret, IsTimeout: true}, "timeout"},
		{"other", errors.New(secret), "transport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := catalogHTTPFailureClass(tc.err)
			if got != tc.want || strings.Contains(got, secret) {
				t.Fatal("diagnostic class differs or leaked error text")
			}
		})
	}
	for _, status := range []int{200, 201, 403, 500} {
		if catalogHTTPStatus(&http.Response{StatusCode: status}) != status {
			t.Fatal("HTTP status was discarded")
		}
	}
	if catalogHTTPStatus(nil) != 0 || catalogHTTPStatus(&http.Response{StatusCode: 999}) != 0 {
		t.Fatal("unknown HTTP status was invented")
	}
}
