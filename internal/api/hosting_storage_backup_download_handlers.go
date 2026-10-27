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

const storageDownloadStreamPath = "/api/v1/runners/hosting/storage-backup-download/stream"

func storageDownloadRunnerPath(path string) bool {
	return path == storageDownloadStreamPath || path == "/api/v1/runners/hosting/storage-backup-download/plan"
}
func storageDownloadPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/storage-backups/") && (strings.HasSuffix(path, "/archive") || strings.HasSuffix(path, "/archive-options")) || storageDownloadRunnerPath(path)
}

type storageDownloadSession struct {
	actor        string
	plan         domain.HostingStorageBackupDownloadPlan
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
type storageDownloadBroker struct {
	mu       sync.Mutex
	sessions map[string]*storageDownloadSession
}

func (b *storageDownloadBroker) reserve(id, actor string, ctx context.Context, cancel context.CancelFunc, check func(context.Context) error) (*storageDownloadSession, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions == nil {
		b.sessions = map[string]*storageDownloadSession{}
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
	s := &storageDownloadSession{actor: actor, ctx: ctx, cancel: cancel, reader: reader, writer: writer, ready: make(chan error, 1), done: make(chan struct{}), checkBrowser: check}
	b.sessions[id] = s
	return s, true
}
func (b *storageDownloadBroker) claim(id, hash string) (*storageDownloadSession, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	if s == nil || s.claimed || s.plan.Hash != hash || s.ctx.Err() != nil {
		return nil, false
	}
	s.claimed = true
	return s, true
}
func (b *storageDownloadBroker) present(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	return s != nil && s.ctx.Err() == nil
}
func (b *storageDownloadBroker) matches(id, hash string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	return s != nil && s.ctx.Err() == nil && s.plan.Hash == hash
}
func (b *storageDownloadBroker) setPlan(id string, p domain.HostingStorageBackupDownloadPlan) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions[id].plan = p
}
func (b *storageDownloadBroker) remove(id string, success bool) {
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

func storageDownloadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeErrorEnvelope(w, 504, "download_timeout", "archive download deadline exceeded")
	case errors.Is(err, store.ErrStorageDownloadUnavailable):
		writeErrorEnvelope(w, 503, "download_unavailable", "archive download unavailable")
	case errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrUnauthenticated):
		writeErrorEnvelope(w, 403, "forbidden", "archive download authority denied")
	default:
		writeError(w, err)
	}
}
func (s *Server) downloadBrowserAuthority(r *http.Request) error {
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
	_, err = s.app.StorageBackupDownloadOptions(r.Context(), r.PathValue("id"))
	return err
}

func (s *Server) storageBackupDownloadOptions(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	ctx, stop := context.WithTimeout(r.Context(), 5*time.Second)
	defer stop()
	out, err := s.app.StorageBackupDownloadOptions(ctx, r.PathValue("id"))
	if err != nil {
		storageDownloadError(w, err)
		return
	}
	if !s.storageBackupDownloadEnabled {
		out.Available = false
		out.ReasonCode = "disabled"
		out.Blob = nil
	}
	writeJSON(w, 200, out)
}
func (s *Server) storageBackupDownload(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	if err := s.downloadBrowserAuthority(r); err != nil {
		storageDownloadError(w, err)
		return
	}
	if !s.storageBackupDownloadEnabled {
		storageDownloadError(w, store.ErrStorageDownloadUnavailable)
		return
	}
	actor, err := s.app.CurrentPrincipal(r.Context())
	if err != nil {
		storageDownloadError(w, err)
		return
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		storageDownloadError(w, store.ErrStorageDownloadUnavailable)
		return
	}
	id := "download_" + hex.EncodeToString(random[:])
	ctx, cancel := context.WithTimeout(r.Context(), 300*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	session, ok := s.storageDownloads.reserve(id, actor.ID, ctx, cancel, func(check context.Context) error {
		bounded, stop := context.WithTimeout(r.Context(), 5*time.Second)
		defer stop()
		detach := context.AfterFunc(check, stop)
		defer detach()
		return s.downloadBrowserAuthority(r.WithContext(bounded))
	})
	if !ok {
		writeErrorEnvelope(w, 429, "download_capacity", "archive download capacity exhausted")
		return
	}
	success := false
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = s.app.CloseStorageBackupDownload(cleanup, id, success)
		s.storageDownloads.remove(id, success)
	}()
	metadata, stopMetadata := context.WithTimeout(ctx, 5*time.Second)
	p, err := s.app.CreateStorageBackupDownload(metadata, id, r.PathValue("id"), time.Now().UTC().Add(300*time.Second))
	stopMetadata()
	if err != nil {
		storageDownloadError(w, err)
		return
	}
	s.storageDownloads.setPlan(id, p)
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(300 * time.Second)
	if err = controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		storageDownloadError(w, store.ErrStorageDownloadUnavailable)
		return
	}
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
		storageDownloadError(w, err)
		return
	}
	if err = session.checkBrowser(ctx); err != nil {
		storageDownloadError(w, err)
		return
	}
	// Read before attachment headers so a small malformed stream can fail cleanly.
	first := make([]byte, 32<<10)
	n, readErr := session.reader.Read(first)
	if readErr != nil && readErr != io.EOF {
		storageDownloadError(w, store.ErrConflict)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", `attachment; filename="`+p.BackupID+`.tar"`)
	w.Header().Set("Content-Length", strconv.FormatInt(p.Blob.SizeBytes, 10))
	w.Header().Set("Cache-Control", "no-store")
	if n > 0 {
		if _, err = w.Write(first[:n]); err != nil {
			return
		}
	}
	remaining, err := io.Copy(w, session.reader)
	success = err == nil && int64(n)+remaining == p.Blob.SizeBytes
}
func (s *Server) runnerStorageBackupDownloadPlan(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	var a domain.HostingAuthority
	if !decodeStorageArchive(w, r, &a) {
		return
	}
	if !s.storageBackupDownloadEnabled {
		storageDownloadError(w, store.ErrStorageDownloadUnavailable)
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
	if !s.storageDownloads.present(a.OperationID) {
		storageDownloadError(w, store.ErrStorageDownloadUnavailable)
		return
	}
	p, err := s.app.RunnerStorageBackupDownloadPlan(r.Context(), a)
	if err != nil {
		storageDownloadError(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) runnerStorageBackupDownloadStream(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeErrorEnvelope(w, 400, "bad_request", "download stream accepts no query")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		writeErrorEnvelope(w, 400, "invalid_request", "multipart stream required")
		return
	}
	if !s.storageBackupDownloadEnabled || !s.runtimeEnvironmentTransport(r) {
		storageDownloadError(w, store.ErrStorageDownloadUnavailable)
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
	r.Body = http.MaxBytesReader(w, r.Body, domain.HostingStorageArchiveMaxBytes+(64<<10))
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
	var in domain.HostingStorageBackupDownloadStream
	if err != nil || len(raw) > 4096 || domain.DecodeHostingStorageArchiveJSON(raw, &in) != nil || in.Validate() != nil {
		writeErrorEnvelope(w, 400, "bad_request", "invalid stream metadata")
		return
	}
	if !s.storageDownloads.matches(in.OperationID, in.PlanHash) {
		storageDownloadError(w, store.ErrStorageDownloadUnavailable)
		return
	}
	bounded, stopBegin := context.WithTimeout(r.Context(), 5*time.Second)
	p, err := s.app.BeginStorageBackupDownload(bounded, in)
	stopBegin()
	if err != nil {
		storageDownloadError(w, err)
		return
	}
	session, ok := s.storageDownloads.claim(in.OperationID, in.PlanHash)
	if !ok {
		storageDownloadError(w, store.ErrStorageDownloadUnavailable)
		return
	}
	fail := func(e error) {
		select {
		case session.ready <- e:
		default:
		}
		_ = session.writer.CloseWithError(e)
	}
	if in.FailureCode == "archive_unavailable" {
		if _, e := parts.NextPart(); e != io.EOF {
			fail(store.ErrConflict)
			writeErrorEnvelope(w, 400, "bad_request", "failure metadata cannot contain an archive")
			return
		}
		if e := session.checkBrowser(r.Context()); e != nil {
			fail(e)
			storageDownloadError(w, e)
			return
		}
		fail(store.ErrNotFound)
		writeJSON(w, 200, map[string]string{"session_id": p.SessionID, "plan_hash": p.Hash})
		return
	}
	part, err := parts.NextPart()
	if err != nil || part.FormName() != "archive" || part.FileName() != "archive.tar" {
		fail(store.ErrConflict)
		writeErrorEnvelope(w, 400, "bad_request", "one archive part required")
		return
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
		current, err := s.app.RunnerStorageBackupDownloadPlan(bounded, in.HostingAuthority)
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
				_ = r.Body.Close()
				fail(ctx.Err())
				return
			case <-session.ctx.Done():
				_ = r.Body.Close()
				fail(session.ctx.Err())
				return
			case <-ticker.C:
				if e := before(); e != nil {
					cancel()
					_ = r.Body.Close()
					fail(e)
					return
				}
			}
		}
	}()
	var stopWatcher sync.Once
	stopWatching := func() { stopWatcher.Do(func() { close(stopped); <-done }) }
	defer func() { cancel(); stopWatching() }()
	if err = before(); err != nil {
		fail(err)
		storageDownloadError(w, err)
		return
	}
	select {
	case session.ready <- nil:
	default:
		return
	}
	err = copyVerifiedStorageDownload(session.writer, part, p.Blob, func() error {
		if _, e := parts.NextPart(); e != io.EOF {
			return store.ErrConflict
		}
		return before()
	})
	if err != nil {
		fail(err)
		storageDownloadError(w, err)
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
	storageDownloadError(w, store.ErrConflict)
}

// Withhold the last block until exact size, digest, framing and current authority agree.
func copyVerifiedStorageDownload(dst io.Writer, src io.Reader, blob domain.HostingStorageArchiveBlob, finish func() error) error {
	if blob.Validate() != nil || finish == nil {
		return store.ErrConflict
	}
	hash := sha256.New()
	remaining := blob.SizeBytes
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
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != blob.SHA256 {
		return store.ErrConflict
	}
	if err := finish(); err != nil {
		return err
	}
	_, err := dst.Write(tail)
	return err
}
