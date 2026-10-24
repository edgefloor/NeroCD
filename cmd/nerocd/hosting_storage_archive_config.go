package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"nerocd/internal/domain"
)

type hostingStorageArchivePolicy struct {
	Helper *domain.HostingStorageArchiveHelper `json:"helper"`
	Limits *domain.HostingStorageArchiveLimits `json:"limits"`
}

// loadHostingStorageArchivePolicy accepts only an explicit operator document.
// All diagnostics intentionally omit its path, contents and decoder errors.
func loadHostingStorageArchivePolicy() (result *hostingStorageArchivePolicy, resultErr error) {
	path := os.Getenv("NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE")
	if path == "" {
		return nil, nil
	}
	invalid := errors.New("invalid hosting storage archive policy")
	if strings.TrimSpace(path) != path {
		return nil, invalid
	}
	// Nonblocking open prevents a FIFO substituted for the operator file from
	// hanging startup before the regular-file check. Symlinks are not accepted.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, invalid
	}
	defer func() {
		if f.Close() != nil {
			result = nil
			resultErr = invalid
		}
	}()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 65536 {
		return nil, invalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, invalid
	}
	// Compact whitespace before the shared protocol decoder's tighter limit;
	// the complete operator file remains independently bounded to 64 KiB.
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return nil, invalid
	}
	var policy hostingStorageArchivePolicy
	if domain.DecodeHostingStorageProtocolJSON(compact.Bytes(), &policy) != nil || policy.Helper == nil || policy.Limits == nil || policy.Helper.Validate() != nil || policy.Limits.Validate() != nil {
		return nil, invalid
	}
	return &policy, nil
}

// This seam is the actual queue startup boundary, not a second configuration
// path. The store rejects configuration after workers have started.
type storageArchiveHostingStarter interface {
	ConfigureStorageArchive(*domain.HostingStorageArchiveHelper, domain.HostingStorageArchiveLimits) error
	StartHostingExecution(context.Context) (func(context.Context) error, error)
}

func startConfiguredHostingExecution(ctx context.Context, pg storageArchiveHostingStarter, policy *hostingStorageArchivePolicy) (func(context.Context) error, error) {
	var helper *domain.HostingStorageArchiveHelper
	var limits domain.HostingStorageArchiveLimits
	if policy != nil {
		if policy.Helper == nil || policy.Limits == nil || policy.Helper.Validate() != nil || policy.Limits.Validate() != nil {
			return nil, errors.New("invalid hosting storage archive policy")
		}
		helper, limits = policy.Helper, *policy.Limits
	}
	if err := pg.ConfigureStorageArchive(helper, limits); err != nil {
		return nil, fmt.Errorf("configure hosting storage archive: %w", err)
	}
	stop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		return nil, fmt.Errorf("start hosting execution: %w", err)
	}
	return stop, nil
}
