package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type blockedLoginUsers struct {
	store.UserRepository
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (u *blockedLoginUsers) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	u.calls.Add(1)
	u.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return domain.User{}, ctx.Err()
	case <-u.release:
		return u.UserRepository.GetUserByEmail(ctx, email)
	}
}

func TestConcurrentSessionAdmissionReservesAccountAndSourceCapacity(t *testing.T) {
	for _, budget := range []string{"account", "source"} {
		t.Run(budget, func(t *testing.T) {
			s := newTestService()
			s.SetLoginLimiter(auth.NewLoginLimiter(time.Now, 2, time.Minute, 64))
			users := &blockedLoginUsers{UserRepository: s.users, entered: make(chan struct{}, 12), release: make(chan struct{})}
			s.users = users
			var wg sync.WaitGroup
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(users.release) }); wg.Wait() })
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			results := make(chan error, 12)
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					email, source := "admin@example.local", "192.0.2.1"
					if budget == "account" {
						source = fmt.Sprintf("192.0.2.%d", i+1)
					} else {
						email = fmt.Sprintf("missing%d@example.local", i)
					}
					_, err := s.CreateSessionWithMetadata(ctx, email, "incorrect", SessionCreateMetadata{SourceIP: source})
					results <- err
				}(i)
			}
			for range 2 {
				select {
				case <-users.entered:
				case <-ctx.Done():
					t.Fatal("admitted login did not reach repository")
				}
			}
			for range 10 {
				select {
				case err := <-results:
					if !errors.Is(err, auth.ErrRateLimited) {
						t.Fatalf("over-budget error=%v", err)
					}
				case <-ctx.Done():
					t.Fatal("overlapping requests were not rejected before credential lookup")
				}
			}
			if users.calls.Load() != 2 {
				t.Fatalf("credential lookups=%d", users.calls.Load())
			}
			release.Do(func() { close(users.release) })
			for range 2 {
				select {
				case err := <-results:
					if !errors.Is(err, auth.ErrInvalidCredentials) {
						t.Fatalf("admitted login error=%v", err)
					}
				case <-ctx.Done():
					t.Fatal("verification did not complete")
				}
			}
		})
	}
}

func TestCanceledSessionLookupReleasesLoginReservation(t *testing.T) {
	s := newTestService()
	s.SetLoginLimiter(auth.NewLoginLimiter(time.Now, 1, time.Minute, 64))
	users := &blockedLoginUsers{UserRepository: s.users, entered: make(chan struct{}, 2), release: make(chan struct{})}
	s.users = users
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.CreateSession(ctx, "admin@example.local", "admin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup: %v", err)
	}
	close(users.release)
	if _, err := s.CreateSession(t.Context(), "admin@example.local", "admin"); err != nil {
		t.Fatalf("cancellation leaked login capacity: %v", err)
	}
}
