package hostingproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := (1 << 20) - b.Len()
	if len(p) > left {
		b.overflow = true
		p = p[:left]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func (e *Engine) daemon(ctx context.Context, p Plan) (err error) {
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", e.settings.DockerSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/info", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("fixed Docker daemon unavailable")
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	var v struct{ ID, OSType, Architecture string }
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v) != nil {
		return errors.New("invalid daemon response")
	}
	if v.ID != p.identity.DaemonID || v.OSType != "linux" || (v.Architecture != "aarch64" && v.Architecture != "x86_64" && v.Architecture != "arm64" && v.Architecture != "amd64") {
		return errors.New("docker daemon identity or platform mismatch")
	}
	return nil
}
func (e *Engine) command(ctx context.Context, p Plan, args ...string) ([]byte, error) {
	if err := e.daemon(ctx, p); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	argv := append([]string{"--host", "unix://" + e.settings.DockerSocket, "--config", filepath.Join(e.settings.Directory, "docker-config")}, args...)
	cmd := exec.CommandContext(ctx, e.settings.DockerBinary, argv...)
	cmd.Dir = e.settings.Directory
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + filepath.Join(e.settings.Directory, "docker-config"), "DOCKER_CONFIG=" + filepath.Join(e.settings.Directory, "docker-config")}
	configureCommand(cmd)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker %s failed: %w", args[0], err)
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("docker output exceeded bound")
	}
	return stdout.Bytes(), nil
}

type container struct {
	ID     string
	Config struct {
		Image  string
		Labels map[string]string
		Cmd    []string
		Env    []string
	}
	HostConfig struct {
		NetworkMode, RestartPolicyName string
		RestartPolicy                  struct{ Name string }
		Binds                          []string
	}
	State  struct{ Running bool }
	Mounts []struct {
		Source, Destination string
		RW                  bool
	}
}

func (p Plan) labels() map[string]string {
	return map[string]string{"io.nerocd.proxy": p.identity.ProxyID, "io.nerocd.server": p.identity.ServerID, "io.nerocd.destination": p.identity.DestinationID, "io.nerocd.runner": p.identity.RunnerID, "io.nerocd.daemon": p.identity.DaemonID, "io.nerocd.static": p.staticHash}
}
func (e *Engine) find(ctx context.Context, p Plan) (*container, error) {
	raw, err := e.command(ctx, p, "container", "ls", "--all", "--filter", "name=^/"+p.name()+"$", "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, nil
	}
	raw, err = e.command(ctx, p, "container", "inspect", p.name())
	if err != nil {
		return nil, err
	}
	var cs []container
	if json.Unmarshal(raw, &cs) != nil || len(cs) != 1 {
		return nil, errors.New("invalid container inspect")
	}
	c := &cs[0]
	for k, v := range p.labels() {
		if c.Config.Labels[k] != v {
			return nil, errors.New("foreign proxy container or static settings mismatch")
		}
	}
	if c.Config.Image != Image || c.HostConfig.NetworkMode != "host" || c.HostConfig.RestartPolicy.Name != "unless-stopped" {
		return nil, errors.New("owned proxy runtime configuration mismatch")
	}
	expected := map[string]bool{filepath.Join(e.settings.Directory, "dynamic") + ":/dynamic:false": true, filepath.Join(e.settings.Directory, "certificates") + ":/certificates:true": true}
	if len(c.Mounts) != len(expected) {
		return nil, errors.New("owned proxy mounts mismatch")
	}
	for _, m := range c.Mounts {
		if !expected[fmt.Sprintf("%s:%s:%t", m.Source, m.Destination, m.RW)] {
			return nil, errors.New("owned proxy mounts mismatch")
		}
	}
	if digest(c.Config.Cmd) != digest(e.staticArgs()) {
		return nil, errors.New("owned proxy command mismatch")
	}
	return c, nil
}
func (e *Engine) staticArgs() []string {
	ca := "https://acme-v02.api.letsencrypt.org/directory"
	if e.settings.CA == LetsEncryptStaging {
		ca = "https://acme-staging-v02.api.letsencrypt.org/directory"
	}
	if e.fixtureCA != "" {
		ca = e.fixtureCA
	}
	args := []string{"--global.checknewversion=false", "--global.sendanonymoususage=false", "--api=true", "--api.dashboard=false", "--api.insecure=true", fmt.Sprintf("--entrypoints.traefik.address=127.0.0.1:%d", e.settings.ManagementPort), "--entrypoints.web.address=:80", "--entrypoints.websecure.address=:443", "--providers.file.directory=/dynamic", "--providers.file.watch=true", "--providers.providersthrottleduration=100ms", "--certificatesresolvers.managed.acme.email=" + e.settings.Email, "--certificatesresolvers.managed.acme.storage=/certificates/acme.json", "--certificatesresolvers.managed.acme.caserver=" + ca, "--certificatesresolvers.managed.acme.httpchallenge.entrypoint=web", "--log.level=ERROR"}
	if e.fixtureCA != "" {
		args = append(args, "--certificatesresolvers.managed.acme.certificatesduration=1", "--api.debug=true")
	}
	return args
}
func (e *Engine) launch(ctx context.Context, p Plan) error {
	args := []string{"run", "--detach", "--name", p.name(), "--network", "host", "--restart", "unless-stopped", "--cap-drop", "ALL", "--cap-add", "NET_BIND_SERVICE", "--security-opt", "no-new-privileges:true", "--read-only", "--mount", "type=bind,src=" + filepath.Join(e.settings.Directory, "dynamic") + ",dst=/dynamic,readonly", "--mount", "type=bind,src=" + filepath.Join(e.settings.Directory, "certificates") + ",dst=/certificates"}
	for k, v := range p.labels() {
		args = append(args, "--label", k+"="+v)
	}
	if e.fixtureCA != "" {
		args = append(args, e.fixtureDockerArgs...)
	}
	args = append(args, Image)
	args = append(args, e.staticArgs()...)
	_, err := e.command(ctx, p, args...)
	return err
}
func checkPorts(port uint16) error {
	for _, p := range []uint16{80, 443, port} {
		l, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p))
		if err != nil {
			return fmt.Errorf("required port %d occupied", p)
		}
		_ = l.Close()
	}
	return nil
}
