package app

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type bootstrapRenewalRepository struct {
	store.ServerBootstrapRepository
	renew func(context.Context, domain.ServerBootstrapStage) (domain.ServerBootstrapStage, error)
}

func (r bootstrapRenewalRepository) AuthorizeServerBootstrapDelivery(context.Context, string, string, domain.ServerBootstrapStage, *secretcrypto.Keyring, domain.AuditEvent) (store.ServerBootstrapDelivery, error) {
	return store.ServerBootstrapDelivery{}, nil
}

func (r bootstrapRenewalRepository) RenewServerBootstrapStage(ctx context.Context, _, _ string, stage domain.ServerBootstrapStage) (domain.ServerBootstrapStage, error) {
	return r.renew(ctx, stage)
}

func TestServerBootstrapInitialRenewalHonorsReturnedExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		stage := domain.ServerBootstrapStage{Command: domain.ServerBootstrapCommand{Action: "probe", Deadline: now.Add(time.Minute)}, ExpiresAt: now.Add(30 * time.Second)}
		repo := bootstrapRenewalRepository{renew: func(_ context.Context, stage domain.ServerBootstrapStage) (domain.ServerBootstrapStage, error) {
			stage.ExpiresAt = now
			return stage, nil
		}}
		op := domain.ServerBootstrapOperation{Input: domain.ServerBootstrapInput{Target: domain.ServerBootstrapTarget{IP: "192.0.2.10", Port: 22, User: "root"}}}
		_, err := (&Service{}).bootstrapRemote(t.Context(), repo, "actor", op, stage)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired renewal allowed remote submission: %v", err)
		}
	})
}

func TestServerBootstrapInitialRenewalIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		stage := domain.ServerBootstrapStage{Command: domain.ServerBootstrapCommand{Action: "probe", Deadline: now.Add(time.Minute)}, ExpiresAt: now.Add(2 * time.Second)}
		repo := bootstrapRenewalRepository{renew: func(ctx context.Context, _ domain.ServerBootstrapStage) (domain.ServerBootstrapStage, error) {
			<-ctx.Done()
			return domain.ServerBootstrapStage{}, ctx.Err()
		}}
		_, err := (&Service{}).bootstrapRemote(t.Context(), repo, "actor", domain.ServerBootstrapOperation{}, stage)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(now) > 2*time.Second {
			t.Fatalf("initial renewal exceeded confirmed authority: elapsed=%v err=%v", time.Since(now), err)
		}
	})
}

func TestServerBootstrapStalledRenewalCannotDelaySSHCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan int, 1)
		var sshStoppedAt time.Time
		var renewalJoined bool
		go func() {
			<-ctx.Done()
			sshStoppedAt = time.Now()
			done <- 1
		}()
		_, err := waitServerBootstrapRemote(ctx, cancel, now.Add(3*time.Second), time.Second, done, func(renewCtx context.Context) (time.Time, error) {
			<-renewCtx.Done()
			// Simulate a driver finishing cleanup after its context expires. SSH must
			// already be cancelled, and the caller must still join this owned work.
			time.Sleep(4 * time.Second)
			renewalJoined = true
			return now.Add(time.Hour), nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || sshStoppedAt.Sub(now) != 3*time.Second || !renewalJoined || time.Since(now) != 7*time.Second {
			t.Fatalf("err=%v SSH stopped after=%v renewal joined=%v returned after=%v", err, sshStoppedAt.Sub(now), renewalJoined, time.Since(now))
		}
	})
}

func TestServerBootstrapRenewalHonorsShortenedExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan int, 1)
		go func() { <-ctx.Done(); done <- 1 }()
		_, err := waitServerBootstrapRemote(ctx, cancel, now.Add(30*time.Second), 10*time.Second, done, func(context.Context) (time.Time, error) {
			return time.Now().Add(2 * time.Second), nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(now) != 12*time.Second {
			t.Fatalf("shortened expiry ignored: err=%v elapsed=%v", err, time.Since(now))
		}
	})
}

func TestServerBootstrapRenewalWaitIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan int, 1)
		go func() { <-ctx.Done(); done <- 1 }()
		_, err := waitServerBootstrapRemote(ctx, cancel, now.Add(30*time.Second), 10*time.Second, done, func(renewCtx context.Context) (time.Time, error) {
			<-renewCtx.Done()
			return time.Time{}, renewCtx.Err()
		})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(now) != 15*time.Second {
			t.Fatalf("renewal budget ignored: err=%v elapsed=%v", err, time.Since(now))
		}
	})
}

func TestServerBootstrapRenewalCannotResetCommandDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		ctx, cancel := context.WithDeadline(t.Context(), now.Add(2*time.Second))
		defer cancel()
		done := make(chan int, 1)
		go func() { <-ctx.Done(); done <- 1 }()
		_, err := waitServerBootstrapRemote(ctx, cancel, now.Add(30*time.Second), time.Second, done, func(context.Context) (time.Time, error) {
			return time.Now().Add(time.Hour), nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(now) != 2*time.Second {
			t.Fatalf("original command deadline extended: err=%v elapsed=%v", err, time.Since(now))
		}
	})
}

func TestServerBootstrapCompletionCannotRaceExpiredAuthority(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan int, 1)
		done <- 1
		_, err := waitServerBootstrapRemote(ctx, cancel, time.Now(), time.Second, done, func(context.Context) (time.Time, error) {
			return time.Time{}, errors.New("renewed expired authority")
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired completion accepted: %v", err)
		}
	})
}

func TestServerBootstrapRemoteCompletionCancelsAndJoinsRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan int, 1)
		renewalStarted := make(chan struct{})
		var joined bool
		go func() {
			<-renewalStarted
			time.Sleep(time.Second)
			done <- 7
		}()
		out, err := waitServerBootstrapRemote(ctx, cancel, now.Add(30*time.Second), time.Second, done, func(renewCtx context.Context) (time.Time, error) {
			close(renewalStarted)
			<-renewCtx.Done()
			joined = true
			return time.Time{}, renewCtx.Err()
		})
		if err != nil || out != 7 || !joined || time.Since(now) != 2*time.Second || ctx.Err() != nil {
			t.Fatalf("completion did not preserve authority and join renewal: out=%d err=%v joined=%v elapsed=%v ctxErr=%v", out, err, joined, time.Since(now), ctx.Err())
		}
	})
}

func TestServerBootstrapRemoteCompletionConstructsReceipt(t *testing.T) {
	now := time.Now()
	stage := domain.ServerBootstrapStage{
		Command: domain.ServerBootstrapCommand{ID: "command", Action: "probe", Deadline: now.Add(time.Minute)},
		LeaseID: "lease", Attempt: 1, Fence: 2, ExpiresAt: now.Add(30 * time.Second),
	}
	repo := bootstrapRenewalRepository{renew: func(_ context.Context, stage domain.ServerBootstrapStage) (domain.ServerBootstrapStage, error) {
		return stage, nil
	}}
	op := domain.ServerBootstrapOperation{Input: domain.ServerBootstrapInput{Target: domain.ServerBootstrapTarget{IP: "192.0.2.10", Port: 22, User: "root"}}}
	// The delivery stub supplies an empty SSH key. Probe rejects it locally before
	// dialing, so this exercises the real caller's closed receipt path without I/O.
	receipt, err := (&Service{}).bootstrapRemote(t.Context(), repo, "actor", op, stage)
	if err != nil || receipt.Outcome != "refused" || receipt.CommandID != stage.Command.ID || receipt.LeaseID != stage.LeaseID || receipt.Attempt != stage.Attempt || receipt.Fence != stage.Fence {
		t.Fatalf("completed remote call lost its receipt: receipt=%+v err=%v", receipt, err)
	}
}

func TestServerBootstrapCompletionRejectsCancelledParent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan int, 1)
	done <- 7
	_, err := waitServerBootstrapRemote(ctx, cancel, time.Now().Add(time.Minute), time.Second, done, func(context.Context) (time.Time, error) {
		return time.Time{}, errors.New("unexpected renewal")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled parent accepted completion: %v", err)
	}
}
