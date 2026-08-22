package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/store"
	"nerocd/web"
)

type runtimeConfig struct {
	addr                 string
	databaseURL          string
	mode                 deploymentMode
	leaseTTL             time.Duration
	reaperInterval       time.Duration
	cookieSecure         bool
	publicOrigin         string
	trustedProxyCIDRs    []string
	developmentMemory    bool
	devBootstrapEmail    string
	devBootstrapPassword string
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadRuntimeConfig(*addr)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	service, closeStore, err := newService(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer closeStore()
	reaperCtx, stopReaper := context.WithCancel(context.Background())
	defer stopReaper()
	go func() {
		ticker := time.NewTicker(cfg.reaperInterval)
		defer ticker.Stop()
		for {
			select {
			case <-reaperCtx.Done():
				return
			case <-ticker.C:
				if err := service.ReapExpiredLeases(reaperCtx); err != nil {
					logger.Error("lease reaper", "error", err)
				}
			}
		}
	}()
	server := api.NewServerWithConfig(service, logger, web.Static(), api.ServerConfig{AllowInsecureCookies: !cfg.cookieSecure, PublicOrigin: cfg.publicOrigin, TrustedProxyCIDRs: cfg.trustedProxyCIDRs})

	if cfg.databaseURL == "" {
		logger.Warn("server using in-memory store; set NEROCD_DATABASE_URL for durable runtime state")
	}
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return err
	}
	terminationSignals := make(chan os.Signal, 1)
	signal.Notify(terminationSignals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(terminationSignals)
	termination := make(chan struct{})
	go func() {
		select {
		case <-terminationSignals:
			close(termination)
		case <-reaperCtx.Done():
		}
	}()
	logger.Info("server listening", "addr", cfg.addr)
	return (&serverLifecycle{
		Listener:    listener,
		Handler:     server,
		Termination: termination,
		Logger:      logger,
		OnDrain:     server.SetDraining,
	}).Serve(context.Background())
}

func loadRuntimeConfig(addr string) (runtimeConfig, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return runtimeConfig{}, errors.New("listen address is required")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return runtimeConfig{}, fmt.Errorf("listen address must include host and port or :port: %w", err)
	}
	mode, err := configuredMode()
	if err != nil {
		return runtimeConfig{}, err
	}
	databaseURL, err := loadDatabaseURL(mode)
	if err != nil {
		return runtimeConfig{}, err
	}
	developmentMemory := mode == modeDevelopment && strings.TrimSpace(os.Getenv("NEROCD_DEV_MEMORY")) == "true"
	if (mode == modeProduction || os.Getenv("NEROCD_REQUIRE_DATABASE") == "true") && databaseURL == "" {
		return runtimeConfig{}, errors.New("NEROCD_REQUIRE_DATABASE=true requires NEROCD_DATABASE_URL")
	}
	if databaseURL == "" && !developmentMemory {
		return runtimeConfig{}, errors.New("NEROCD_DATABASE_URL is required; set NEROCD_DEV_MEMORY=true only for an explicit disposable development store")
	}
	devBootstrapEmail := ""
	devBootstrapPassword := ""
	if developmentMemory {
		devBootstrapEmail = strings.TrimSpace(os.Getenv("NEROCD_DEV_BOOTSTRAP_EMAIL"))
		passwordFile := strings.TrimSpace(os.Getenv("NEROCD_DEV_BOOTSTRAP_PASSWORD_FILE"))
		if devBootstrapEmail == "" || passwordFile == "" {
			return runtimeConfig{}, errors.New("NEROCD_DEV_MEMORY=true requires NEROCD_DEV_BOOTSTRAP_EMAIL and NEROCD_DEV_BOOTSTRAP_PASSWORD_FILE")
		}
		secret, secretErr := readOwnerOnlyProductionSecret(passwordFile)
		if secretErr != nil || len(strings.TrimSpace(string(secret))) == 0 {
			return runtimeConfig{}, errors.New("development bootstrap password cannot be read")
		}
		devBootstrapPassword = strings.TrimSpace(string(secret))
	}
	ttl := 2 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("NEROCD_LEASE_TTL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 5*time.Second || parsed > 10*time.Minute {
			return runtimeConfig{}, errors.New("NEROCD_LEASE_TTL must be between 5s and 10m")
		}
		ttl = parsed
	}
	reaper := 5 * time.Second
	if raw := strings.TrimSpace(os.Getenv("NEROCD_REAPER_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < time.Second || parsed > time.Minute {
			return runtimeConfig{}, errors.New("NEROCD_REAPER_INTERVAL must be between 1s and 1m")
		}
		reaper = parsed
	}
	cookieSecure := true
	if raw, ok := os.LookupEnv("NEROCD_COOKIE_SECURE"); ok {
		parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return runtimeConfig{}, errors.New("NEROCD_COOKIE_SECURE must be true or false")
		}
		cookieSecure = parsed
	}
	if mode == modeProduction && !cookieSecure {
		return runtimeConfig{}, errors.New("production rejects insecure cookies")
	}
	publicOrigin := strings.TrimSpace(os.Getenv("NEROCD_PUBLIC_ORIGIN"))
	trustedProxyCIDRs := []string{}
	if raw := strings.TrimSpace(os.Getenv("NEROCD_TRUSTED_PROXY_CIDRS")); raw != "" {
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(value)
			prefix, parseErr := netip.ParsePrefix(value)
			if parseErr != nil || !prefix.IsValid() {
				return runtimeConfig{}, errors.New("NEROCD_TRUSTED_PROXY_CIDRS must contain valid CIDRs")
			}
			trustedProxyCIDRs = append(trustedProxyCIDRs, prefix.Masked().String())
		}
		if len(trustedProxyCIDRs) > 32 {
			return runtimeConfig{}, errors.New("NEROCD_TRUSTED_PROXY_CIDRS may contain at most 32 CIDRs")
		}
	}
	if mode == modeProduction {
		origin, err := url.Parse(publicOrigin)
		if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.User != nil {
			return runtimeConfig{}, errors.New("production requires NEROCD_PUBLIC_ORIGIN as an HTTPS origin")
		}
		publicOrigin = origin.String()
	}
	return runtimeConfig{addr: addr, databaseURL: databaseURL, mode: mode, leaseTTL: ttl, reaperInterval: reaper, cookieSecure: cookieSecure, publicOrigin: publicOrigin, trustedProxyCIDRs: trustedProxyCIDRs, developmentMemory: developmentMemory, devBootstrapEmail: devBootstrapEmail, devBootstrapPassword: devBootstrapPassword}, nil
}

func newService(ctx context.Context, cfg runtimeConfig) (*app.Service, func(), error) {
	if cfg.databaseURL != "" {
		pg, err := store.OpenPostgres(ctx, cfg.databaseURL)
		if err != nil {
			return nil, nil, err
		}
		service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Retention: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg})
		if err != nil {
			_ = pg.Close()
			return nil, nil, err
		}
		service.SetAllowLegacyPasswordVerification(cfg.mode != modeProduction)
		if err := service.SetLeaseTTL(cfg.leaseTTL); err != nil {
			_ = pg.Close()
			return nil, nil, err
		}
		return service, func() { _ = pg.Close() }, nil
	}

	if !cfg.developmentMemory {
		return nil, nil, errors.New("in-memory store requires explicit development mode")
	}
	mem := store.NewMemoryStore()
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Retention: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem})
	if err != nil {
		return nil, nil, err
	}
	if _, err := service.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: cfg.devBootstrapEmail, Name: "Development Operator", Password: cfg.devBootstrapPassword}); err != nil {
		return nil, nil, errors.New("development bootstrap failed")
	}
	service.SetAllowLegacyPasswordVerification(cfg.mode != modeProduction)
	if err := service.SetLeaseTTL(cfg.leaseTTL); err != nil {
		return nil, nil, err
	}
	return service, func() {}, nil
}
