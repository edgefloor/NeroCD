package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type databaseImportUploadSession struct {
	actor   string
	plan    domain.HostingDatabaseImportStagePlan
	reader  *io.PipeReader
	writer  *io.PipeWriter
	armed   chan struct{}
	done    chan error
	claimed bool
}
type databaseImportUploadBroker struct {
	mu       sync.Mutex
	sessions map[string]*databaseImportUploadSession
}

func (b *databaseImportUploadBroker) reserve(actor string, p domain.HostingDatabaseImportStagePlan) (*databaseImportUploadSession, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions == nil {
		b.sessions = make(map[string]*databaseImportUploadSession)
	}
	if _, exists := b.sessions[p.Stage.ID]; exists {
		return nil, false
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
	s := &databaseImportUploadSession{actor: actor, plan: p, reader: reader, writer: writer, armed: make(chan struct{}), done: make(chan error, 1)}
	b.sessions[p.Stage.ID] = s
	return s, true
}
func (b *databaseImportUploadBroker) claim(id, hash string) (*databaseImportUploadSession, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	if s == nil || s.claimed || s.plan.Hash != hash {
		return nil, false
	}
	s.claimed = true
	return s, true
}
func (b *databaseImportUploadBroker) remove(id string) {
	b.mu.Lock()
	s := b.sessions[id]
	delete(b.sessions, id)
	b.mu.Unlock()
	if s != nil {
		_ = s.reader.CloseWithError(context.Canceled)
		_ = s.writer.CloseWithError(context.Canceled)
	}
}

func (s *Server) requestDatabaseImportUploadStage(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseImportUploadInput
	if !decodeDatabaseImport(w, r, &in) {
		return
	}
	out, err := s.app.RequestDatabaseImportUploadStage(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusAccepted, out, err)
}

// uploadBrowserAuthority reauthenticates the cookie/token as well as target authority.
func (s *Server) uploadBrowserAuthority(r *http.Request, p domain.HostingDatabaseImportStagePlan) error {
	ctx, stop := context.WithTimeout(r.Context(), 5*time.Second)
	defer stop()
	var principal auth.Principal
	var err error
	if token, ok := bearerToken(r.Header.Get("Authorization")); ok {
		principal, err = s.app.AuthenticateSessionToken(ctx, token)
	} else if cookie, e := r.Cookie("nerocd_session"); e == nil {
		principal, err = s.app.AuthenticateBrowserSessionToken(ctx, cookie.Value)
	} else {
		return auth.ErrForbidden
	}
	if err != nil || principal.ID != p.ActorID {
		return auth.ErrForbidden
	}
	return s.app.RevalidateDatabaseImportUpload(ctx, p)
}

// watchImportUpload closes blocked IO on cancellation, expiry or lost authority.
// The handler must stop and join the watcher before returning.
func watchImportUpload(ctx context.Context, closeIO func(error), check func() error) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				closeIO(ctx.Err())
				return
			case <-ticker.C:
				if err := check(); err != nil {
					closeIO(err)
					return
				}
			}
		}
	}()
	return func() { close(stop); <-done }
}
func uploadDeadlines(w http.ResponseWriter, deadline time.Time) error {
	c := http.NewResponseController(w)
	for _, err := range []error{c.SetReadDeadline(deadline), c.SetWriteDeadline(deadline)} {
		if err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
	}
	return nil
}
func (s *Server) uploadDatabaseImportStageContent(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/octet-stream" {
		writeErrorEnvelope(w, 400, "invalid_request", "one binary body required")
		return
	}
	match := r.Header.Get("If-Match")
	if len(match) != 73 || !strings.HasPrefix(match, `"sha256:`) || !strings.HasSuffix(match, `"`) {
		writeErrorEnvelope(w, 400, "invalid_request", "strong upload hash required")
		return
	}
	hash := match[1 : len(match)-1]
	p, view, attempted, err := s.app.DatabaseImportUpload(r.Context(), r.PathValue("id"), hash, false)
	if errors.Is(err, store.ErrDatabaseImportUploadHash) {
		writeErrorEnvelope(w, 412, "upload_hash_mismatch", "upload hash mismatch")
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if attempted {
		writeJSON(w, 409, view)
		return
	}
	actor, err := s.app.CurrentPrincipal(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	session, ok := s.databaseImportUploads.reserve(actor.ID, p)
	if !ok {
		writeErrorEnvelope(w, 429, "upload_capacity", "upload capacity exhausted")
		return
	}
	defer s.databaseImportUploads.remove(p.Stage.ID)
	_, view, attempted, err = s.app.DatabaseImportUpload(r.Context(), p.Stage.ID, hash, true)
	if err != nil {
		writeError(w, err)
		return
	}
	if attempted {
		writeJSON(w, 409, view)
		return
	}
	close(session.armed)
	ctx, cancel := context.WithDeadline(r.Context(), *p.Stage.UploadDeadline)
	defer cancel()
	if err = uploadDeadlines(w, *p.Stage.UploadDeadline); err != nil {
		writeError(w, store.ErrConflict)
		return
	}
	fail := func(err error) {
		cancel()
		_ = http.NewResponseController(w).SetReadDeadline(time.Now())
		_ = session.writer.CloseWithError(err)
		_ = session.reader.CloseWithError(err)
		_ = r.Body.Close()
	}
	stop := watchImportUpload(ctx, fail, func() error { return s.uploadBrowserAuthority(r.WithContext(ctx), p) })
	defer stop()
	err = copyImportUpload(session.writer, r.Body, p.Stage.UploadSource.Bytes)
	if err == nil {
		err = s.uploadBrowserAuthority(r.WithContext(ctx), p)
	}
	if err != nil {
		fail(err)
		writeError(w, store.ErrConflict)
		return
	}
	_ = session.writer.Close()
	select {
	case err = <-session.done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		writeError(w, store.ErrConflict)
		return
	}
	view, err = s.app.GetDatabaseImportStage(r.Context(), p.Stage.ID)
	proxyResponse(w, 202, view, err)
}

// copyImportUpload withholds the final block until exact encoded EOF is established.
func copyImportUpload(dst io.Writer, src io.Reader, bytes int64) error {
	if bytes < 1 || bytes > domain.HostingDatabaseImportMaxBytes {
		return store.ErrConflict
	}
	buffer := make([]byte, 32<<10)
	tail := make([]byte, 0, 32<<10)
	for bytes > 0 {
		size := min(int64(len(buffer)), bytes)
		n, err := io.ReadFull(src, buffer[:size])
		if err != nil {
			return err
		}
		if len(tail) > 0 {
			if _, err = dst.Write(tail); err != nil {
				return err
			}
		}
		tail = append(tail[:0], buffer[:n]...)
		bytes -= int64(n)
	}
	var extra [1]byte
	if n, err := src.Read(extra[:]); n != 0 || err != io.EOF {
		return store.ErrConflict
	}
	_, err := dst.Write(tail)
	return err
}

func (s *Server) runnerDatabaseImportUploadStream(w http.ResponseWriter, r *http.Request) {
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.HostingDatabaseImportUploadStream
	if !decodeDatabaseImport(w, r, &in) {
		return
	}
	if in.Validate() != nil {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid upload authority")
		return
	}
	if !s.runtimeEnvironmentTransport(r) {
		writeError(w, store.ErrConflict)
		return
	}
	var ok bool
	r, ok = s.databaseConnectionRunnerRequest(w, r)
	if !ok {
		return
	}
	p, err := s.app.RunnerDatabaseImportStagePlan(r.Context(), in.HostingAuthority)
	if err != nil {
		writeError(w, err)
		return
	}
	if p.Stage.Version != domain.HostingDatabaseImportUploadVersion || p.Hash != in.PlanHash {
		writeError(w, store.ErrConflict)
		return
	}
	session, ok := s.databaseImportUploads.claim(p.Stage.ID, p.Hash)
	if !ok {
		writeError(w, store.ErrConflict)
		return
	}
	var result error
	defer func() { _ = session.reader.CloseWithError(result); session.done <- result }()
	ctx, cancel := context.WithDeadline(r.Context(), *p.Stage.UploadDeadline)
	defer cancel()
	select {
	case <-session.armed:
	case <-ctx.Done():
		result = ctx.Err()
		writeError(w, store.ErrConflict)
		return
	}
	if result = uploadDeadlines(w, *p.Stage.UploadDeadline); result != nil {
		writeError(w, store.ErrConflict)
		return
	}
	check := func() error {
		bounded, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		token, _ := bearerToken(r.Header.Get("Authorization"))
		if _, err := s.app.AuthenticateRunnerToken(bounded, token); err != nil {
			return err
		}
		current, err := s.app.RunnerDatabaseImportStagePlan(bounded, in.HostingAuthority)
		if err != nil {
			return err
		}
		if current.Hash != p.Hash {
			return store.ErrConflict
		}
		return nil
	}
	stop := watchImportUpload(ctx, func(err error) {
		cancel()
		_ = session.reader.CloseWithError(err)
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now())
	}, check)
	defer stop()
	if result = check(); result != nil {
		writeError(w, result)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, result = io.Copy(w, session.reader)
}
