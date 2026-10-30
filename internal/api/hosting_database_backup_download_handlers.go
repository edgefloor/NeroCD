package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

const databaseDownloadStreamPath = "/api/v1/runners/hosting/database-backup-download/stream"

func databaseDownloadRunnerPath(path string) bool {
	return path == databaseDownloadStreamPath || path == "/api/v1/runners/hosting/database-backup-download/plan"
}
func databaseDownloadPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/database-backups/") && (strings.HasSuffix(path, "/archive") || strings.HasSuffix(path, "/archive-options")) || databaseDownloadRunnerPath(path)
}

type databaseDownloadSession struct {
	actor        string
	plan         domain.HostingDatabaseBackupDownloadPlan
	ctx          context.Context
	cancel       context.CancelFunc
	reader       *io.PipeReader
	writer       *io.PipeWriter
	ready        chan error
	done         chan struct{}
	checkBrowser func(context.Context) error
	claimed      bool
	success      bool
}
type databaseDownloadBroker struct {
	mu       sync.Mutex
	sessions map[string]*databaseDownloadSession
}

func (b *databaseDownloadBroker) reserve(id, actor string, ctx context.Context, cancel context.CancelFunc, check func(context.Context) error) (*databaseDownloadSession, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions == nil {
		b.sessions = map[string]*databaseDownloadSession{}
	}
	if len(b.sessions) >= 2 {
		return nil, false
	}
	for _, s := range b.sessions {
		if s.actor == actor {
			return nil, false
		}
	}
	reader, writer := io.Pipe()
	s := &databaseDownloadSession{actor: actor, ctx: ctx, cancel: cancel, reader: reader, writer: writer, ready: make(chan error, 1), done: make(chan struct{}), checkBrowser: check}
	b.sessions[id] = s
	return s, true
}
func (b *databaseDownloadBroker) claim(id, hash string) (*databaseDownloadSession, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	if s == nil || s.claimed || s.plan.Hash != hash || s.ctx.Err() != nil {
		return nil, false
	}
	s.claimed = true
	return s, true
}
func (b *databaseDownloadBroker) present(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	return s != nil && s.ctx.Err() == nil
}
func (b *databaseDownloadBroker) matches(id, hash string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	return s != nil && s.ctx.Err() == nil && s.plan.Hash == hash
}
func (b *databaseDownloadBroker) setPlan(id string, p domain.HostingDatabaseBackupDownloadPlan) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions[id].plan = p
}
func (b *databaseDownloadBroker) remove(id string, success bool) {
	b.mu.Lock()
	s := b.sessions[id]
	if s != nil {
		delete(b.sessions, id)
		s.success = success
		close(s.done)
	}
	b.mu.Unlock()
	if s != nil {
		s.cancel()
		_ = s.reader.CloseWithError(context.Canceled)
		_ = s.writer.CloseWithError(context.Canceled)
	}
}

func databaseDownloadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeErrorEnvelope(w, 504, "download_timeout", "archive download deadline exceeded")
	case errors.Is(err, store.ErrDatabaseDownloadUnavailable):
		writeErrorEnvelope(w, 503, "download_unavailable", "archive download unavailable")
	case errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrUnauthenticated):
		writeErrorEnvelope(w, 403, "forbidden", "archive download authority denied")
	default:
		writeError(w, err)
	}
}
func (s *Server) databaseDownloadBrowserAuthority(r *http.Request) error {
	ctx, stop := context.WithTimeout(r.Context(), 5*time.Second)
	defer stop()
	r = r.WithContext(ctx)
	original, err := s.app.CurrentPrincipal(r.Context())
	if err != nil {
		return auth.ErrForbidden
	}
	var current auth.Principal
	if token, ok := bearerToken(r.Header.Get("Authorization")); ok {
		current, err = s.app.AuthenticateSessionToken(r.Context(), token)
	} else if cookie, e := r.Cookie("nerocd_session"); e == nil {
		current, err = s.app.AuthenticateBrowserSessionToken(r.Context(), cookie.Value)
	} else {
		return auth.ErrForbidden
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || current.ID != original.ID {
		return auth.ErrForbidden
	}
	_, err = s.app.DatabaseBackupDownloadOptions(r.Context(), r.PathValue("id"))
	return err
}

func (s *Server) databaseBackupDownloadOptions(w http.ResponseWriter, r *http.Request) {
	if !databaseDownloadParameters(w, r) {
		return
	}
	ctx, stop := context.WithTimeout(r.Context(), 5*time.Second)
	defer stop()
	out, err := s.app.DatabaseBackupDownloadOptions(ctx, r.PathValue("id"))
	if err != nil {
		databaseDownloadError(w, err)
		return
	}
	if !s.databaseBackupDownloadEnabled {
		out.Available = false
		out.ReasonCode = "disabled"
		out.Blob = nil
	}
	writeJSON(w, 200, out)
}
func (s *Server) databaseBackupDownload(w http.ResponseWriter, r *http.Request) {
	if !databaseDownloadParameters(w, r) {
		return
	}
	if err := s.databaseDownloadBrowserAuthority(r); err != nil {
		databaseDownloadError(w, err)
		return
	}
	if !s.databaseBackupDownloadEnabled {
		databaseDownloadError(w, store.ErrDatabaseDownloadUnavailable)
		return
	}
	actor, err := s.app.CurrentPrincipal(r.Context())
	if err != nil {
		databaseDownloadError(w, err)
		return
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		databaseDownloadError(w, store.ErrDatabaseDownloadUnavailable)
		return
	}
	id := "download_" + hex.EncodeToString(random[:])
	ctx, cancel := context.WithTimeout(r.Context(), 300*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	session, ok := s.databaseDownloads.reserve(id, actor.ID, ctx, cancel, func(check context.Context) error {
		bounded, stop := context.WithTimeout(r.Context(), 5*time.Second)
		defer stop()
		detach := context.AfterFunc(check, stop)
		defer detach()
		return s.databaseDownloadBrowserAuthority(r.WithContext(bounded))
	})
	if !ok {
		writeErrorEnvelope(w, 429, "download_capacity", "archive download capacity exhausted")
		return
	}
	success := false
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = s.app.CloseDatabaseBackupDownload(cleanup, id, success)
		s.databaseDownloads.remove(id, success)
	}()
	metadata, stopMetadata := context.WithTimeout(ctx, 5*time.Second)
	p, err := s.app.CreateDatabaseBackupDownload(metadata, id, r.PathValue("id"), time.Now().UTC().Add(300*time.Second))
	stopMetadata()
	if err != nil {
		databaseDownloadError(w, err)
		return
	}
	s.databaseDownloads.setPlan(id, p)
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(300 * time.Second)
	if err = controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		databaseDownloadError(w, store.ErrDatabaseDownloadUnavailable)
		return
	}
	interrupted := make(chan struct{})
	stopInterrupt := context.AfterFunc(ctx, func() {
		_ = controller.SetWriteDeadline(time.Now())
		_ = session.reader.CloseWithError(ctx.Err())
		close(interrupted)
	})
	defer func() {
		if !stopInterrupt() {
			<-interrupted
		}
	}()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case err = <-session.ready:
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = context.DeadlineExceeded
	}
	if err != nil {
		databaseDownloadError(w, err)
		return
	}
	if err = session.checkBrowser(ctx); err != nil {
		databaseDownloadError(w, err)
		return
	}
	// Read before attachment headers so a small malformed stream can fail cleanly.
	first := make([]byte, 32<<10)
	n, readErr := session.reader.Read(first)
	if readErr != nil && readErr != io.EOF {
		databaseDownloadError(w, store.ErrConflict)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+p.BackupID+databaseDownloadExtension(p.BackupVersion)+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(p.Blob.Bytes, 10))
	w.Header().Set("Cache-Control", "no-store")
	if n > 0 {
		if _, err = w.Write(first[:n]); err != nil {
			return
		}
	}
	remaining, err := io.Copy(w, session.reader)
	success = err == nil && int64(n)+remaining == p.Blob.Bytes
}
func (s *Server) runnerDatabaseBackupDownloadPlan(w http.ResponseWriter, r *http.Request) {
	if !databaseDownloadParameters(w, r) {
		return
	}
	var a domain.HostingAuthority
	if !decodeDatabaseBackup(w, r, &a) {
		return
	}
	if !s.databaseBackupDownloadEnabled {
		databaseDownloadError(w, store.ErrDatabaseDownloadUnavailable)
		return
	}
	ctx, stop := context.WithTimeout(r.Context(), 5*time.Second)
	defer stop()
	r = r.WithContext(ctx)
	var ok bool
	r, ok = s.databaseConnectionRunnerRequest(w, r)
	if !ok {
		return
	}
	if !s.databaseDownloads.present(a.OperationID) {
		databaseDownloadError(w, store.ErrDatabaseDownloadUnavailable)
		return
	}
	p, err := s.app.RunnerDatabaseBackupDownloadPlan(r.Context(), a)
	if err != nil {
		databaseDownloadError(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) runnerDatabaseBackupDownloadStream(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeErrorEnvelope(w, 400, "bad_request", "download stream accepts no query")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		writeErrorEnvelope(w, 400, "invalid_request", "multipart stream required")
		return
	}
	if !s.databaseBackupDownloadEnabled || !s.runtimeEnvironmentTransport(r) {
		databaseDownloadError(w, store.ErrDatabaseDownloadUnavailable)
		return
	}
	var ok bool
	r, ok = s.databaseConnectionRunnerRequest(w, r)
	if !ok {
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(300 * time.Second))
	_ = controller.SetWriteDeadline(time.Now().Add(300 * time.Second))
	r.Body = http.MaxBytesReader(w, r.Body, domain.HostingDatabaseBackupMaxBytes+(64<<10))
	parts, err := r.MultipartReader()
	if err != nil {
		writeErrorEnvelope(w, 400, "bad_request", "invalid multipart stream")
		return
	}
	meta, err := parts.NextPart()
	if err != nil || meta.FormName() != "metadata" || meta.FileName() != "" {
		writeErrorEnvelope(w, 400, "bad_request", "metadata must be first")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(meta, 4097))
	var in domain.HostingDatabaseBackupDownloadStream
	if err != nil || len(raw) > 4096 || domain.DecodeHostingDatabaseBackupJSON(raw, &in) != nil || in.Validate() != nil {
		writeErrorEnvelope(w, 400, "bad_request", "invalid stream metadata")
		return
	}
	if !s.databaseDownloads.matches(in.OperationID, in.PlanHash) {
		databaseDownloadError(w, store.ErrDatabaseDownloadUnavailable)
		return
	}
	bounded, stopBegin := context.WithTimeout(r.Context(), 5*time.Second)
	p, err := s.app.BeginDatabaseBackupDownload(bounded, in)
	stopBegin()
	if err != nil {
		databaseDownloadError(w, err)
		return
	}
	session, ok := s.databaseDownloads.claim(in.OperationID, in.PlanHash)
	if !ok {
		databaseDownloadError(w, store.ErrDatabaseDownloadUnavailable)
		return
	}
	fail := func(e error) {
		select {
		case session.ready <- e:
		default:
		}
		_ = session.writer.CloseWithError(e)
		if !errors.Is(e, store.ErrNotFound) {
			session.cancel()
		}
	}
	before := func() error {
		bounded, stop := context.WithTimeout(r.Context(), 5*time.Second)
		defer stop()
		detach := context.AfterFunc(session.ctx, stop)
		defer detach()
		if session.ctx.Err() != nil {
			return session.ctx.Err()
		}
		if err := session.checkBrowser(bounded); err != nil {
			return err
		}
		token, _ := bearerToken(r.Header.Get("Authorization"))
		if _, err := s.app.AuthenticateRunnerToken(bounded, token); err != nil {
			return auth.ErrForbidden
		}
		current, err := s.app.RunnerDatabaseBackupDownloadPlan(bounded, in.HostingAuthority)
		if err != nil {
			return err
		}
		if current != p {
			return store.ErrConflict
		}
		return nil
	}
	ctx, cancel := context.WithDeadline(r.Context(), p.ExpiresAt)
	defer cancel()
	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ctx.Done():
				fail(ctx.Err())
				interruptDatabaseDownloadBody(controller, r.Body, session, ctx.Err())
				return
			case <-session.ctx.Done():
				fail(session.ctx.Err())
				interruptDatabaseDownloadBody(controller, r.Body, session, session.ctx.Err())
				return
			case <-ticker.C:
				if e := before(); e != nil {
					cancel()
					fail(e)
					interruptDatabaseDownloadBody(controller, r.Body, session, e)
					return
				}
			}
		}
	}()
	var stopWatcher sync.Once
	stopWatching := func() { stopWatcher.Do(func() { close(stopped); <-done }) }
	defer func() { cancel(); stopWatching() }()
	if in.FailureCode == "archive_unavailable" {
		if _, e := parts.NextPart(); e != io.EOF {
			fail(store.ErrConflict)
			writeErrorEnvelope(w, 400, "bad_request", "failure metadata cannot contain an archive")
			return
		}
		if e := session.checkBrowser(r.Context()); e != nil {
			fail(e)
			databaseDownloadError(w, e)
			return
		}
		fail(store.ErrNotFound)
		writeJSON(w, 200, map[string]string{"session_id": p.SessionID, "plan_hash": p.Hash})
		return
	}
	part, err := parts.NextPart()
	if err != nil || part.FormName() != "archive" || part.FileName() != "archive.bin" {
		fail(store.ErrConflict)
		writeErrorEnvelope(w, 400, "bad_request", "one archive part required")
		return
	}
	if err = before(); err != nil {
		fail(err)
		databaseDownloadError(w, err)
		return
	}
	select {
	case session.ready <- nil:
	default:
		return
	}
	err = copyVerifiedDatabaseDownload(session.writer, part, p.Blob, func() error {
		if _, e := parts.NextPart(); e != io.EOF {
			return store.ErrConflict
		}
		return before()
	})
	if err != nil {
		fail(err)
		databaseDownloadError(w, err)
		return
	}
	stopWatching()
	_ = session.writer.Close()
	select {
	case <-session.done:
		if session.success {
			writeJSON(w, 200, map[string]string{"session_id": p.SessionID, "plan_hash": p.Hash})
			return
		}
	case <-ctx.Done():
	}
	databaseDownloadError(w, store.ErrConflict)
}

// Withhold the last block until exact size, digest, framing and current authority agree.
func copyVerifiedDatabaseDownload(dst io.Writer, src io.Reader, blob domain.HostingDatabaseBackupBlob, finish func() error) error {
	if blob.Validate() != nil || finish == nil {
		return store.ErrConflict
	}
	hash := sha256.New()
	remaining := blob.Bytes
	buffer := make([]byte, 32<<10)
	tail := make([]byte, 0, 32<<10)
	for remaining > 0 {
		n := int64(len(buffer))
		if remaining < n {
			n = remaining
		}
		read, err := io.ReadFull(src, buffer[:n])
		if err != nil {
			return store.ErrConflict
		}
		_, _ = hash.Write(buffer[:read])
		remaining -= int64(read)
		if len(tail) > 0 {
			if _, err := dst.Write(tail); err != nil {
				return err
			}
		}
		tail = append(tail[:0], buffer[:read]...)
	}
	var extra [1]byte
	if n, err := src.Read(extra[:]); n != 0 || err != io.EOF {
		return store.ErrConflict
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != blob.Digest {
		return store.ErrConflict
	}
	if err := finish(); err != nil {
		return err
	}
	_, err := dst.Write(tail)
	return err
}

func databaseDownloadExtension(version string) string {
	if domain.HostingDatabaseBackupFormatFor(version) == "postgresql_custom" {
		return ".dump"
	}
	return ".sql"
}

func databaseDownloadParameters(w http.ResponseWriter, r *http.Request) bool {
	if !archiveRequestParameters(w, r, false) {
		return false
	}
	if r.Header.Get("Range") != "" {
		writeErrorEnvelope(w, 400, "bad_request", "archive ranges are unsupported")
		return false
	}
	return true
}

// Interrupt a real net/http read before Close, which can wait on the body mutex.
func interruptDatabaseDownloadBody(controller *http.ResponseController, body io.ReadCloser, session *databaseDownloadSession, err error) {
	_ = controller.SetReadDeadline(time.Now())
	_ = session.reader.CloseWithError(err)
	_ = session.writer.CloseWithError(err)
	_ = body.Close()
}
