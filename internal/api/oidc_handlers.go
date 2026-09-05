package api

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"nerocd/internal/app"
)

const oidcFlowCookie = "nerocd_oidc_flow"

var oidcDeepLink = regexp.MustCompile(`\A/(?:runs|deployments|runners)/[A-Za-z0-9._:-]{1,200}\z`)
var oidcErrorCode = regexp.MustCompile(`\A[A-Za-z0-9._-]{1,128}\z`)

func (s *Server) oidcStatus(w http.ResponseWriter, _ *http.Request) {
	noStore(w)
	writeJSON(w, http.StatusOK, s.app.OIDCStatus())
}

func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	redirectPath, ok := validOIDCRedirectQuery(r.URL.Query())
	if !ok {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "invalid OIDC redirect")
		return
	}
	bootstrap, err := s.app.PublicBootstrapStatus(r.Context())
	if err != nil || bootstrap.Status != "complete" {
		writeErrorEnvelope(w, http.StatusServiceUnavailable, "oidc_unavailable", "OIDC sign-in is unavailable")
		return
	}
	started, err := s.app.StartOIDCLogin(r.Context(), redirectPath, s.clientSource(r))
	if err != nil {
		if !errors.Is(err, app.ErrOIDCDisabled) && strings.Contains(r.Header.Get("Accept"), "text/html") {
			w.Header().Set("Referrer-Policy", "no-referrer")
			http.Redirect(w, r, "/sign-in?oidc_error=failed", http.StatusFound)
			return
		}
		switch {
		case errors.Is(err, app.ErrOIDCDisabled):
			writeErrorEnvelope(w, http.StatusNotFound, "oidc_disabled", "OIDC sign-in is disabled")
		case errors.Is(err, app.ErrOIDCRateLimited):
			w.Header().Set("Retry-After", "60")
			writeErrorEnvelope(w, http.StatusTooManyRequests, "rate_limited", "OIDC sign-in is temporarily unavailable")
		default:
			writeErrorEnvelope(w, http.StatusServiceUnavailable, "oidc_unavailable", "OIDC sign-in is unavailable")
		}
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcFlowCookie, Value: started.Verifier, Path: "/api/v1/oidc", Expires: started.ExpiresAt, MaxAge: int(time.Until(started.ExpiresAt).Seconds()), HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, started.AuthorizationURL, http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	w.Header().Set("Referrer-Policy", "no-referrer")
	code, codeOK := singleQueryValue(r.URL.Query(), "code")
	state, stateOK := singleQueryValue(r.URL.Query(), "state")
	errorCode, errorPresent, errorOK := optionalQueryValue(r.URL.Query(), "error")
	issuer, issuerPresent, issuerOK := optionalQueryValue(r.URL.Query(), "iss")
	if !stateOK || !errorOK || !issuerOK || (errorPresent && !oidcErrorCode.MatchString(errorCode)) || (issuerPresent && !s.app.OIDCCallbackIssuerValid(issuer)) || codeOK == errorPresent || (!codeOK && !errorPresent) {
		writeErrorEnvelope(w, http.StatusBadRequest, "oidc_failed", "OIDC authentication failed")
		return
	}
	cookie, err := r.Cookie(oidcFlowCookie)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		writeErrorEnvelope(w, http.StatusBadRequest, "oidc_failed", "OIDC authentication failed")
		return
	}
	if errorPresent {
		bound, _ := s.app.RejectOIDCLogin(r.Context(), state, cookie.Value)
		if bound {
			s.clearOIDCFlowCookie(w)
			http.Redirect(w, r, "/sign-in?oidc_error=failed", http.StatusFound)
			return
		}
		writeErrorEnvelope(w, http.StatusBadRequest, "oidc_failed", "OIDC authentication failed")
		return
	}
	created, redirectPath, flowBound, err := s.app.CompleteOIDCLogin(r.Context(), code, state, cookie.Value, app.SessionCreateMetadata{SourceIP: s.clientSource(r), UserAgent: r.UserAgent()})
	if err != nil {
		if flowBound {
			s.clearOIDCFlowCookie(w)
			http.Redirect(w, r, "/sign-in?oidc_error=failed", http.StatusFound)
			return
		}
		writeErrorEnvelope(w, http.StatusBadRequest, "oidc_failed", "OIDC authentication failed")
		return
	}
	s.clearOIDCFlowCookie(w)
	http.SetCookie(w, &http.Cookie{Name: "nerocd_session", Value: created.Token, Path: "/api/v1", Expires: created.Session.ExpiresAt, HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, redirectPath, http.StatusFound)
}

func optionalQueryValue(values url.Values, name string) (string, bool, bool) {
	items, present := values[name]
	if !present {
		return "", false, true
	}
	if len(items) != 1 {
		return "", true, false
	}
	value := items[0]
	return value, true, value != "" && len(value) <= 2_048 && strings.TrimSpace(value) == value
}

func (s *Server) clearOIDCFlowCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: oidcFlowCookie, Path: "/api/v1/oidc", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode})
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func singleQueryValue(values url.Values, name string) (string, bool) {
	items := values[name]
	returnValue := ""
	if len(items) == 1 {
		returnValue = items[0]
	}
	return returnValue, len(items) == 1 && returnValue != "" && len(returnValue) <= 2_048 && strings.TrimSpace(returnValue) == returnValue
}

func validOIDCRedirectQuery(values url.Values) (string, bool) {
	for key := range values {
		if key != "redirect" {
			return "", false
		}
	}
	items, present := values["redirect"]
	if !present {
		return "/", true
	}
	if len(items) != 1 {
		return "", false
	}
	return validOIDCRedirect(items[0])
}

func validOIDCRedirect(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/", true
	}
	if len(value) > 500 || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\%\r\n") {
		return "", false
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" {
		return "", false
	}
	static := map[string]bool{"/": true, "/runs": true, "/deployments": true, "/runners": true, "/operations": true, "/approvals": true, "/projects": true, "/templates": true, "/logs": true, "/audit": true, "/settings": true}
	if !static[parsed.Path] && !oidcDeepLink.MatchString(parsed.Path) {
		return "", false
	}
	for key, values := range parsed.Query() {
		if key != "q" || len(values) != 1 || strings.TrimSpace(values[0]) == "" || len(values[0]) > 200 {
			return "", false
		}
	}
	return value, true
}
