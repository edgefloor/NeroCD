package hostingproxy

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"reflect"
	"time"
)

// Outcome describes whether a generation was verified, rejected, or left uncertain.
type Outcome string

const (
	// Applied means the desired routing generation loaded; TLS may still be pending.
	Applied Outcome = "applied"
	// RolledBack means the previous routing generation was restored and verified.
	RolledBack Outcome = "rolled_back"
	// Uncertain requires the caller to retain its fenced proxy hold for reconciliation.
	Uncertain Outcome = "uncertain"
	// Refused means validation or admission prevented a new generation mutation.
	Refused Outcome = "refused"
)

// Certificate contains public TLS observations and never certificate or account keys.
type Certificate struct {
	Hostname, Issuer, Fingerprint, Status string
	ExpiresAt                             time.Time
}

// RouteObservation separates loaded HTTP routing from trusted HTTPS readiness.
type RouteObservation struct {
	RouteID               string
	HTTPReady, HTTPSReady bool
	Certificate           Certificate
}

// Result reports the desired and last verified routing generation.
// Outcome remains meaningful when a method also returns an error; callers must
// retain their hold on Uncertain and must not infer HTTPS readiness from Applied.
type Result struct {
	Outcome                             Outcome
	DesiredGeneration, LoadedGeneration uint64
	Hash, ContainerID                   string
	Routes                              []RouteObservation
}

// Engine methods serialize through an owner-only Linux file lock across processes.
// They do not replace the caller's fenced lease or provide atomic Docker fencing.
type Engine struct {
	settings          Settings
	fixtureCA         string
	fixtureRoots      *x509.CertPool
	fixtureDockerArgs []string
	verifyTimeout     time.Duration
}

// New validates trusted operator settings and requires a Linux host.
// It performs no installation; each operation acquires its own local state lock.
func New(s Settings) (*Engine, error) {
	if err := platform(); err != nil {
		return nil, err
	}
	if err := validateSettings(s); err != nil {
		return nil, err
	}
	return &Engine{settings: s, verifyTimeout: 15 * time.Second}, nil
}
func (e *Engine) validate(p Plan) error {
	if p.hash == "" || !reflect.DeepEqual(p.settings, e.settings) {
		return errors.New("plan does not belong to engine settings")
	}
	return platform()
}

// Install creates an absent proxy or verifies a matching installed generation.
// It refuses foreign containers and occupied ports without replacing them.
func (e *Engine) Install(ctx context.Context, p Plan) (Result, error) { return e.apply(ctx, p, true) }

// Apply replaces the complete routing generation after checking its predecessor.
// A replay of the same generation and hash verifies and acknowledges existing state.
func (e *Engine) Apply(ctx context.Context, p Plan) (Result, error) { return e.apply(ctx, p, false) }
func (e *Engine) apply(ctx context.Context, p Plan, install bool) (result Result, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result = Result{Outcome: Refused, DesiredGeneration: p.generation, Hash: p.hash}
	if err = e.validate(p); err != nil {
		return result, err
	}
	s, err := openStorage(ctx, e.settings.Directory)
	if err != nil {
		return result, err
	}
	defer s.close()
	old, err := s.load()
	if err != nil {
		return result, err
	}
	if old.Generation != 0 && (old.Identity != p.identity || old.StaticHash != p.staticHash) {
		return result, errors.New("persistent identity or static settings mismatch")
	}
	if old.Phase != "" && old.Phase != "committed" {
		result.Outcome = Uncertain
		return result, errors.New("unfinished prior operation; retain controller hold")
	}
	if old.Generation != 0 {
		if err = s.checkFile("certificates/acme.json"); err != nil {
			result.Outcome = Uncertain
			return result, err
		}
	}
	c, err := e.find(ctx, p)
	if err != nil {
		return result, err
	}
	if c != nil {
		result.ContainerID = c.ID
	}
	if old.Hash == p.hash && old.Generation == p.generation {
		if c == nil {
			return result, errors.New("recorded proxy container missing; explicit recovery required")
		}
		if err = e.loaded(ctx, p); err != nil {
			result.Outcome = Uncertain
			return result, err
		}
		if old.Phase != "committed" {
			result.Outcome = Uncertain
			return result, errors.New("unfinished generation requires explicit recovery")
		}
		return e.observation(ctx, p, c.ID), nil
	}

	if old.Generation != p.previous {
		return result, errors.New("stale previous generation")
	}
	if c == nil {
		if !install || old.Generation != 0 {
			return result, errors.New("proxy is not installed")
		}
		if err = checkPorts(e.settings.ManagementPort); err != nil {
			return result, err
		}
	} else if old.Generation == 0 {
		return result, errors.New("owned container lacks generation receipt")
	}
	if err = e.daemon(ctx, p); err != nil {
		return result, err
	}
	next := receipt{Identity: p.identity, StaticHash: p.staticHash, Hash: p.hash, Generation: p.generation, Routes: p.Routes(), Phase: "intent"}
	// An intent precedes external effects. Any lost response after this point is uncertain.
	if err = s.save(next); err != nil {
		result.Outcome = Uncertain
		return result, err
	}
	result.Outcome = Uncertain
	if c == nil {
		if _, err = s.root.Lstat("certificates/acme.json"); errors.Is(err, os.ErrNotExist) {
			f, x := s.root.OpenFile("certificates/acme.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if x != nil {
				return result, x
			}
			if x = f.Sync(); x != nil {
				_ = f.Close()
				return result, x
			}
			if x = f.Close(); x != nil {
				return result, x
			}
		}
		if err = s.checkFile("certificates/acme.json"); err != nil {
			return result, err
		}
		if _, err = e.command(ctx, p, "pull", Image); err != nil {
			return result, err
		}
	}
	if old.Generation != 0 {
		b, x := s.read("dynamic/routes.yml")
		if x != nil {
			return result, x
		}
		if x = s.atomic("previous.json", b); x != nil {
			return result, x
		}
	}
	if err = s.atomic("dynamic/routes.yml", p.dynamic()); err != nil {
		return result, err
	}
	if c == nil {
		if err = e.launch(ctx, p); err != nil {
			return result, err
		}
		c, err = e.find(ctx, p)
		if err != nil || c == nil {
			return result, errors.New("proxy launch response uncertain")
		}
		result.ContainerID = c.ID
	}
	if err = e.waitLoaded(ctx, p); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if old.Generation == 0 {
			return result, err
		}
		previous := p
		previous.generation = old.Generation
		previous.hash = old.Hash
		previous.routes = append([]Route(nil), old.Routes...)
		b, x := s.read("previous.json")
		if x != nil {
			return result, x
		}
		if x = s.atomic("dynamic/routes.yml", b); x != nil {
			return result, x
		}
		if x = e.waitLoaded(ctx, previous); x != nil {
			return result, errors.New("rollback not verified; retain controller hold")
		}
		if x = s.save(old); x != nil {
			return result, x
		}
		result.Outcome = RolledBack
		result.LoadedGeneration = old.Generation
		return result, errors.New("candidate rejected; previous generation restored")
	}
	next.Phase = "committed"
	if err = s.save(next); err != nil {
		return result, err
	}
	return e.observation(ctx, p, c.ID), nil
}

// Observe verifies the committed receipt, owned container, loaded routes, and probes.
// It never acknowledges unfinished intent or treats an untrusted certificate as ready.
func (e *Engine) Observe(ctx context.Context, p Plan) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := Result{Outcome: Uncertain, DesiredGeneration: p.generation, Hash: p.hash}
	if err := e.validate(p); err != nil {
		return result, err
	}
	s, err := openStorage(ctx, e.settings.Directory)
	if err != nil {
		return result, err
	}
	defer s.close()
	r, err := s.load()
	if err != nil {
		return result, err
	}
	if r.Identity != p.identity || r.Hash != p.hash || r.Generation != p.generation || r.Phase != "committed" {
		return result, errors.New("generation receipt not committed or mismatched")
	}
	if err = s.checkFile("certificates/acme.json"); err != nil {
		return result, err
	}
	c, err := e.find(ctx, p)
	if err != nil || c == nil {
		return result, errors.New("owned proxy not observable")
	}
	if err = e.loaded(ctx, p); err != nil {
		return result, err
	}
	return e.observation(ctx, p, c.ID), nil
}
func (e *Engine) waitLoaded(ctx context.Context, p Plan) error {
	ctx, cancel := context.WithTimeout(ctx, e.verifyTimeout)
	defer cancel()
	for {
		if err := e.loaded(ctx, p); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
