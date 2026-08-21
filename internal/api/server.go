package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/store"
)

const (
	maxJSONBodyBytes = 1 << 20 // Requests are control-plane documents, never artifacts.
	maxPageLimit     = 100
	maxPageOffset    = 100_000
	maxRequestIDLen  = 128
)

var safeRequestID = regexp.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9._-]{0,127}\z`)

type Server struct {
	app            *app.Service
	logger         *slog.Logger
	mux            *http.ServeMux
	metrics        *metrics
	cookieSecure   bool
	publicOrigin   string
	trustedProxies []netip.Prefix
	draining       atomic.Bool
}

type ServerConfig struct {
	// AllowInsecureCookies permits non-Secure cookies only for explicit local
	// development or HTTP test servers. The zero value remains secure.
	AllowInsecureCookies bool
	PublicOrigin         string
	TrustedProxyCIDRs    []string
}

type PublicRoute struct {
	Method string
	Path   string
}

type metrics struct {
	mu               sync.Mutex
	requests         map[string]int64
	totalLatencyMS   int64
	loginRateLimited int64
}

const maxMetricSeries = 512

func (m *metrics) recordLoginRateLimit() { m.mu.Lock(); defer m.mu.Unlock(); m.loginRateLimited++ }

func newMetrics() *metrics {
	return &metrics{requests: map[string]int64{}}
}

func (m *metrics) record(method string, path string, status int, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := metricMethod(method) + "|" + metricRoute(path) + "|" + metricStatus(status)
	if _, known := m.requests[key]; !known && len(m.requests) >= maxMetricSeries {
		key = method + "|/other|" + strconv.Itoa(status)
	}
	m.requests[key]++
	m.totalLatencyMS += duration.Milliseconds()
}

func metricMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return method
	default:
		return "other"
	}
}
func metricStatus(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return "other"
	}
}
func metricRoute(path string) string {
	if path == "/metrics" {
		return "/metrics"
	}
	for _, route := range publicRoutes {
		if route.Path == path {
			return route.Path
		}
	}
	if strings.HasPrefix(path, "/api/v1/repositories/") && strings.HasSuffix(path, "/policy") {
		return "/api/v1/repositories/{id}/policy"
	}
	return "/other"
}

func (m *metrics) render() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out strings.Builder
	out.WriteString("# HELP nerocd_http_requests_total Total HTTP requests by method, path, and status.\n")
	out.WriteString("# TYPE nerocd_http_requests_total counter\n")
	for key, count := range m.requests {
		parts := strings.Split(key, "|")
		out.WriteString(`nerocd_http_requests_total{method="`)
		out.WriteString(parts[0])
		out.WriteString(`",path="`)
		out.WriteString(parts[1])
		out.WriteString(`",status="`)
		out.WriteString(parts[2])
		out.WriteString(`"} `)
		out.WriteString(strconv.FormatInt(count, 10))
		out.WriteString("\n")
	}
	out.WriteString("# HELP nerocd_http_request_duration_milliseconds_sum Total request duration in milliseconds.\n")
	out.WriteString("# TYPE nerocd_http_request_duration_milliseconds_sum counter\n")
	out.WriteString("nerocd_http_request_duration_milliseconds_sum ")
	out.WriteString(strconv.FormatInt(m.totalLatencyMS, 10))
	out.WriteString("\n")
	out.WriteString("# HELP nerocd_auth_login_rate_limited_total Login requests rejected by the local limiter.\n")
	out.WriteString("# TYPE nerocd_auth_login_rate_limited_total counter\n")
	out.WriteString("nerocd_auth_login_rate_limited_total ")
	out.WriteString(strconv.FormatInt(m.loginRateLimited, 10))
	out.WriteString("\n")
	return out.String()
}

func NewServer(appService *app.Service, logger *slog.Logger, static fs.FS) *Server {
	return NewServerWithConfig(appService, logger, static, ServerConfig{})
}

func NewServerWithConfig(appService *app.Service, logger *slog.Logger, static fs.FS, cfg ServerConfig) *Server {
	trusted, err := parseTrustedProxyCIDRs(cfg.TrustedProxyCIDRs)
	if err != nil {
		panic("invalid trusted proxy configuration")
	}
	s := &Server{app: appService, logger: logger, mux: http.NewServeMux(), metrics: newMetrics(), cookieSecure: !cfg.AllowInsecureCookies, publicOrigin: strings.TrimRight(strings.TrimSpace(cfg.PublicOrigin), "/"), trustedProxies: trusted}
	s.routes(static)
	return s
}

func parseTrustedProxyCIDRs(values []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, err
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}

func (s *Server) trustedProxy(addr netip.Addr) bool {
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// clientSource trusts X-Forwarded-For only from an explicitly configured
// immediate proxy. It walks the chain from the trusted edge toward the client,
// returning the first untrusted address; malformed headers never replace the
// socket peer. This keeps rate-limit keys outside client control.
func (s *Server) clientSource(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	if !s.trustedProxy(peer) {
		return peer.String()
	}
	values := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return peer.String()
	}
	for i := len(values) - 1; i >= 0; i-- {
		candidate, parseErr := netip.ParseAddr(strings.TrimSpace(values[i]))
		if parseErr != nil {
			return peer.String()
		}
		if !s.trustedProxy(candidate) {
			return candidate.String()
		}
	}
	return peer.String()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := requestID(r)
	r = r.WithContext(app.WithRequestID(r.Context(), requestID))
	w.Header().Set("X-Request-ID", requestID)
	s.securityHeaders(w)
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		if recovered := recover(); recovered != nil {
			// Never serialize or log a recovered value: panics often carry an
			// upstream body, token, or sentinel. The request ID is enough to
			// correlate the safe client response with the server-side event.
			s.logger.Error("request panic recovered", "request_id", requestID, "panic_type", fmt.Sprintf("%T", recovered))
			if !rec.wroteHeader {
				writeJSON(rec, http.StatusInternalServerError, map[string]string{"error": "internal_error", "request_id": requestID})
			}
		}
		s.metrics.record(r.Method, r.URL.Path, rec.status, time.Since(start))
		s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration_ms", time.Since(start).Milliseconds(), "request_id", requestID)
	}()
	if s.draining.Load() && r.URL.Path != "/api/v1/health" && r.URL.Path != "/api/v1/ready" {
		writeJSON(rec, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	if err := validatePagination(r); err != nil {
		writeErrorEnvelope(rec, http.StatusBadRequest, "bad_request", "invalid pagination")
		return
	}
	if requiresJSONDocument(r) && !strictJSONDocument(rec, r) {
		return
	}
	s.mux.ServeHTTP(rec, r)
}

func (s *Server) securityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	// The SPA's accessible dialog/toast primitives set positional style
	// attributes at runtime. Keep executable content on the strict default
	// policy while permitting only same-origin styles plus those attributes.
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'; object-src 'none'")
	if strings.HasPrefix(strings.ToLower(s.publicOrigin), "https://") {
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
	}
}

func requiresJSONDocument(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	default:
		return false
	}
}

// strictJSONDocument verifies the media type, bounded size, and one-document
// framing once at the HTTP boundary. Handlers then decode the exact buffered
// document into their typed request shape.
func strictJSONDocument(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeErrorEnvelope(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "application/json is required")
		return false
	}
	if r.ContentLength > maxJSONBodyBytes {
		writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the limit")
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	if err != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	if len(body) > maxJSONBodyBytes {
		writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the limit")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var document json.RawMessage
	if err := decoder.Decode(&document); err != nil || len(document) == 0 {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "JSON body must contain one document")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return true
}

func validatePagination(r *http.Request) error {
	query := r.URL.Query()
	for _, name := range []string{"limit", "offset"} {
		value, present := query[name]
		if !present || len(value) != 1 || strings.TrimSpace(value[0]) == "" {
			if present {
				return errors.New("invalid pagination")
			}
			continue
		}
		parsed, err := strconv.Atoi(value[0])
		if err != nil || parsed < 0 || (name == "limit" && parsed > maxPageLimit) || (name == "offset" && parsed > maxPageOffset) {
			return errors.New("invalid pagination")
		}
	}
	return nil
}

// SetDraining is called by the listener lifecycle before it stops accepting
// requests. Existing handlers drain; all later API requests, including ready,
// receive a stable unavailable response.
func (s *Server) SetDraining(value bool) { s.draining.Store(value) }

func (s *Server) routes(static fs.FS) {
	for _, route := range publicRoutes {
		handler := http.HandlerFunc(s.handlerFor(route.Path))
		if requiresRunnerAuth(route.Path) {
			handler = s.authenticateRunner(handler)
		} else if route.Path == "/api/v1/browser-sessions" && route.Method == http.MethodDelete {
			handler = s.authenticateBrowser(handler)
		} else if (route.Path == "/api/v1/sessions" && route.Method == http.MethodGet) || requiresAuth(route.Path) {
			handler = s.authenticate(handler)
		}
		s.mux.Handle(route.Method+" "+route.Path, handler)
	}
	s.mux.Handle(http.MethodGet+" /metrics", s.authenticate(http.HandlerFunc(s.metricsHandler)))
	s.mux.Handle("/", spaFileServer(static))
}

func requiresAuth(path string) bool {
	switch path {
	case "/api/v1/health", "/api/v1/ready", "/api/v1/bootstrap-status", "/api/v1/sessions", "/api/v1/browser-sessions", "/api/v1/runner-enrollments/consume":
		return false
	default:
		return true
	}
}

func requiresRunnerAuth(path string) bool {
	switch path {
	case "/api/v1/runners/heartbeat", "/api/v1/runners/telemetry", "/api/v1/runners/claim", "/api/v1/runners/renew", "/api/v1/runners/lease", "/api/v1/runners/logs", "/api/v1/runners/events/batch", "/api/v1/runners/secrets/access", "/api/v1/runners/artifacts", "/api/v1/runners/complete", "/api/v1/runners/deployments/transition", "/api/v1/runners/deployments/plan", "/api/v1/runners/deployments/status", "/api/v1/runners/deployments/provenance", "/api/v1/runners/deployments/fail-and-rollback":
		return true
	default:
		return false
	}
}

func (s *Server) authenticate(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var principal auth.Principal
		var err error
		var source auth.CredentialSource
		if token, ok := bearerToken(r.Header.Get("Authorization")); ok {
			principal, err = s.app.AuthenticateSessionToken(r.Context(), token)
			source = auth.CredentialSourceBearer
		} else if cookie, cookieErr := r.Cookie("nerocd_session"); cookieErr == nil {
			principal, err = s.app.AuthenticateBrowserSessionToken(r.Context(), cookie.Value)
			source = auth.CredentialSourceCookie
		} else {
			writeError(w, auth.ErrUnauthenticated)
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		ctx := auth.WithCredentialSource(auth.WithPrincipal(r.Context(), principal), source)
		if csrfRequired(r) && source == auth.CredentialSourceCookie && (r.Header.Get("X-NeroCD-CSRF") != "1" || !s.validMutationOrigin(r)) {
			writeError(w, auth.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func (s *Server) authenticateBrowser(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("nerocd_session")
		if err != nil {
			writeError(w, auth.ErrUnauthenticated)
			return
		}
		principal, err := s.app.AuthenticateBrowserSessionToken(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, err)
			return
		}
		if csrfRequired(r) && (r.Header.Get("X-NeroCD-CSRF") != "1" || !s.validMutationOrigin(r)) {
			writeError(w, auth.ErrForbidden)
			return
		}
		ctx := auth.WithCredentialSource(auth.WithPrincipal(r.Context(), principal), auth.CredentialSourceCookie)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func csrfRequired(r *http.Request) bool {
	return r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete
}

func (s *Server) validMutationOrigin(r *http.Request) bool {
	if s.publicOrigin == "" {
		return true
	}
	return strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/") == s.publicOrigin
}

func (s *Server) authenticateRunner(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, auth.ErrUnauthenticated)
			return
		}
		principal, err := s.app.AuthenticateRunnerToken(r.Context(), token)
		if err != nil {
			writeError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	}
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

func paginated[T any](r *http.Request, items []T) map[string]any {
	total := len(items)
	limit := parseNonNegativeInt(r.URL.Query().Get("limit"), total)
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	offset := parseNonNegativeInt(r.URL.Query().Get("offset"), 0)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	if limit == 0 {
		end = offset
	}
	page := items[offset:end]
	return map[string]any{"items": page, "limit": limit, "offset": offset, "count": len(page), "total": total}
}

func pageFromRequest(r *http.Request) store.Page {
	return store.Page{
		Limit:   parseNonNegativeInt(r.URL.Query().Get("limit"), 0),
		Offset:  parseNonNegativeInt(r.URL.Query().Get("offset"), 0),
		Enabled: r.URL.Query().Has("limit") || r.URL.Query().Has("offset"),
	}
}

func paginatedResult[T any](result store.PageResult[T]) map[string]any {
	items := result.Items
	if items == nil {
		items = []T{}
	}
	return map[string]any{"items": items, "limit": result.Limit, "offset": result.Offset, "count": len(items), "total": result.Total}
}

func parseNonNegativeInt(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func decodeBody(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := decodeJSONDocument(io.LimitReader(r.Body, maxJSONBodyBytes+1), value); err != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return false
	}
	return true
}

// decodeJSONDocument is the only typed JSON decoder used by API handlers.
// The outer boundary owns media type and byte framing; this layer owns strict
// request schemas and exact one-document decoding, including nested typed
// objects. Fields intentionally declared as maps remain free-form by design.
func decodeJSONDocument(body io.Reader, value any) error {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON documents")
		}
		return err
	}
	return nil
}

func requestID(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); safeRequestID.MatchString(value) {
		return value
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err == nil {
		return "req_" + hex.EncodeToString(buf)
	}
	return "req_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		status = http.StatusUnauthorized
		code = "unauthenticated"
	case errors.Is(err, auth.ErrForbidden):
		status = http.StatusForbidden
		code = "forbidden"
	case errors.Is(err, auth.ErrRateLimited):
		status = http.StatusTooManyRequests
		code = "rate_limited"
	case errors.Is(err, auth.ErrInvalidCredentials):
		status = http.StatusUnauthorized
		code = "invalid_credentials"
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
		code = "not_found"
	case errors.Is(err, store.ErrConflict):
		status = http.StatusConflict
		code = "conflict"
	case strings.Contains(err.Error(), "required"), strings.Contains(err.Error(), "invalid"):
		status = http.StatusBadRequest
		code = "bad_request"
	}
	message := map[string]string{
		"unauthenticated":     "authentication is required",
		"forbidden":           "access is denied",
		"rate_limited":        "request rate is limited",
		"invalid_credentials": "invalid credentials",
		"not_found":           "resource not found",
		"conflict":            "request conflicts with current state",
		"bad_request":         "invalid request",
		"internal_error":      "internal server error",
	}[code]
	writeErrorEnvelope(w, status, code, message)
}

func writeErrorEnvelope(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]string{"code": code, "error": message})
}

func spaFileServer(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(strings.ToLower(r.URL.Path), ".map") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(static, path); err != nil {
			r.URL.Path = "/"
			w.Header().Set("Cache-Control", "no-cache")
		} else if isHashedStaticAsset(path) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func isHashedStaticAsset(path string) bool {
	if !strings.HasPrefix(path, "assets/") {
		return false
	}
	name := path[strings.LastIndex(path, "/")+1:]
	dot := strings.LastIndex(name, ".")
	const viteHashLength = 8
	if dot <= viteHashLength || name[dot-viteHashLength-1] != '-' {
		return false
	}
	for _, character := range name[dot-viteHashLength : dot] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(value []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(value)
}
