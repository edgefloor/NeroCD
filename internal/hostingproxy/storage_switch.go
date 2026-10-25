package hostingproxy

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"time"
)

// ApplyInterruptedStorageSwitch replaces an explicitly stopped predecessor.
// The caller must revalidate current controller authority and sole-writer proof
// immediately before mutation. No failure path restores a stopped predecessor.
// A forward recovery may admit only the exact prior or exact failed generation.
func (e *Engine) ApplyInterruptedStorageSwitch(ctx context.Context, p, prior Plan, failed *Plan, before func(context.Context) error) (result Result, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result = Result{Outcome: Refused, DesiredGeneration: p.generation, LoadedGeneration: prior.generation, Hash: p.hash}
	if before == nil || e.validate(p) != nil || e.validate(prior) != nil || p.verification == nil || p.previous != prior.generation || p.identity != prior.identity || p.staticHash != prior.staticHash {
		return result, errors.New("invalid interrupted storage switch")
	}
	baseline := receipt{Identity: prior.identity, StaticHash: prior.staticHash, Hash: prior.hash, Generation: prior.generation, Routes: prior.Routes(), Phase: "committed", Verification: prior.verification}
	if p.switchPredecessor(baseline, false) != nil {
		return result, errors.New("interrupted switch route scope changed")
	}
	if failed != nil && (e.validate(*failed) != nil || failed.identity != p.identity || failed.staticHash != p.staticHash || failed.previous != prior.generation || failed.generation >= p.generation || !reflect.DeepEqual(failed.routes, p.routes) || !reflect.DeepEqual(failed.verification, p.verification)) {
		return result, errors.New("invalid failed storage switch")
	}
	// Unreadable local evidence cannot establish the closed prior state.
	result.Outcome = Uncertain
	state, err := openStorage(ctx, e.settings.Directory)
	if err != nil {
		return result, err
	}
	defer state.close()
	old, err := state.load()
	if err != nil {
		return result, err
	}
	matches := func(value receipt, plan Plan, committed bool) bool {
		return value.Identity == plan.identity && value.StaticHash == plan.staticHash && value.Generation == plan.generation && value.Hash == plan.hash && reflect.DeepEqual(value.Routes, plan.routes) && reflect.DeepEqual(value.Verification, plan.verification) && (!committed && (value.Phase == "intent" || value.Phase == "committed") || value.Phase == "committed")
	}
	if matches(old, p, true) {
		if err = e.daemon(ctx, p); err != nil {
			return result, err
		}
		container, e1 := e.find(ctx, p)
		if e1 != nil || container == nil {
			return result, errors.New("recorded proxy missing")
		}
		if err = e.loaded(ctx, p); err != nil {
			return result, err
		}
		return e.verifiedObservation(ctx, p, container.ID)
	}
	if !matches(old, prior, true) && (failed == nil || !matches(old, *failed, false)) {
		result.Outcome = Uncertain
		return result, errors.New("storage switch local receipt is outside admitted history")
	}
	if err = state.checkFile("certificates/acme.json"); err != nil {
		return result, err
	}
	file, err := state.read("dynamic/routes.yml")
	if err != nil {
		return result, err
	}
	loaded := prior
	if !bytes.Equal(file, prior.dynamic()) {
		if failed == nil || !bytes.Equal(file, failed.dynamic()) {
			result.Outcome = Uncertain
			return result, errors.New("storage switch local file is outside admitted history")
		}
		loaded = *failed
	}
	if err = e.daemon(ctx, p); err != nil {
		return result, err
	}
	container, err := e.find(ctx, p)
	if err != nil || container == nil {
		return result, errors.New("storage switch proxy is not installed")
	}
	result.ContainerID = container.ID
	result.Outcome = Refused
	// This verifies exact loaded management identity. HTTP/HTTPS readiness of A
	// is deliberately absent only on this separately admitted interruption path.
	if err = e.loadedInterruptedStoragePredecessor(ctx, loaded, p); err != nil {
		return result, errors.New("storage switch loaded state is outside admitted history")
	}
	if err = before(ctx); err != nil || ctx.Err() != nil {
		return result, errors.New("storage switch current writer authority unavailable")
	}
	next := receipt{Identity: p.identity, StaticHash: p.staticHash, Hash: p.hash, Generation: p.generation, Routes: p.Routes(), Phase: "intent", Verification: p.verification}
	result.Outcome = Uncertain
	if err = state.save(next); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = state.atomic("dynamic/routes.yml", p.dynamic()); err != nil {
		return result, err
	}
	verified, err := e.waitVerified(ctx, p, container.ID)
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	next.Phase = "committed"
	if err = state.save(next); err != nil {
		return result, err
	}
	return verified, nil
}

func (e *Engine) loadedInterruptedStoragePredecessor(ctx context.Context, loaded, next Plan) error {
	if next.verification == nil || loaded.identity != next.identity || loaded.staticHash != next.staticHash || len(loaded.routes) != len(next.routes) {
		return errors.New("interrupted predecessor configuration scope changed")
	}
	for i, route := range next.routes {
		prior := loaded.routes[i]
		if next.required(route.ID) {
			route.HostPort = prior.HostPort
		}
		if route != prior {
			return errors.New("interrupted predecessor route scope changed")
		}
	}
	return e.loadedConfiguration(ctx, loaded, &next)
}

// VerifyInterruptedStorageReceipt verifies only already settled interrupted-storage
// evidence. It never retries a switch, authorizes a writer, or changes a receipt.
func (e *Engine) VerifyInterruptedStorageReceipt(ctx context.Context, current, prior Plan, failed *Plan, outcome Outcome, loadedGeneration uint64) error {
	for _, p := range []Plan{current, prior} {
		if err := e.validate(p); err != nil {
			return err
		}
	}
	if failed != nil {
		if err := e.validate(*failed); err != nil {
			return err
		}
	}
	state, err := openStorage(ctx, e.settings.Directory)
	if err != nil {
		return err
	}
	defer state.close()
	local, err := state.load()
	if err != nil {
		return err
	}
	if err = state.checkFile("certificates/acme.json"); err != nil {
		return err
	}
	file, err := state.read("dynamic/routes.yml")
	if err != nil {
		return err
	}
	return e.verifyInterruptedStorageReceipt(ctx, current, prior, failed, outcome, loadedGeneration, local, file)
}

// The public method retains the Linux eligibility guard. This read-only core is
// also exercised with owned scripted endpoints on other Unix development hosts.
func (e *Engine) verifyInterruptedStorageReceipt(ctx context.Context, current, prior Plan, failed *Plan, outcome Outcome, loadedGeneration uint64, local receipt, file []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if current.hash == "" || prior.hash == "" || !reflect.DeepEqual(current.settings, e.settings) || !reflect.DeepEqual(prior.settings, e.settings) || current.verification == nil || current.previous != prior.generation || current.identity != prior.identity || current.staticHash != prior.staticHash {
		return errors.New("invalid interrupted receipt history")
	}
	baseline := receipt{Identity: prior.identity, StaticHash: prior.staticHash, Hash: prior.hash, Generation: prior.generation, Routes: prior.Routes(), Phase: "committed", Verification: prior.verification}
	if current.switchPredecessor(baseline, false) != nil {
		return errors.New("interrupted receipt route scope changed")
	}
	if failed != nil && (failed.hash == "" || !reflect.DeepEqual(failed.settings, e.settings) || failed.identity != current.identity || failed.staticHash != current.staticHash || failed.previous != prior.generation || failed.generation >= current.generation || !reflect.DeepEqual(failed.routes, current.routes) || !reflect.DeepEqual(failed.verification, current.verification)) {
		return errors.New("invalid failed interrupted receipt history")
	}
	expected := current
	switch outcome {
	case Applied:
		if loadedGeneration != current.generation {
			return errors.New("applied interrupted receipt generation changed")
		}
	case Refused:
		if loadedGeneration != prior.generation {
			return errors.New("refused interrupted receipt generation changed")
		}
		expected = prior
	default:
		return errors.New("interrupted receipt outcome is unresolved or unsupported")
	}
	if local.Phase != "committed" || local.Identity != expected.identity || local.StaticHash != expected.staticHash || local.Hash != expected.hash || local.Generation != expected.generation || !reflect.DeepEqual(local.Routes, expected.routes) || !reflect.DeepEqual(local.Verification, expected.verification) {
		return errors.New("interrupted local receipt differs from exact settled history")
	}
	if !bytes.Equal(file, expected.dynamic()) {
		return errors.New("interrupted local file differs from settled history")
	}
	if err := e.daemon(ctx, expected); err != nil {
		return err
	}
	container, err := e.find(ctx, expected)
	if err != nil {
		return err
	}
	if container == nil || !container.State.Running {
		return errors.New("settled interrupted proxy container is missing or stopped")
	}
	if outcome == Refused {
		return e.loadedInterruptedStoragePredecessor(ctx, prior, current)
	}
	if err := e.loaded(ctx, current); err != nil {
		return err
	}
	_, err = e.verifiedObservation(ctx, current, container.ID)
	return err
}
