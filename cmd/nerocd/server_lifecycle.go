package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// serverLifecycle is the small ownership boundary for an HTTP listener.  It
// deliberately owns the listener, drain state, server timeouts, and terminal
// shutdown path so the command and tests do not need to recreate that logic.
type serverLifecycle struct {
	Listener    net.Listener
	Handler     http.Handler
	Termination <-chan struct{}
	Grace       time.Duration
	DrainWindow time.Duration
	Logger      *slog.Logger
	OnDrain     func(bool)

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int

	draining atomic.Bool
}

func (l *serverLifecycle) defaults() {
	if l.Grace <= 0 {
		l.Grace = 25 * time.Second
	}
	if l.DrainWindow <= 0 {
		// Keep already accepted keep-alive connections observable long enough
		// to return health/ready/draining semantics before Shutdown closes the
		// listener and idle sockets.
		l.DrainWindow = 150 * time.Millisecond
	}
	if l.ReadHeaderTimeout <= 0 {
		l.ReadHeaderTimeout = 5 * time.Second
	}
	if l.ReadTimeout <= 0 {
		l.ReadTimeout = 30 * time.Second
	}
	if l.WriteTimeout <= 0 {
		l.WriteTimeout = 30 * time.Second
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = 60 * time.Second
	}
	if l.MaxHeaderBytes <= 0 {
		l.MaxHeaderBytes = 1 << 20
	}
}

func (l *serverLifecycle) Draining() bool { return l.draining.Load() }

func (l *serverLifecycle) setDraining(value bool) {
	l.draining.Store(value)
	if l.OnDrain != nil {
		l.OnDrain(value)
	}
}

// Serve blocks until the server exits or a termination request arrives. A
// graceful completion normalizes http.ErrServerClosed to nil; a grace expiry
// force-closes connections and returns a non-nil error to make orchestration
// restart rather than silently report a clean drain.
func (l *serverLifecycle) Serve(ctx context.Context) error {
	if l.Listener == nil || l.Handler == nil {
		return errors.New("server lifecycle requires listener and handler")
	}
	l.defaults()
	server := &http.Server{
		Handler:           l.Handler,
		ReadHeaderTimeout: l.ReadHeaderTimeout,
		ReadTimeout:       l.ReadTimeout,
		WriteTimeout:      l.WriteTimeout,
		IdleTimeout:       l.IdleTimeout,
		MaxHeaderBytes:    l.MaxHeaderBytes,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(l.Listener) }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	case <-l.Termination:
	}

	l.setDraining(true)
	if l.Logger != nil {
		l.Logger.Info("server draining")
	}
	window := time.NewTimer(l.DrainWindow)
	select {
	case <-window.C:
	case <-ctx.Done():
		window.Stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), l.Grace)
	err := server.Shutdown(shutdownCtx)
	cancel()
	if err != nil {
		_ = server.Close()
		if l.Logger != nil {
			l.Logger.Error("server forced closed", "error", err)
		}
		return fmt.Errorf("server shutdown deadline exceeded: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
