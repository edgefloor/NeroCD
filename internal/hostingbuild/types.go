// Package hostingbuild builds local images from caller-owned immutable checkouts.
// It does not acquire source, deploy workloads, or establish hostile-code isolation.
package hostingbuild

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

// Pinned toolchain images are fixed by this package and are not request inputs.
const (
	BuildKitImage      = "moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c"
	DockerfileFrontend = "docker/dockerfile:1.28.0@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b"
	RailpackFrontend   = "ghcr.io/railwayapp/railpack-frontend:v0.40.1@sha256:f1973377693af30c9b37a92c97c661c07b277ccdc6be909213c74c771f8d2d6d"
	StaticImage        = "nginx:1.29.3-alpine@sha256:b3c656d55d7ad751196f21b7fd2e8d4da9cb430e32f646adcf92441b72f82b14"
)

var (
	// ErrCleanupUnconfirmed blocks all reuse until operator reconciliation.
	ErrCleanupUnconfirmed = errors.New("cleanup_unconfirmed: operator reconciliation required")
	// ErrBusy reports another process holding this host build reservation.
	ErrBusy       = errors.New("another local build owns this host")
	idPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// Config is trusted operator configuration, never request data. All engine
// processes on a host must use the same private StateDir and fixed local daemon.
// The Docker binary, Buildx binary and state ancestors must be administrator-owned.
type Config struct {
	DockerBinary string
	Socket       string
	BuildxBinary string
	BuildxSHA256 string
	// PlannerImage is an operator-built image with the pinned Railpack 0.40.1
	// CLI at /usr/local/bin/railpack and verified Mise 2026.9.15 at
	// /usr/local/bin/mise. A digest or local immutable ID is required.
	PlannerImage string
	StateDir     string
	// CgroupRoot is the explicit administrator-delegated cgroup v2 directory.
	CgroupRoot string
}

// Strategy selects an adapter with fixed toolchain inputs.
type Strategy string

// Supported build strategies.
const (
	Dockerfile Strategy = "dockerfile"
	Railpack   Strategy = "railpack"
	Static     Strategy = "static"
)

// Limits bounds the complete build. SwapBytes is additional swap, not total RAM.
// DiskMode must be monitored. Sampling can overshoot UsageBytes and ReserveBytes.
type Limits struct {
	CPUQuota      int64 // microseconds per 100000 microsecond period
	MemoryBytes   int64
	SwapBytes     int64
	Deadline      time.Duration
	ContextBytes  int64
	ContextFiles  int
	PlanBytes     int64
	LogBytes      int64
	ExportBytes   int64
	UnpackedBytes int64
	UsageBytes    int64
	ReserveBytes  uint64
	PollInterval  time.Duration
	DiskMode      string
}

// Request names an immutable source and explicit resource budgets.
type Request struct {
	BuildID  string
	Checkout string
	SourceID string
	InputID  string
	DaemonID string
	Strategy Strategy
	Platform string
	Limits   Limits
}

// Result is a receipt for an image verified on the fixed daemon. ManifestDigest
// describes the retained OCI manifest; ImageID is the daemon identity derived from the validated index, manifest or config.
// It deliberately contains no registry RepoDigest.
type Result struct {
	Origin         string `json:"origin"`
	BuildID        string `json:"build_id"`
	SourceID       string `json:"source_id"`
	InputID        string `json:"input_id"`
	RequestDigest  string `json:"request_digest"`
	ContextDigest  string `json:"context_digest"`
	DaemonID       string `json:"daemon_id"`
	ImageID        string `json:"image_id"`
	ManifestDigest string `json:"manifest_digest"`
	IndexDigest    string `json:"index_digest"`
	ConfigDigest   string `json:"config_digest"`
	Platform       string `json:"platform"`
}

// Engine performs synchronous builds against one fixed local daemon.
type Engine struct{ config Config }

// New validates trusted operator configuration and the pinned Buildx executable.
func New(c Config) (*Engine, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("local build engine requires Linux on the Docker daemon host")
	}
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.Socket) || !filepath.IsAbs(c.DockerBinary) || !filepath.IsAbs(c.BuildxBinary) {
		return nil, errors.New("configuration requires absolute paths")
	}
	if !digestPattern.MatchString(c.BuildxSHA256) {
		return nil, errors.New("buildx checksum required")
	}
	if c.BuildxSHA256 != buildxChecksum(runtime.GOARCH) {
		return nil, errors.New("unsupported Buildx 0.38.1 checksum")
	}
	if err := validateDelegation(c.CgroupRoot); err != nil {
		return nil, err
	}
	if err := privateDir(c.StateDir); err != nil {
		return nil, err
	}
	st, err := os.Stat(c.Socket)
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		return nil, errors.New("fixed Unix Docker socket required")
	}
	for _, p := range []string{c.DockerBinary, c.BuildxBinary} {
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
			return nil, fmt.Errorf("unsafe executable: %s", p)
		}
	}
	if got, err := fileDigest(c.BuildxBinary); err != nil || got != c.BuildxSHA256 {
		return nil, errors.New("buildx binary checksum mismatch")
	}
	return &Engine{config: c}, nil
}

func buildxChecksum(arch string) string {
	switch arch {
	case "amd64":
		return "sha256:e25d5cd675092bb5d1d03632fdc46cdf3fede7b514dc5f023a105e00d3ce5ad4"
	case "arm64":
		return "sha256:3b83b3731916ba433feccaaa689fe5bcdc81c641d92c8e6c2ea865dfa141c97a"
	default:
		return ""
	}
}

func (r Request) validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !idPattern.MatchString(r.BuildID) || r.SourceID == "" || len(r.SourceID) > 512 || r.InputID == "" || len(r.InputID) > 512 || r.DaemonID == "" {
		return errors.New("build/source/input/daemon identity required")
	}
	if !filepath.IsAbs(r.Checkout) || r.Checkout != filepath.Clean(r.Checkout) {
		return errors.New("absolute canonical checkout required")
	}
	if r.Strategy != Dockerfile && r.Strategy != Railpack && r.Strategy != Static {
		return errors.New("unsupported build strategy")
	}
	if r.Platform != "linux/amd64" && r.Platform != "linux/arm64" {
		return errors.New("unsupported single Linux platform")
	}
	l := r.Limits
	if l.LogBytes > 64<<20 || l.PlanBytes > 16<<20 {
		return errors.New("log or planner budget exceeds supported memory bound")
	}
	if r.Strategy == Railpack && l.MemoryBytes < 512<<20 {
		return errors.New("railpack requires at least 512MiB aggregate memory")
	}
	if l.DiskMode != "monitored" {
		return errors.New("only monitored disk mode supported; hard quota unproven")
	}
	if l.CPUQuota < 2000 || l.CPUQuota > 1000000000 || l.MemoryBytes < 64<<20 || l.SwapBytes < 0 || l.Deadline <= 0 || l.Deadline > 24*time.Hour || l.ContextBytes <= 0 || l.ContextFiles <= 0 || l.PlanBytes <= 0 || l.LogBytes <= 0 || l.ExportBytes <= 0 || l.UnpackedBytes <= 0 || l.UsageBytes <= 0 || l.ReserveBytes == 0 || l.ReserveBytes > 1<<50 || l.PollInterval < 10*time.Millisecond || l.PollInterval > time.Second {
		return errors.New("all resource budgets must be explicit and positive; poll interval 10ms..1s")
	}
	// Headroom makes +1 bounded reads and arithmetic safe.
	for _, n := range []int64{l.ContextBytes, l.PlanBytes, l.LogBytes, l.ExportBytes, l.UnpackedBytes, l.UsageBytes, l.MemoryBytes, l.SwapBytes} {
		if n > 1<<50 {
			return errors.New("resource budget exceeds supported maximum")
		}
	}
	return nil
}
