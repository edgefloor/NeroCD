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
	if err = e.loaded(ctx, loaded); err != nil {
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
