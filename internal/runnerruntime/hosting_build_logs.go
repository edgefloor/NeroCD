package runnerruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type buildLogClient interface {
	BuildEvents(context.Context, domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error)
}
type buildOutputEngine interface {
	BuildWithOutput(context.Context, hostingbuild.Request, hostingbuild.OutputSink) (hostingbuild.Result, error)
}

// Reusable secret material stays in memory. Private credentials are added only
// to the owning attempt adapter, never to this executor configuration.
func (e *BuildExecutor) setBuildLogSecrets(token string) error {
	if len(token) > 4096 {
		return errors.New("build log secret exceeds bound")
	}
	if token != "" {
		e.logSecrets = []runner.SecretMaterial{{Value: strings.Clone(token), Encodings: []string{"base64", "base64url", "hex"}}}
	}
	return nil
}

type buildOutputAdapter struct {
	mu        sync.Mutex
	executor  *BuildExecutor
	attempt   runner.AttemptIdentity
	authority domain.HostingBuildAuthority
	secrets   []runner.SecretMaterial
	redactor  *runner.Redactor
	tails     map[string]string
	failed    bool
}

func newBuildOutputAdapter(e *BuildExecutor, a domain.HostingBuildAuthority, at runner.AttemptIdentity, attemptSecrets ...runner.SecretMaterial) (*buildOutputAdapter, error) {
	if _, ok := e.client.(buildLogClient); !ok {
		return nil, errors.New("typed build log client required")
	}
	if _, ok := e.engine.(buildOutputEngine); !ok {
		return nil, errors.New("observed build engine required")
	}
	secrets := append(append([]runner.SecretMaterial(nil), e.logSecrets...), attemptSecrets...)
	if len(secrets) > 16 {
		return nil, errors.New("too many build log secrets")
	}
	for _, secret := range secrets {
		if len(secret.Value) > 4096 || len(secret.Encodings) > 3 {
			return nil, errors.New("build log secret exceeds bound")
		}
	}
	return &buildOutputAdapter{executor: e, attempt: at, authority: a, secrets: secrets, redactor: runner.NewRedactor(secrets), tails: map[string]string{}}, nil
}

func validBuildOutput(component, stream string) bool {
	return (component == "build" || component == "buildkit" || component == "railpack") && (stream == "system" || stream == "stdout" || stream == "stderr")
}

func (s *buildOutputAdapter) Sink(ctx context.Context, out hostingbuild.Output) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return errors.New("build output delivery unresolved")
	}
	component, stream := string(out.Component), string(out.Stream)
	if !validBuildOutput(component, stream) || len(out.Data) > domain.HostingBuildLogMessageLimit {
		s.failed = true
		return errors.New("invalid raw build output")
	}
	if err := ctx.Err(); err != nil {
		s.failed = true
		return err
	}
	key := component + "/" + stream
	safe := s.redactor.RedactChunk(key, string(out.Data))
	if err := s.frame(ctx, key, safe, false); err != nil {
		s.failed = true
		return err
	}
	return nil
}

// frame retains at most three redacted bytes across calls. Invalid UTF-8 and
// NUL become U+FFFD; messages never wait for line delimiters.
func (s *buildOutputAdapter) frame(ctx context.Context, key, safe string, final bool) error {
	input := s.tails[key] + safe
	delete(s.tails, key)
	component, stream, _ := strings.Cut(key, "/")
	var frame strings.Builder
	emit := func() error {
		if frame.Len() == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.executor.checkBuildLogAuthority(ctx, s.authority, false); err != nil {
			return err
		}
		if _, err := s.executor.journal.AppendBuildLog(s.attempt, s.authority.BuildID, component, stream, frame.String()); err != nil {
			return errors.New("build log journal unavailable")
		}
		frame.Reset()
		return s.executor.drainBuildLogs(ctx, s.authority)
	}
	for len(input) > 0 {
		if !final && !utf8.FullRuneInString(input) {
			s.tails[key] = input
			break
		}
		r, n := utf8.DecodeRuneInString(input)
		if r == 0 {
			r = utf8.RuneError
		}
		if frame.Len()+utf8.RuneLen(r) > domain.HostingBuildLogMessageLimit {
			if err := emit(); err != nil {
				return err
			}
		}
		frame.WriteRune(r)
		input = input[n:]
	}
	return emit()
}

// Finish flushes only a normally finished, still-authorized operation. A failed
// or canceled sink retains the immutable intent and every pending journal entry.
func (s *buildOutputAdapter) Finish(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return errors.New("build output delivery unresolved")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, out := range s.redactor.Flush() {
		if err := s.frame(ctx, out.Stream, out.Message, false); err != nil {
			s.failed = true
			return err
		}
	}
	for _, component := range []string{"build", "buildkit", "railpack"} {
		for _, stream := range []string{"system", "stdout", "stderr"} {
			if err := s.frame(ctx, component+"/"+stream, "", true); err != nil {
				s.failed = true
				return err
			}
		}
	}
	return s.executor.drainBuildLogs(ctx, s.authority)
}

func (e *BuildExecutor) drainBuildLogs(ctx context.Context, a domain.HostingBuildAuthority) error {
	return e.replayBuildLogs(ctx, a, false)
}

func (e *BuildExecutor) replayBuildLogs(ctx context.Context, a domain.HostingBuildAuthority, historical bool) error {
	client, ok := e.client.(buildLogClient)
	if !ok {
		return errors.New("typed build log client required")
	}
	for _, entry := range e.journal.Snapshot().BuildLogs {
		if entry.Batch.Authority != a {
			continue
		}
		// Keep the exact retained batch across retries, including canonical hash,
		// event key and requested sequence. No authority is inferred from receipts.
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := e.checkBuildLogAuthority(ctx, a, historical); err != nil {
				return err
			}
			request, cancel := context.WithTimeout(ctx, e.config.RequestTimeout)
			ack, sendErr := client.BuildEvents(request, entry.Batch)
			cancel()
			if sendErr == nil {
				if hostingbuildprotocol.ValidateLogAcknowledgment(entry.Batch, ack) != nil {
					return errors.New("build log acknowledgment changed")
				}
				if err := e.journal.AckBuildLogs(entry.Batch, ack); err != nil {
					return err
				}
				break
			}
			if runnerclient.ClassifyFailure(sendErr) != runnerclient.FailureTransient {
				return errors.New("build log delivery rejected; journal retained")
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil
}

func (e *BuildExecutor) checkBuildLogAuthority(ctx context.Context, a domain.HostingBuildAuthority, historical bool) error {
	status, err := e.status(ctx, a, nil)
	if err != nil {
		return err
	}
	intent := false
	for _, i := range e.journal.Snapshot().Build {
		if i.Phase == "attempt_intent" && buildAuthority(i.Attempt, i.BuildID) == a {
			intent = true
			if i.InputHash != domain.HostingDocumentHash(status.Build.Snapshot) {
				return errors.New("retained build snapshot changed")
			}
		}
	}
	if !intent {
		return errors.New("build logs have no immutable intent")
	}
	if !historical {
		return requireLive(status)
	}
	return nil
}
