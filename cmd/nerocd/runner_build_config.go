package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/runnerruntime"
)

type runnerBuildConfigFile struct {
	Compose    bool                            `json:"compose,omitempty"`
	PrivateGit *runnerruntime.PrivateGitConfig `json:"private_git,omitempty"`
	Version    int                             `json:"version"`
	SourceRoot string                          `json:"source_root"`
	Engine     struct {
		DockerBinary string `json:"docker_binary"`
		Socket       string `json:"socket"`
		BuildxBinary string `json:"buildx_binary"`
		BuildxSHA256 string `json:"buildx_sha256"`
		PlannerImage string `json:"planner_image"`
		StateDir     string `json:"state_dir"`
		CgroupRoot   string `json:"cgroup_root"`
	} `json:"engine"`
	Target struct {
		RunnerID      string `json:"runner_id"`
		ServerID      string `json:"server_id"`
		DestinationID string `json:"destination_id"`
		BindingID     string `json:"binding_id,omitempty"`
	} `json:"target"`
	Policy         domain.HostingBuildPolicy `json:"policy"`
	StatusInterval time.Duration             `json:"status_interval_ns"`
	RequestTimeout time.Duration             `json:"request_timeout_ns"`
}

func loadRunnerBuildConfig(path string) (*runnerruntime.BuildConfig, error) {
	if path == "" {
		return nil, nil
	}
	if strings.TrimSpace(path) != path {
		return nil, errors.New("invalid build configuration path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64<<10 {
		return nil, errors.New("build configuration must be an owned mode0600 bounded regular file")
	}
	// This existing reader supplies the same owned mode0600, no-symlink and
	// 64KiB boundary required for trusted runner configuration. Its result is
	// treated exclusively as nonsecret JSON, never as an authentication token.
	raw, err := readRunnerCredentialFile(path)
	if err != nil {
		return nil, errors.New("build configuration must be an owned mode0600 bounded regular file")
	}
	c, err := decodeRunnerBuildConfig([]byte(raw))
	if err != nil {
		return nil, err
	}
	if err = c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}
func decodeRunnerBuildConfig(raw []byte) (*runnerruntime.BuildConfig, error) {
	invalid := errors.New("invalid build configuration JSON")
	if len(raw) > 64<<10 || !utf8.Valid(raw) {
		return nil, invalid
	}
	shape := json.NewDecoder(bytes.NewReader(raw))
	if checkRunnerBuildJSON(shape, 0) != nil {
		return nil, invalid
	}
	if _, err := shape.Token(); err != io.EOF {
		return nil, invalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var f runnerBuildConfigFile
	if d.Decode(&f) != nil || f.Version != 1 {
		return nil, invalid
	}
	return &runnerruntime.BuildConfig{Compose: f.Compose, PrivateGit: f.PrivateGit, SourceRoot: f.SourceRoot, Engine: hostingbuild.Config{DockerBinary: f.Engine.DockerBinary, Socket: f.Engine.Socket, BuildxBinary: f.Engine.BuildxBinary, BuildxSHA256: f.Engine.BuildxSHA256, PlannerImage: f.Engine.PlannerImage, StateDir: f.Engine.StateDir, CgroupRoot: f.Engine.CgroupRoot}, Target: runnerruntime.BuildTarget{RunnerID: f.Target.RunnerID, ServerID: f.Target.ServerID, DestinationID: f.Target.DestinationID, BindingID: f.Target.BindingID}, Policy: f.Policy, StatusInterval: f.StatusInterval, RequestTimeout: f.RequestTimeout}, nil
}
func checkRunnerBuildJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting exceeds bound")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null build configuration values are forbidden")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[strings.ToLower(name)] {
				return errors.New("duplicate build configuration field")
			}
			// The struct-only schema has ASCII names. Reject Unicode aliases
			// before encoding/json can apply broader Unicode case folding.
			for _, r := range name {
				if r > unicode.MaxASCII {
					return errors.New("non-ASCII build configuration field")
				}
			}
			seen[strings.ToLower(name)] = true
			if err = checkRunnerBuildJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err = checkRunnerBuildJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = d.Token()
	return err
}
func runnerBuildCapabilities(c *runnerruntime.BuildConfig, proxyConfigured, specified bool, capabilities []string) ([]string, error) {
	private := c != nil && c.PrivateGit != nil
	compose := c != nil && c.Compose
	if c != nil && c.Compose != c.Policy.GitCompose {
		return nil, errors.New("git Compose requires matching explicit runner and operator policy")
	}
	for _, capability := range capabilities {
		if (capability == domain.HostingGitComposeBuildCapability || capability == domain.HostingGitComposeArgsCapability) && !compose {
			return nil, errors.New("git Compose capability requires explicit configuration")
		}
	}

	for _, capability := range capabilities {
		if capability == domain.HostingBuildTriggerRevisionCapability && c == nil {
			return nil, errors.New("trigger revision capability requires explicit build configuration")
		}
		if capability == domain.HostingPrivateGitCapability && !private {
			return nil, errors.New("private Git capability requires explicit configuration")
		}
	}
	if c != nil && !specified {
		capabilities = []string{domain.HostingBuildTaskType}
		if proxyConfigured {
			capabilities = append(capabilities, domain.RunTypeProxyApply)
		}
	}
	buildCap, shellCap := false, false
	for _, capability := range capabilities {
		buildCap = buildCap || capability == domain.HostingBuildTaskType
		shellCap = shellCap || capability == "shell"
	}
	if buildCap && c == nil {
		return nil, errors.New("hosting_build_v1 requires explicit validated build configuration")
	}
	if c != nil && shellCap {
		return nil, errors.New("shell and hosting_build_v1 cannot share this configured build runner")
	}
	if c != nil && !buildCap {
		capabilities = append(capabilities, domain.HostingBuildTaskType)
	}
	if c != nil {
		found := false
		for _, capability := range capabilities {
			found = found || capability == domain.HostingBuildTriggerRevisionCapability
		}
		if !found {
			capabilities = append(capabilities, domain.HostingBuildTriggerRevisionCapability)
		}
	}
	if compose {
		for _, needed := range []string{domain.HostingGitComposeBuildCapability, domain.HostingGitComposeArgsCapability} {
			found := false
			for _, capability := range capabilities {
				found = found || capability == needed
			}
			if !found {
				capabilities = append(capabilities, needed)
			}
		}
	}
	if private {
		found := false
		for _, capability := range capabilities {
			found = found || capability == domain.HostingPrivateGitCapability
		}
		if !found {
			capabilities = append(capabilities, domain.HostingPrivateGitCapability)
		}
	}
	return capabilities, nil
}
