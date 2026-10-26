package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/netip"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/notificationtransport"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
	"nerocd/web"
)

const backupNotificationMessageLimit = 64 << 10

// Receipts stay in memory. Evidence contains only closed proof names and IDs,
// never SMTP credentials, recipient addresses, raw email or webhook bodies.
type backupNotificationReceipt struct {
	Transport  string
	Body       []byte
	Recipients []string
	Accepted   bool
}
type backupNotificationReceivers struct {
	mu                                            sync.Mutex
	receipts                                      []backupNotificationReceipt
	reject                                        map[string]int
	smtpOrigin, webhookOrigin, username, password string
	roots                                         *x509.CertPool
	certificate                                   tls.Certificate
	spki                                          string
}

func backupNotificationCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	// Chromium does not advertise Ed25519 in its TLS signature schemes.
	// Use P-256 for both certificates so the browser can negotiate before
	// the controlled CA or exact SPKI identity is checked.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("notification fixture CA key failed")
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "NeroCD disposable notification fixture"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		t.Fatal("notification fixture CA failed")
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal("notification fixture CA parse failed")
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("notification fixture leaf key failed")
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, parsed, leafKey.Public(), key)
	if err != nil {
		t.Fatal("notification fixture leaf failed")
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{leafDER, der}, PrivateKey: leafKey}, roots
}

func (r *backupNotificationReceivers) record(transport string, body []byte, recipients []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.receipts) >= 256 {
		return false
	}
	accepted := r.reject[transport] == 0
	if r.reject[transport] > 0 {
		r.reject[transport]--
	}
	r.receipts = append(r.receipts, backupNotificationReceipt{transport, bytes.Clone(body), append([]string(nil), recipients...), accepted})
	return accepted
}
func (r *backupNotificationReceivers) snapshot() []backupNotificationReceipt {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]backupNotificationReceipt, len(r.receipts))
	for i, v := range r.receipts {
		out[i] = v
		out[i].Body = bytes.Clone(v.Body)
		out[i].Recipients = append([]string(nil), v.Recipients...)
	}
	return out
}
func (r *backupNotificationReceivers) failNext(transport string, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reject[transport] = count
}

// Both listeners are inside the disposable Linux fixture. Starting this helper
// requires the separately authorized live profile, including the generated CA.
func startBackupNotificationReceivers(t *testing.T) *backupNotificationReceivers {
	t.Helper()
	certificate, roots := backupNotificationCertificate(t)
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal("notification fixture leaf parse failed")
	}
	pin := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	r := &backupNotificationReceivers{certificate: certificate, spki: base64.StdEncoding.EncodeToString(pin[:]), roots: roots, reject: map[string]int{}, username: databaseRemoteRandom(t), password: databaseRemoteRandom(t)}
	webhook := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/backup-events" || req.URL.RawQuery != "" || req.TLS == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(req.Body, backupNotificationMessageLimit+1))
		defer clear(raw)
		if err != nil || len(raw) > backupNotificationMessageLimit || !json.Valid(raw) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !r.record("webhook", raw, nil) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	webhook.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	webhook.Config.ReadHeaderTimeout = 5 * time.Second
	webhook.Config.ReadTimeout = 5 * time.Second
	webhook.Config.WriteTimeout = 5 * time.Second
	webhook.StartTLS()
	t.Cleanup(webhook.Close)
	r.webhookOrigin = webhook.URL
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
	if err != nil {
		t.Fatal("notification SMTP listener failed")
	}
	r.smtpOrigin = "smtp+tls://" + listener.Addr().String()
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			r.serveSMTP(conn)
		}
	}()
	t.Cleanup(func() {
		if listener.Close() != nil {
			t.Error("notification SMTP close failed")
		}
		select {
		case <-joined:
		case <-time.After(6 * time.Second):
			t.Error("notification SMTP did not join")
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, v := range r.receipts {
			clear(v.Body)
		}
	})
	return r
}

func backupNotificationSMTPAuth(argument, username, password string) bool {
	method, encoded, ok := strings.Cut(argument, " ")
	if !ok || method != "PLAIN" {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	defer clear(raw)
	if err != nil || len(raw) > 1024 {
		return false
	}
	expected := []byte("\x00" + username + "\x00" + password)
	defer clear(expected)
	return subtle.ConstantTimeCompare(raw, expected) == 1
}
func backupNotificationSMTPAddress(argument string) (string, bool) {
	_, value, ok := strings.Cut(argument, ":")
	if !ok || !strings.HasPrefix(value, "<") || !strings.HasSuffix(value, ">") {
		return "", false
	}
	value = strings.TrimSuffix(strings.TrimPrefix(value, "<"), ">")
	address, err := mail.ParseAddress(value)
	return value, err == nil && address.Address == value && !strings.ContainsAny(value, "\r\n")
}
func (r *backupNotificationReceivers) serveSMTP(conn net.Conn) {
	defer conn.Close()
	if conn.SetDeadline(time.Now().Add(5*time.Second)) != nil {
		return
	}
	reader := textproto.NewReader(bufio.NewReader(io.LimitReader(conn, backupNotificationMessageLimit+4096)))
	writer := bufio.NewWriter(conn)
	reply := func(value string) bool {
		_, err := writer.WriteString(value + "\r\n")
		return err == nil && writer.Flush() == nil
	}
	if !reply("220 notification fixture ESMTP") {
		return
	}
	authenticated := false
	sender := false
	var recipients []string
	for i := 0; i < 32; i++ {
		line, err := reader.ReadLine()
		if err != nil || len(line) > 1024 {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch verb {
		case "EHLO":
			if !reply("250-fixture\r\n250-AUTH PLAIN\r\n250 SIZE 65536") {
				return
			}
		case "AUTH":
			authenticated = backupNotificationSMTPAuth(arg, r.username, r.password)
			if !authenticated {
				reply("535 authentication failed")
				return
			}
			if !reply("235 authenticated") {
				return
			}
		case "MAIL":
			_, sender = backupNotificationSMTPAddress(arg)
			if !authenticated || !sender {
				reply("530 authentication required")
				return
			}
			recipients = nil
			if !reply("250 sender accepted") {
				return
			}
		case "RCPT":
			address, ok := backupNotificationSMTPAddress(arg)
			if !sender || !ok || len(recipients) >= 8 {
				reply("550 recipient refused")
				return
			}
			recipients = append(recipients, address)
			if !reply("250 recipient accepted") {
				return
			}
		case "DATA":
			if !sender || len(recipients) == 0 {
				reply("503 envelope required")
				return
			}
			if !reply("354 send message") {
				return
			}
			raw, err := io.ReadAll(io.LimitReader(reader.DotReader(), backupNotificationMessageLimit+1))
			if err != nil || len(raw) > backupNotificationMessageLimit {
				clear(raw)
				return
			}
			accepted := r.record("email", raw, recipients)
			clear(raw)
			if accepted {
				if !reply("250 queued") {
					return
				}
			} else {
				if !reply("451 temporary fixture failure") {
					return
				}
			}
			sender = false
			recipients = nil
		case "QUIT":
			reply("221 goodbye")
			return
		default:
			reply("502 command refused")
			return
		}
	}
}

func TestBackupNotificationFixtureCertificateAndEnvelope(t *testing.T) {
	certificate, roots := backupNotificationCertificate(t)
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(certificate.Certificate[1])
	if err != nil {
		t.Fatal("fixture CA certificate parse failed")
	}
	for _, cert := range []*x509.Certificate{ca, leaf} {
		key, ok := cert.PublicKey.(*ecdsa.PublicKey)
		if !ok || key.Curve != elliptic.P256() || cert.SignatureAlgorithm != x509.ECDSAWithSHA256 {
			t.Fatal("fixture certificate algorithm is not the Chromium-compatible P-256 chain")
		}
	}
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1"}); err != nil {
		t.Fatal("fixture leaf not trusted for exact loopback")
	}
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "localhost"}); err == nil {
		t.Fatal("fixture leaf accepted ungranted name")
	}
	valid := base64.StdEncoding.EncodeToString([]byte("\x00user\x00private"))
	if !backupNotificationSMTPAuth("PLAIN "+valid, "user", "private") {
		t.Fatal("valid SMTP auth refused")
	}
	for _, arg := range []string{"PLAIN " + valid, "LOGIN " + valid, "PLAIN invalid"} {
		if backupNotificationSMTPAuth(arg, "other", "private") {
			t.Fatal("invalid SMTP auth accepted")
		}
	}
	for _, arg := range []string{"TO:<owner@example.local>", "FROM:<fixture@example.local>"} {
		if _, ok := backupNotificationSMTPAddress(arg); !ok {
			t.Fatal("valid envelope refused")
		}
	}
	for _, arg := range []string{"TO:owner@example.local", "TO:<owner@example.local>\r\nRCPT TO:<other@example.local>", "TO:<not an address>"} {
		if _, ok := backupNotificationSMTPAddress(arg); ok {
			t.Fatal("invalid envelope accepted")
		}
	}
}

type backupNotificationChannel struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Generation int64    `json:"generation"`
	Enabled    bool     `json:"enabled"`
	Events     []string `json:"events"`
}
type backupNotificationDelivery struct {
	ID                 string `json:"id"`
	EventID            string `json:"event_id"`
	ChannelID          string `json:"channel_id"`
	Event              string `json:"event"`
	SourceKind         string `json:"source_kind"`
	SourceID           string `json:"source_id"`
	SourceState        string `json:"source_state"`
	State              string `json:"state"`
	FailureCode        string `json:"failure_code"`
	LocalCopyAvailable bool   `json:"local_copy_available"`
	RecoveryURL        string `json:"recovery_url"`
	AttemptCount       int    `json:"attempt_count"`
	MaxAttempts        int    `json:"max_attempts"`
	Attempts           []struct {
		Number      int    `json:"number"`
		State       string `json:"state"`
		FailureCode string `json:"failure_code"`
	} `json:"attempts"`
}

func notificationRuntimePath(project string) string {
	return "/api/v1/projects/" + project + "/notification-deliveries"
}
func notificationRuntimeChannelPath(project string) string {
	return "/api/v1/projects/" + project + "/notification-channels"
}
func notificationRuntimePoll(f *databaseRunnerFixture, description string, check func() bool) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 120*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-ctx.Done():
			f.t.Fatal("notification fixture did not settle: " + description)
		case <-ticker.C:
		}
	}
}
func notificationRuntimeDelivery(f *databaseRunnerFixture, project, id, state string) backupNotificationDelivery {
	f.t.Helper()
	var result backupNotificationDelivery
	notificationRuntimePoll(f, "delivery "+state, func() bool {
		result = databaseRunnerJSON[backupNotificationDelivery](f, f.token, "GET", notificationRuntimePath(project)+"/"+id, nil, 200)
		return result.State == state
	})
	return result
}
func notificationRuntimeSource(f *databaseRunnerFixture, project, source, event string, channels []backupNotificationChannel) []backupNotificationDelivery {
	f.t.Helper()
	var selected []backupNotificationDelivery
	notificationRuntimePoll(f, "actual "+event, func() bool {
		values := databaseRunnerJSON[[]backupNotificationDelivery](f, f.token, "GET", notificationRuntimePath(project), nil, 200)
		selected = nil
		for _, v := range values {
			if v.SourceID == source && v.Event == event {
				selected = append(selected, v)
			}
		}
		if len(selected) != len(channels) {
			return false
		}
		for _, v := range selected {
			if v.State != "delivered" {
				return false
			}
		}
		return true
	})
	byChannel := map[string]bool{}
	eventID := ""
	for _, v := range selected {
		if byChannel[v.ChannelID] || v.ID == "" || v.EventID == "" || v.MaxAttempts != 5 || v.AttemptCount < 1 {
			f.t.Fatal("source event delivery identities repeated or incomplete")
		}
		byChannel[v.ChannelID] = true
		if eventID != "" && eventID != v.EventID {
			f.t.Fatal("one source result created multiple event identities")
		}
		eventID = v.EventID
		expected := false
		for _, c := range channels {
			expected = expected || c.ID == v.ChannelID
		}
		if !expected {
			f.t.Fatal("unexpected source channel")
		}
	}
	return selected
}
func notificationRuntimeVerifyReceipts(f *databaseRunnerFixture, receivers *backupNotificationReceivers, deliveries []backupNotificationDelivery, channels []backupNotificationChannel, source domain.HostingDatabase, backup string, local bool, failure string) string {
	f.t.Helper()
	receipts := receivers.snapshot()
	actualLink := ""
	for _, d := range deliveries {
		kind := ""
		for _, c := range channels {
			if c.ID == d.ChannelID {
				kind = c.Type
			}
		}
		u, err := url.Parse(d.RecoveryURL)
		if err != nil || u.IsAbs() || u.Path != "/projects" || u.Query().Get("project_id") != source.Resource.ProjectID || u.Query().Get("environment_id") != source.Resource.ProjectEnvironmentID || u.Query().Get("resource_id") != source.Resource.ID || u.Query().Get("backup_id") != backup || d.LocalCopyAvailable != local {
			f.t.Fatal("source recovery locator or local-copy outcome mismatch")
		}
		if d.Event == "upload_failed" && u.Query().Get("transfer_id") != d.SourceID {
			f.t.Fatal("upload recovery locator lost transfer scope")
		}
		expectedLink := strings.Replace(os.Getenv("NEROCD_DATABASE_CONNECTION_BROWSER_ORIGIN"), "http://", "https://", 1) + d.RecoveryURL
		matched := 0
		for _, r := range receipts {
			if r.Transport != kind || !r.Accepted {
				continue
			}
			if kind == "webhook" {
				var payload struct {
					EventID            string `json:"event_id"`
					DeliveryID         string `json:"delivery_id"`
					Event              string `json:"event"`
					SourceKind         string `json:"source_kind"`
					SourceID           string `json:"source_id"`
					SourceState        string `json:"source_state"`
					RecoveryURL        string `json:"recovery_url"`
					FailureCode        string `json:"failure_code"`
					LocalCopyAvailable bool   `json:"local_copy_available"`
				}
				if json.Unmarshal(r.Body, &payload) != nil {
					f.t.Fatal("actual webhook payload invalid")
				}
				if payload.DeliveryID != d.ID {
					continue
				}
				if payload.EventID != d.EventID || payload.Event != d.Event || payload.SourceKind != d.SourceKind || payload.SourceID != d.SourceID || payload.SourceState != d.SourceState || payload.RecoveryURL != expectedLink || payload.FailureCode != failure || payload.LocalCopyAvailable != d.LocalCopyAvailable {
					f.t.Fatal("actual webhook and durable source differ")
				}
				if actualLink != "" && actualLink != payload.RecoveryURL {
					f.t.Fatal("transports disagree on absolute recovery link")
				}
				actualLink = payload.RecoveryURL
				matched++
			} else {
				message, err := mail.ReadMessage(bytes.NewReader(r.Body))
				if err != nil {
					f.t.Fatal("actual SMTP message invalid")
				}
				if message.Header.Get("Message-ID") != "<"+d.ID+"@nerocd.invalid>" {
					continue
				}
				body, err := io.ReadAll(message.Body)
				if err != nil {
					f.t.Fatal("actual SMTP body unavailable")
				}
				for _, required := range []string{"Event: " + d.Event, "Event ID: " + d.EventID, "Delivery ID: " + d.ID, "Source: " + d.SourceKind + "/" + d.SourceID, "Source state: " + d.SourceState, "Failure code: " + failure, expectedLink} {
					if !bytes.Contains(body, []byte(required)) {
						f.t.Fatal("actual SMTP source identity/outcome/link missing")
					}
				}
				if len(r.Recipients) != 1 || r.Recipients[0] != "admin@example.local" {
					f.t.Fatal("SMTP went to an unexpected recipient")
				}
				matched++
				clear(body)
			}
		}
		if matched != 1 {
			f.t.Fatal("actual successful transport receipt missing or repeated")
		}
	}
	if actualLink == "" {
		f.t.Fatal("actual absolute recovery link missing")
	}
	return actualLink
}

type backupNotificationBrowser struct {
	f                                     *databaseRunnerFixture
	mu                                    sync.Mutex
	phase, backup, recovery, failedBackup string
	configured, observed, done            chan struct{}
	restored                              chan domain.HostingDatabaseBackupOperation
}

func (b *backupNotificationBrowser) set(phase, backup, recovery string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.phase, b.backup, b.recovery = phase, backup, recovery
}
func (b *backupNotificationBrowser) await(ch <-chan struct{}, name string) {
	b.f.t.Helper()
	select {
	case <-ch:
	case <-time.After(120 * time.Second):
		b.f.t.Fatal("notification browser missing " + name)
	}
}
func notificationRuntimeBrowserOwner(api *Server, project, origin string, next http.Handler) http.Handler {
	return api.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := api.app.CurrentPrincipal(r.Context())
		role, roleErr := api.app.ProjectRole(r.Context(), project)
		if err != nil || roleErr != nil || principal.ID != "usr_bootstrap" || role.Role != "owner" || r.Header.Get("Origin") != origin || r.Host != strings.TrimPrefix(origin, "https://") || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func startBackupNotificationBrowser(f *databaseRunnerFixture, source domain.HostingDatabase, receivers *backupNotificationReceivers) *backupNotificationBrowser {
	f.t.Helper()
	origin := strings.Replace(os.Getenv("NEROCD_DATABASE_CONNECTION_BROWSER_ORIGIN"), "http://", "https://", 1)
	if !applicationDatabaseBrowserOrigin(strings.Replace(origin, "https://", "http://", 1)) {
		f.t.Fatal("owned notification browser origin required")
	}
	if _, err := f.service.BootstrapAdmin(f.t.Context(), app.BootstrapAdminInput{Email: "notification-bootstrap@example.local", Name: "Notification bootstrap", Password: "notification-bootstrap-private-only"}); err != nil {
		f.t.Fatal("notification browser bootstrap failed")
	}
	for _, member := range []app.ProjectMemberInput{{ProjectID: source.Resource.ProjectID, Email: "admin@example.local", Role: "owner"}, {ProjectID: source.Resource.ProjectID, Email: "viewer@example.local", Role: "viewer"}} {
		databaseRunnerJSON[domain.ProjectMember](f, f.token, "POST", "/api/v1/project-members", member, 200)
	}
	changed, err := f.database.Exec(f.t.Context(), `UPDATE users SET global_role='user' WHERE id='usr_bootstrap' AND global_role='system_admin'`)
	if err != nil || changed.RowsAffected() != 1 {
		f.t.Fatal("notification browser owner demotion failed")
	}
	api := NewServerWithConfig(f.service, slog.New(slog.NewTextHandler(f.logs, nil)), web.Static(), ServerConfig{PublicOrigin: origin})
	b := &backupNotificationBrowser{f: f, phase: "configure", configured: make(chan struct{}), observed: make(chan struct{}), done: make(chan struct{}), restored: make(chan domain.HostingDatabaseBackupOperation, 1)}
	control := "/api/v1/fixture/database-backup/" + databaseRemoteRandom(f.t)[:32]
	smtpURL, err := url.Parse(receivers.smtpOrigin)
	if err != nil {
		f.t.Fatal("SMTP fixture URL invalid")
	}
	port, err := strconv.Atoi(smtpURL.Port())
	if err != nil {
		f.t.Fatal("SMTP fixture port invalid")
	}
	owner := notificationRuntimeBrowserOwner(api, source.Resource.ProjectID, origin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "POST" && r.URL.Path == control+"/configuration" {
			writeJSON(w, 200, map[string]any{"smtp_host": "127.0.0.1", "smtp_port": port, "smtp_security": "tls", "smtp_username": receivers.username, "smtp_password": receivers.password, "from_address": "fixture@example.local", "recipient_email": "admin@example.local", "webhook_url": receivers.webhookOrigin + "/backup-events"})
			return
		}
		if r.Method == "POST" && r.URL.Path == control+"/state" {
			b.mu.Lock()
			defer b.mu.Unlock()
			writeJSON(w, 200, map[string]string{"phase": b.phase, "backup_id": b.backup, "recovery_url": b.recovery, "failed_backup_id": b.failedBackup})
			return
		}
		if r.Method == "POST" {
			var target chan struct{}
			switch r.URL.Path {
			case control + "/configured":
				target = b.configured
			case control + "/observed":
				target = b.observed
			case control + "/done":
				target = b.done
			}
			if target != nil {
				select {
				case <-target:
					w.WriteHeader(409)
				default:
					close(target)
					w.WriteHeader(204)
				}
				return
			}
		}
		w.WriteHeader(404)
	}))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, control) {
			owner.ServeHTTP(w, r)
			return
		}
		if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/v1/database-backups/") && strings.HasSuffix(r.URL.Path, "/restores") {
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, r)
			if rec.Code == 202 {
				var op domain.HostingDatabaseBackupOperation
				if json.Unmarshal(rec.Body.Bytes(), &op) != nil || op.Mode != "restore" {
					f.t.Error("notification recovery restore admission invalid")
				} else {
					select {
					case b.restored <- op:
					default:
						f.t.Error("notification recovery admitted more than one restore")
					}
				}
			}
			databaseRemoteCopyResponse(w, rec)
			return
		}
		api.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp", "0.0.0.0:18080")
	if err != nil {
		f.t.Fatal("notification browser bridge failed")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second}
	joined := make(chan error, 1)
	server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{receivers.certificate}}
	go func() { joined <- server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(ctx) != nil {
			_ = server.Close()
			f.t.Error("notification browser did not stop gracefully")
		}
		select {
		case err := <-joined:
			if err != http.ErrServerClosed {
				f.t.Error("notification browser exit failed")
			}
		case <-ctx.Done():
			f.t.Error("notification browser did not join")
		}
	})
	meta := map[string]string{"origin": origin, "project_id": source.Resource.ProjectID, "environment": "database runner acceptance", "engine": "postgresql", "profile": "backup_notifications", "tls_spki": receivers.spki, "application_resource_id": source.Resource.ID, "application_name": source.Resource.Name, "database_resource_id": source.Resource.ID, "foreign_resource_id": "unused_fixture", "control_path": control}
	raw, _ := json.Marshal(meta)
	fmt.Println("DATABASE_BACKUP_BROWSER_READY " + string(raw))
	return b
}

func notificationRuntimeTest(f *databaseRunnerFixture, project string, c backupNotificationChannel) backupNotificationDelivery {
	return databaseRunnerJSON[backupNotificationDelivery](f, f.token, "POST", notificationRuntimeChannelPath(project)+"/"+c.ID+"/tests", map[string]any{"request_id": backupRuntimeRequestID(f.t), "expected_generation": c.Generation}, 202)
}
func notificationRuntimeSave(f *databaseRunnerFixture, project string, c backupNotificationChannel, configuration map[string]any) backupNotificationChannel {
	method, path := "PUT", notificationRuntimeChannelPath(project)+"/"+c.ID
	if c.ID == "" {
		method, path = "POST", notificationRuntimeChannelPath(project)
	}
	body := map[string]any{"request_id": backupRuntimeRequestID(f.t), "expected_generation": c.Generation, "name": c.Name, "type": c.Type, "enabled": c.Enabled, "events": []string{"backup_failed", "upload_failed"}}
	if configuration != nil {
		body["configuration"] = configuration
	}
	return databaseRunnerJSON[backupNotificationChannel](f, f.token, method, path, body, 200)
}
func notificationRuntimeRetryAndAuthority(f *databaseRunnerFixture, project string, r *backupNotificationReceivers) {
	f.t.Helper()
	// This event is explicitly a test. It cannot satisfy either real source proof.
	extra := notificationRuntimeSave(f, project, backupNotificationChannel{Name: "Runtime retry qualification", Type: "webhook", Enabled: true}, map[string]any{"webhook_url": r.webhookOrigin + "/backup-events"})
	r.failNext("webhook", 5)
	exhausted := notificationRuntimeTest(f, project, extra)
	exhausted = notificationRuntimeDelivery(f, project, exhausted.ID, "failed")
	if exhausted.Event != "test" || exhausted.AttemptCount != 5 || len(exhausted.Attempts) != 5 || exhausted.MaxAttempts != 5 {
		f.t.Fatal("persisted transport exhaustion missing")
	}
	for i, a := range exhausted.Attempts {
		if a.Number != i+1 || a.State != "failed" || a.FailureCode == "" {
			f.t.Fatal("attempt audit incomplete")
		}
	}
	backupRuntimeRestartController(f)
	persisted := databaseRunnerJSON[backupNotificationDelivery](f, f.token, "GET", notificationRuntimePath(project)+"/"+exhausted.ID, nil, 200)
	if persisted.State != "failed" || persisted.AttemptCount != 5 || len(persisted.Attempts) != 5 {
		f.t.Fatal("restart lost exhaustion")
	}
	fmt.Println("DATABASE_BACKUP_NOTIFICATIONS_PASS postgresql retry_exhaustion")
	for _, kind := range []string{"disabled", "changed"} {
		r.failNext("webhook", 1)
		delivery := notificationRuntimeTest(f, project, extra)
		notificationRuntimePoll(f, "first rejected "+kind, func() bool {
			v := databaseRunnerJSON[backupNotificationDelivery](f, f.token, "GET", notificationRuntimePath(project)+"/"+delivery.ID, nil, 200)
			return v.AttemptCount == 1 && v.State == "pending"
		})
		if kind == "disabled" {
			extra.Enabled = false
		} else {
			extra.Name = "Changed pending notification"
		}
		extra = notificationRuntimeSave(f, project, extra, nil)
		suppressed := notificationRuntimeDelivery(f, project, delivery.ID, "suppressed")
		if suppressed.AttemptCount != 1 {
			f.t.Fatal("obsolete channel sent a second attempt")
		}
		if kind == "disabled" {
			extra.Enabled = true
			extra = notificationRuntimeSave(f, project, extra, nil)
		}
	}
	extra.Enabled = false
	notificationRuntimeSave(f, project, extra, nil)
	// Remove only this disposable schema's viewer after a rejected first attempt.
	emailURL, err := url.Parse(r.smtpOrigin)
	if err != nil {
		f.t.Fatal("SMTP origin unavailable")
	}
	port, _ := strconv.Atoi(emailURL.Port())
	recipient := notificationRuntimeSave(f, project, backupNotificationChannel{Name: "Runtime removed recipient", Type: "email", Enabled: true}, map[string]any{"smtp_host": "127.0.0.1", "smtp_port": port, "smtp_security": "tls", "smtp_username": r.username, "smtp_password": r.password, "from_address": "fixture@example.local", "recipient_user_ids": []string{"usr_viewer"}})
	r.failNext("email", 1)
	delivery := notificationRuntimeTest(f, project, recipient)
	notificationRuntimePoll(f, "recipient first rejection", func() bool {
		v := databaseRunnerJSON[backupNotificationDelivery](f, f.token, "GET", notificationRuntimePath(project)+"/"+delivery.ID, nil, 200)
		return v.AttemptCount == 1 && v.State == "pending"
	})
	changed, err := f.database.Exec(f.t.Context(), `DELETE FROM project_members WHERE project_id=$1 AND user_id='usr_viewer'`, project)
	if err != nil || changed.RowsAffected() != 1 {
		f.t.Fatal("fixture recipient removal failed")
	}
	suppressed := notificationRuntimeDelivery(f, project, delivery.ID, "suppressed")
	if suppressed.AttemptCount > 2 {
		f.t.Fatal("removed recipient kept retrying")
	}
	recipient.Enabled = false
	notificationRuntimeSave(f, project, recipient, nil)
	databaseRunnerJSON[domain.ProjectMember](f, f.token, "POST", "/api/v1/project-members", app.ProjectMemberInput{ProjectID: project, Email: "viewer@example.local", Role: "viewer"}, 200)
}

// Docker's daemon events establish that the actual pinned command ran and
// exited nonzero. A failed API state alone cannot prove a pg_dump invocation.
func notificationRuntimeFailedDumpExec(raw []byte, container string) bool {
	if len(raw) > 1<<20 {
		return false
	}
	type event struct {
		Action string
		Actor  struct {
			ID         string
			Attributes map[string]string
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	created := map[string]bool{}
	failed := map[string]bool{}
	for i := 0; ; i++ {
		var value event
		err := dec.Decode(&value)
		if err == io.EOF {
			break
		}
		if err != nil || i >= 128 || value.Actor.ID != container {
			return false
		}
		id := value.Actor.Attributes["execID"]
		if !backupRuntimeValidExecID(id) {
			return false
		}
		if strings.HasPrefix(value.Action, "exec_create: ") {
			command := strings.Join(strings.Fields(strings.TrimPrefix(value.Action, "exec_create: ")), " ")
			if command == "timeout --signal=TERM --kill-after=5s 10s pg_dump --username=postgres --dbname=app --format=custom --no-owner --no-acl" {
				created[id] = true
			}
		}
		if value.Action == "exec_die" {
			code, err := strconv.Atoi(value.Actor.Attributes["exitCode"])
			if err != nil {
				return false
			}
			failed[id] = code > 0 && code < 256
		}
	}
	count := 0
	for id := range created {
		if failed[id] {
			count++
		}
	}
	return count == 1
}

func notificationRuntimeDumpFailure(f *databaseRunnerFixture, source domain.HostingDatabase, deployed domain.HostingOperation) domain.HostingDatabaseBackupOperation {
	f.t.Helper()
	container := deployed.Result.ContainerID
	if !backupRuntimeValidExecID(container) {
		f.t.Fatal("exact owned source container missing")
	}
	// Read and preserve the original setting before changing only the owned app DB.
	original := strings.TrimSpace(string(f.docker("exec", "--user", "postgres", container, "psql", "--username=postgres", "--dbname=postgres", "--no-psqlrc", "--tuples-only", "--no-align", "--command", "SELECT datallowconn FROM pg_database WHERE datname='app';")))
	if original != "t" {
		f.t.Fatal("source app database must initially allow connections")
	}
	var once sync.Once
	restore := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			raw, err := databaseRunnerDocker(ctx, nil, "exec", "--user", "postgres", container, "psql", "--username=postgres", "--dbname=postgres", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--command", "ALTER DATABASE app ALLOW_CONNECTIONS true;")
			clear(raw)
			if err != nil {
				f.t.Error("owned source connection setting restore failed")
			}
		})
	}
	f.t.Cleanup(restore)
	defer restore()
	started := time.Now().UTC().Format(time.RFC3339Nano)
	f.docker("exec", "--user", "postgres", container, "psql", "--username=postgres", "--dbname=postgres", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--command", "ALTER DATABASE app ALLOW_CONNECTIONS false;")
	wire := &databaseBackupRuntimeWire{mode: "backup", pause: true, entered: make(chan struct{}), release: make(chan struct{})}
	f.backupWire.Store(wire)
	var release sync.Once
	defer release.Do(func() { close(wire.release) })
	requested := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "POST", "/api/v1/resources/"+source.Resource.ID+"/database-backups", domain.HostingDatabaseBackupInput{RequestID: backupRuntimeRequestID(f.t), ExpectedResourceGeneration: source.Resource.Generation}, 202)
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		f.t.Fatal("real source dump plan not reached")
	}
	release.Do(func() { close(wire.release) })
	notificationRuntimePoll(f, "actual dump failure", func() bool {
		requested = databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "GET", "/api/v1/database-backups/"+requested.ID, nil, 200)
		return requested.State == "failed" || requested.State == "uncertain"
	})
	f.backupWire.Store(nil)
	events := f.docker("events", "--since", started, "--until", time.Now().UTC().Format(time.RFC3339Nano), "--filter", "container="+container, "--filter", "event=exec_create", "--filter", "event=exec_die", "--format", "{{json .}}")
	if !notificationRuntimeFailedDumpExec(events, container) {
		clear(events)
		f.t.Fatal("no actual pinned pg_dump invocation with a failed exit was observed")
	}
	clear(events)
	if requested.Blob != nil || requested.FailureCode == "" {
		f.t.Fatal("failed dump falsely published a local archive")
	}
	restore()
	return requested
}

func TestHostingBackupNotificationsCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_BACKUP_NOTIFICATIONS_RUNTIME") != "1" || os.Getenv("NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME") != "1" || os.Getenv("NEROCD_DATABASE_BACKUP_RUNTIME") != "1" || os.Getenv("NEROCD_DATABASE_CONNECTION_BROWSER") != "1" || os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact separately authorized notification runtime gate")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	receivers := startBackupNotificationReceivers(t)
	sender, err := notificationtransport.New(notificationtransport.Policy{PrivateOrigins: map[string][]netip.Prefix{}, FixtureTLSOrigins: []string{receivers.webhookOrigin, receivers.smtpOrigin}, RootCAs: receivers.roots})
	if err != nil {
		t.Fatal("exact controlled TLS notification policy failed")
	}
	f := newDatabaseRunnerFixtureConfigured(t, true, true, func(pg *store.PostgresStore, ring *secretcrypto.Keyring) error {
		return pg.ConfigureBackupNotifications(ring, sender, strings.Replace(os.Getenv("NEROCD_DATABASE_CONNECTION_BROWSER_ORIGIN"), "http://", "https://", 1))
	}, true)
	f.protected = append(f.protected, []byte(receivers.username), []byte(receivers.password))
	diagnosticID := ""
	defer f.reportDatabaseBackupFailureIfFailed(&diagnosticID)
	silo := startDatabaseRemoteSilo(f)
	source := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", backupRuntimeCreateInput(t, f.environment, f.destination), 201)
	f.registerRouteDatabaseCleanup(source.Resource.ID)
	deployed := f.deploy(source, "notification_source")
	f.registerRouteOperationCleanup(deployed.ID)
	deployed = f.await(deployed.ID)
	application := backupRuntimeApplication(f, source, "notification_source")
	f.appRecord(application, "PUT", "before", "notification_snapshot_origin")
	target := databaseRunnerJSON[struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}](f, f.token, "POST", "/api/v1/projects/"+source.Resource.ProjectID+"/database-backup-targets", map[string]any{"request_id": backupRuntimeRequestID(t), "expected_revision": 0, "name": "Runtime notification S3", "endpoint": "http://127.0.0.1:9000", "bucket": "nerocd-runtime", "region": "us-east-1", "enabled": true, "credentials": map[string]string{"access_key": silo.access, "secret_key": silo.secret}}, 200)
	validation := databaseRunnerJSON[databaseRemoteTransfer](f, f.token, "POST", "/api/v1/projects/"+source.Resource.ProjectID+"/database-backup-targets/"+target.ID+"/validations", map[string]any{"request_id": backupRuntimeRequestID(t), "expected_revision": target.Revision, "destination_id": f.destination}, 202)
	databaseRemoteAwaitValidation(f, validation)
	browser := startBackupNotificationBrowser(f, source, receivers)
	browser.await(browser.configured, "both channels configured through UI")
	channels := databaseRunnerJSON[[]backupNotificationChannel](f, f.token, "GET", notificationRuntimeChannelPath(source.Resource.ProjectID), nil, 200)
	if len(channels) != 2 {
		t.Fatal("browser did not configure exactly two notification channels")
	}
	types := map[string]bool{}
	for _, c := range channels {
		if !c.Enabled || c.Generation != 1 || types[c.Type] {
			t.Fatal("browser notification channel contract mismatch")
		}
		types[c.Type] = true
	}
	if !types["email"] || !types["webhook"] {
		t.Fatal("both real transports required")
	}
	// Explicit test sends are separately verified and cannot prove source alerts.
	notificationRuntimePoll(f, "explicit test deliveries", func() bool {
		values := databaseRunnerJSON[[]backupNotificationDelivery](f, f.token, "GET", notificationRuntimePath(source.Resource.ProjectID), nil, 200)
		count := 0
		for _, v := range values {
			if v.Event == "test" && v.State == "delivered" {
				count++
			}
		}
		return count == 2
	})
	databaseRunnerJSON[domain.ProjectMember](f, f.token, "POST", "/api/v1/project-members", app.ProjectMemberInput{ProjectID: source.Resource.ProjectID, Email: "viewer@example.local", Role: "maintainer"}, 200)
	actor := createTestSessionFor(t, NewServer(f.service, slog.New(slog.NewTextHandler(f.logs, nil)), web.Static()), "viewer@example.local", "viewer")
	f.protected = append(f.protected, []byte(actor))
	receivers.failNext("webhook", 1)
	receivers.failNext("email", 1)
	f.stop()
	backup := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, actor, "POST", "/api/v1/resources/"+source.Resource.ID+"/database-backups", domain.HostingDatabaseBackupInput{RequestID: backupRuntimeRequestID(t), ExpectedResourceGeneration: source.Resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}}, 202)
	diagnosticID = backup.ID
	wire := &databaseRemoteBackupWire{backup: backup.ID, phase: "failed", entered: make(chan struct{}), release: make(chan struct{})}
	f.remoteBackupWire.Store(wire)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(wire.release) }) })
	f.start()
	backup = backupRuntimeAwait(f, backup.ID)
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		t.Fatal("actual denied upload committed ACK loss not reached")
	}
	failed := databaseRemoteAwait(f, backup.ID, "upload", "failed")
	if failed.FailureCode != "access_denied" || failed.Blob == nil || *failed.Blob != *backup.Blob {
		t.Fatal("S3 denial did not retain exact successful local dump")
	}
	// Both actual source alerts have a rejected attempt before removing the
	// source actor. Their retries must authorize current recipients, regardless
	// of the actor's lost membership, and keep their original event identity.
	notificationRuntimePoll(f, "both rejected actual alerts", func() bool {
		values := databaseRunnerJSON[[]backupNotificationDelivery](f, f.token, "GET", notificationRuntimePath(source.Resource.ProjectID), nil, 200)
		count := 0
		for _, v := range values {
			if v.SourceID == failed.ID && v.Event == "upload_failed" && v.AttemptCount == 1 && v.State == "pending" {
				count++
			}
		}
		return count == 2
	})
	changed, err := f.database.Exec(t.Context(), `DELETE FROM project_members WHERE project_id=$1 AND user_id='usr_viewer'`, source.Resource.ProjectID)
	if err != nil || changed.RowsAffected() != 1 {
		t.Fatal("source actor revocation failed")
	}
	f.stop()
	release.Do(func() { close(wire.release) })
	backupRuntimeRestartController(f)
	f.start()
	notificationRuntimePoll(f, "failed source receipt replay", func() bool { wire.mu.Lock(); defer wire.mu.Unlock(); return wire.replays == 1 })
	f.remoteBackupWire.Store(nil)
	deliveries := notificationRuntimeSource(f, source.Resource.ProjectID, failed.ID, "upload_failed", channels)
	recoveryLink := notificationRuntimeVerifyReceipts(f, receivers, deliveries, channels, source, backup.ID, true, failed.FailureCode)
	retried := false
	for _, d := range deliveries {
		if d.AttemptCount == 2 {
			retried = true
		}
	}
	if !retried {
		t.Fatal("actual transient receiver failure did not retry")
	}
	cas := filepath.Join(f.root, "state", "logical-backups", strings.TrimPrefix(backup.Blob.Digest, "sha256:"))
	raw, err := os.ReadFile(cas)
	if err != nil || int64(len(raw)) != backup.Blob.Bytes {
		t.Fatal("local archive missing after denied upload")
	}
	digest := sha256.Sum256(raw)
	clear(raw)
	if "sha256:"+hex.EncodeToString(digest[:]) != backup.Blob.Digest {
		t.Fatal("retained local archive digest changed")
	}
	fmt.Println("DATABASE_BACKUP_NOTIFICATIONS_PASS postgresql upload_email_webhook")
	before := databaseRunnerJSON[[]backupNotificationDelivery](f, f.token, "GET", notificationRuntimePath(source.Resource.ProjectID), nil, 200)
	backupRuntimeRestartController(f)
	after := databaseRunnerJSON[[]backupNotificationDelivery](f, f.token, "GET", notificationRuntimePath(source.Resource.ProjectID), nil, 200)
	if len(before) != len(after) {
		t.Fatal("controller restart duplicated source events or deliveries")
	}
	identities := map[string]string{}
	for _, value := range before {
		identities[value.ID] = value.EventID
	}
	for _, value := range after {
		if identities[value.ID] != value.EventID {
			t.Fatal("controller restart changed durable delivery identity")
		}
	}
	fmt.Println("DATABASE_BACKUP_NOTIFICATIONS_PASS postgresql source_replay_restart")
	databaseRunnerJSON[domain.ProjectMember](f, f.token, "POST", "/api/v1/project-members", app.ProjectMemberInput{ProjectID: source.Resource.ProjectID, Email: "viewer@example.local", Role: "viewer"}, 200)
	browser.set("recovery", backup.ID, recoveryLink)
	browser.await(browser.observed, "current-scope recovery without automatic action")
	var restored domain.HostingDatabaseBackupOperation
	select {
	case restored = <-browser.restored:
	case <-time.After(120 * time.Second):
		t.Fatal("explicit browser local restore missing")
	}
	diagnosticID = restored.ID
	f.registerRouteDatabaseCleanup(restored.TargetResourceID)
	restored = backupRuntimeAwait(f, restored.ID)
	fresh := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "GET", "/api/v1/resources/"+restored.TargetResourceID+"/database", nil, 200)
	if fresh.Resource.ID == source.Resource.ID || fresh.Binding.ID == source.Binding.ID || fresh.StorageRef == source.StorageRef {
		t.Fatal("local recovery reused source resources")
	}
	distinct := backupRuntimeApplication(f, fresh, "notification_restored")
	f.appRecord(distinct, "GET", "before", "notification_snapshot_origin")
	f.appRecord(distinct, "PUT", "new", "notification_new_write")
	f.appRecord(distinct, "GET", "new", "notification_new_write")
	f.appRecord(application, "GET", "before", "notification_snapshot_origin")
	fmt.Println("DATABASE_BACKUP_NOTIFICATIONS_PASS postgresql local_restore_app")
	notificationRuntimeRetryAndAuthority(f, source.Resource.ProjectID, receivers)
	dump := notificationRuntimeDumpFailure(f, source, deployed)
	diagnosticID = dump.ID
	dumpDeliveries := notificationRuntimeSource(f, source.Resource.ProjectID, dump.ID, "backup_failed", channels)
	notificationRuntimeVerifyReceipts(f, receivers, dumpDeliveries, channels, source, dump.ID, false, dump.FailureCode)
	for _, d := range dumpDeliveries {
		if d.SourceState != dump.State {
			t.Fatal("dump notification misrepresented uncertain outcome")
		}
	}
	fmt.Println("DATABASE_BACKUP_NOTIFICATIONS_PASS postgresql dump_email_webhook")
	fmt.Println("DATABASE_BACKUP_NOTIFICATIONS_PASS postgresql current_authority")
	browser.mu.Lock()
	browser.failedBackup = dump.ID
	browser.mu.Unlock()
	browser.set("complete", backup.ID, recoveryLink)
	browser.await(browser.done, "final actual event history")
	f.assertRedacted()
}

func TestBackupNotificationSMTPReceiverRejectsThenAcceptsWithoutSockets(t *testing.T) {
	r := &backupNotificationReceivers{username: "fixture", password: "private", reject: map[string]int{"email": 1}}
	for attempt := 0; attempt < 2; attempt++ {
		server, client := net.Pipe()
		joined := make(chan struct{})
		t.Cleanup(func() {
			_ = client.Close()
			_ = server.Close()
			select {
			case <-joined:
			case <-time.After(2 * time.Second):
				t.Error("in-memory SMTP cleanup did not join")
			}
		})
		go func() { defer close(joined); r.serveSMTP(server) }()
		if err := client.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		wire := textproto.NewConn(client)
		command := func(code int, format string, args ...any) {
			t.Helper()
			id, err := wire.Cmd(format, args...)
			if err != nil {
				t.Fatal("in-memory SMTP command failed")
			}
			wire.StartResponse(id)
			_, _, err = wire.ReadResponse(code)
			wire.EndResponse(id)
			if err != nil {
				t.Fatal("in-memory SMTP response failed")
			}
		}
		if _, _, err := wire.ReadResponse(220); err != nil {
			t.Fatal("in-memory SMTP greeting failed")
		}
		command(250, "EHLO localhost")
		command(235, "AUTH PLAIN %s", base64.StdEncoding.EncodeToString([]byte("\x00fixture\x00private")))
		command(250, "MAIL FROM:<fixture@example.local>")
		command(250, "RCPT TO:<owner@example.local>")
		command(354, "DATA")
		body := []byte("From: fixture@example.local\r\nTo: owner@example.local\r\nMessage-ID: <stable@nerocd.invalid>\r\n\r\nStable source result\r\n")
		data := wire.DotWriter()
		_, writeErr := data.Write(body)
		closeErr := data.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("in-memory SMTP data failed")
		}
		code := 250
		if attempt == 0 {
			code = 451
		}
		if _, _, err := wire.ReadResponse(code); err != nil {
			t.Fatal("in-memory SMTP outcome mismatch")
		}
		command(221, "QUIT")
		_ = wire.Close()
		select {
		case <-joined:
		case <-time.After(2 * time.Second):
			t.Fatal("in-memory SMTP did not join")
		}
	}
	receipts := r.snapshot()
	if len(receipts) != 2 || receipts[0].Accepted || !receipts[1].Accepted || !bytes.Equal(receipts[0].Body, receipts[1].Body) {
		t.Fatal("SMTP retry did not preserve the same real message")
	}
}

func TestBackupNotificationFailedDumpRequiresMatchingDaemonExec(t *testing.T) {
	container := strings.Repeat("a", 64)
	id := strings.Repeat("b", 64)
	create := func(action, owner, exec, code string) string {
		raw, _ := json.Marshal(map[string]any{"Action": action, "Actor": map[string]any{"ID": owner, "Attributes": map[string]string{"execID": exec, "exitCode": code}}})
		return string(raw) + "\n"
	}
	started := create("exec_create: timeout --signal=TERM --kill-after=5s 10s pg_dump --username=postgres --dbname=app --format=custom --no-owner --no-acl", container, id, "")
	terminal := create("exec_die", container, id, "1")
	if !notificationRuntimeFailedDumpExec([]byte(started+terminal), container) {
		t.Fatal("actual failed dump exec rejected")
	}
	for _, invalid := range []string{started, terminal, started + create("exec_die", container, id, "0"), started + create("exec_die", container, strings.Repeat("c", 64), "1"), started + create("exec_die", strings.Repeat("d", 64), id, "1"), strings.Replace(started, "pg_dump", "pg_isready", 1) + terminal, started + terminal + "invalid"} {
		if notificationRuntimeFailedDumpExec([]byte(invalid), container) {
			t.Fatal("unproven dump invocation accepted")
		}
	}
}
