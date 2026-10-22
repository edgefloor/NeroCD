package hostingbuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (e *Engine) plan(ctx context.Context, dir, source string, r ownedResources, request Request, logs *cappedWriter, observer *outputObserver) error {
	image := e.config.PlannerImage
	if !digestPattern.MatchString(image) && (!strings.Contains(image, "@") || !digestPattern.MatchString(strings.SplitN(image, "@", 2)[1])) {
		return errors.New("immutable trusted Railpack helper image required")
	}
	l := request.Limits
	args := []string{"create", "--pull=never", "--name", r.Planner, "--label", "io.nerocd.hostingbuild.owner=" + r.Token, "--read-only", "--cgroupns=host", "--cgroup-parent", groupRelative(r.Group), "--network", "bridge", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--cpu-period", "100000", "--cpu-quota", strconv.FormatInt(l.CPUQuota/2, 10), "--memory", strconv.FormatInt(l.MemoryBytes/2, 10), "--memory-swap", strconv.FormatInt((l.MemoryBytes+l.SwapBytes)/2, 10), "--pids-limit", "128", "--log-driver", "none", "--tmpfs", "/tmp:rw,exec,nosuid,size=268435456", "--tmpfs", fmt.Sprintf("/out:rw,noexec,nosuid,size=%d", l.PlanBytes), "--mount", "type=bind,src=" + source + ",dst=/source,readonly", "--entrypoint", "/bin/sh", image, "-c", "mkdir -p /tmp/railpack/mise; cp /usr/local/bin/mise /tmp/railpack/mise/mise-2026.9.15; /usr/local/bin/railpack prepare /source --plan-out /out/railpack-plan.json --hide-pretty-plan --info-out /out/info.json > /out/planner.log 2>&1; echo $? > /out/status; exec sleep 86400"}
	if _, err := e.docker(ctx, dir, args...); err != nil {
		return err
	}
	if _, err := e.docker(ctx, dir, "start", r.Planner); err != nil {
		return err
	}
	c, err := e.inspect(ctx, dir, r.Planner)
	if err != nil {
		return err
	}
	if !owned(c, r) {
		return errors.New("planner ownership mismatch")
	}
	group := groupRelative(r.Group)
	actualGroup, inspectErr := containment(c.State.PID, l, r.Group)
	err = inspectErr
	if err == nil && actualGroup != group && !strings.HasPrefix(actualGroup, group+"/") {
		err = errors.New("host PID/cgroup view does not match owned planner placement")
	}
	if err == nil {
		err = checkGroup(r.Group, l)
	}
	if err != nil {
		return err
	}
	ticker := time.NewTicker(l.PollInterval)
	defer ticker.Stop()
	for {
		if !c.State.Running {
			return errors.New("railpack helper exited before output collection")
		}
		if err = verifyDescendants(ctx, c.State.PID, group, l); err != nil {
			return err
		}
		b, readErr := e.docker(ctx, dir, "exec", r.Planner, "cat", "/out/status")
		if readErr == nil {
			if observer != nil {
				// Only the completed planner's merged text is observable. Docker
				// diagnostics and plan/info JSON remain internal.
				logArgs := []string{"--host", "unix://" + e.config.Socket, "--config", filepath.Join(dir, "docker"), "exec", r.Planner, "cat", "/out/planner.log"}
				if err = e.observedCommand(ctx, dir, logs, observer, OutputRailpack, true, e.config.DockerBinary, logArgs...); err != nil {
					return err
				}
			}
			if strings.TrimSpace(string(b)) != "0" {
				return fmt.Errorf("railpack planner failed, exit %s", strings.TrimSpace(string(b)))
			}
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		c, err = e.inspect(ctx, dir, r.Planner)
		if err != nil {
			return err
		}
	}
	var b []byte
	// Docker's archive-copy API does not expose live tmpfs contents on all
	// supported daemons. Read through the running helper with a hard output cap.
	outputCtx, outputCancel := context.WithCancel(ctx)
	defer outputCancel()
	output := &cappedWriter{remaining: l.PlanBytes, cancel: outputCancel}
	args = []string{"--host", "unix://" + e.config.Socket, "--config", filepath.Join(dir, "docker"), "exec", r.Planner, "cat", "/out/railpack-plan.json"}
	if err = e.command(outputCtx, dir, output, e.config.DockerBinary, args...); err != nil {
		return err
	}
	b = output.b.Bytes()
	if err = os.WriteFile(filepath.Join(source, "railpack-plan.json"), b, 0600); err != nil {
		return err
	}
	var plan map[string]any
	if json.Unmarshal(b, &plan) != nil {
		return errors.New("invalid Railpack plan JSON")
	}
	if secrets, ok := plan["secrets"].([]any); ok && len(secrets) > 0 {
		return errors.New("railpack secrets unsupported")
	}
	_, err = logs.Write([]byte("Railpack 0.40.1 plan prepared\n"))
	return err
}
