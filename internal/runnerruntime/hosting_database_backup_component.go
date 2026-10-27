//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// These scripts are fixed source. Guest values are quoted arguments, never shell
// source, and no guest PostgreSQL connection redirection is inherited.
const componentPostgresSelection = `u=${POSTGRES_USER:-postgres}; d=${POSTGRES_DB:-$u}; case "$d" in *=*|postgres://*|postgresql://*) exit 64;; esac; `
const componentPostgresCleanEnvironment = `/usr/bin/env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin PGHOST=/var/run/postgresql PGPORT=5432 PGAPPNAME=nerocd_component_backup LC_ALL=C `
const componentPostgresEnvironment = componentPostgresSelection + `exec ` + componentPostgresCleanEnvironment
const componentImportTerminal = "nerocd-component-import-v1:"

func databaseBackupComponentArgs(mode, id string, seconds int) []string {
	seconds = max(1, seconds-10)
	if mode == "restore" {
		// The fixed shell reports only a numeric terminal result after its child
		// joins. Transport errors or cancellation supply no such proof. Time is an
		// argv value, never interpolated into shell source.
		script := componentPostgresSelection + `set +e; ` + componentPostgresCleanEnvironment + `/usr/bin/timeout -s TERM -k 5 "$1" /usr/local/bin/pg_restore --username="$u" --dbname="$d" --single-transaction --exit-on-error --clean --if-exists --no-owner --no-acl; code=$?; printf 'nerocd-component-import-v1:%s\n' "$code"; exit 0`
		return []string{"exec", "--user", "postgres", "--interactive", id, "/bin/sh", "-ceu", script, "nerocd-import", strconv.Itoa(seconds)}
	}
	return []string{"exec", "--user", "postgres", "--interactive", id, "/usr/bin/timeout", "-s", "TERM", "-k", "5", strconv.Itoa(seconds), "/bin/sh", "-ceu", componentPostgresEnvironment + `/usr/local/bin/pg_dump --username="$u" --dbname="$d" --format=custom --no-owner --no-acl`}
}

func (e *databaseBackupEngine) inspectComponentStack(ctx context.Context, h domain.HostingHealthy, plan domain.HostingApplyPlan, name string) (domain.HostingStackComponent, []domain.HostingStorageArchiveWriterState, error) {
	db, err := domain.HostingPostgresComponent(h, plan, name)
	if err != nil || e.image.verifyDaemon(ctx, h.Release.DaemonID) != nil {
		return db, nil, errDatabaseBackupHeld
	}
	// A copy changes only the verifier's known network inventory. No new plan is
	// persisted or executed and the original immutable plan remains untouched.
	networkPlan := plan
	networkPlan.PreviousHealthy = &h
	network, err := e.image.ensureStackNetwork(ctx, h.OperationID, networkPlan, false)
	if err != nil || network != h.Stack.NetworkID {
		return db, nil, errDatabaseBackupHeld
	}
	volumes, err := e.image.ensureStackVolumes(ctx, h.OperationID, plan, false)
	if err != nil || !reflect.DeepEqual(volumes, h.Stack.Volumes) {
		return db, nil, errDatabaseBackupHeld
	}
	actual, err := e.image.stackContainers(ctx, h.Snapshot.BindingID)
	if err != nil || len(actual) != len(h.Stack.Components) {
		return db, nil, errDatabaseBackupHeld
	}
	known := map[string]domain.HostingStackComponent{}
	for _, c := range h.Stack.Components {
		known[c.ContainerID] = c
	}
	writers := make([]domain.HostingStorageArchiveWriterState, 0, len(actual)-1)
	for _, c := range actual {
		expected, ok := known[c.ID]
		if !ok || expected.Component != c.Labels[stackComponentLabel] || expected.ImageID != c.Image || stackContainerMatches(c, h.OperationID, plan, network) != nil {
			return db, nil, errDatabaseBackupHeld
		}
		if c.ID == db.ContainerID {
			if !c.State.Running {
				return db, nil, errDatabaseBackupHeld
			}
		} else {
			writers = append(writers, domain.HostingStorageArchiveWriterState{ContainerID: c.ID, OperationID: h.OperationID, ImageID: c.Image, PlanHash: plan.Hash, ReleaseHash: plan.Release.Hash, Running: c.State.Running})
		}
	}
	for _, volume := range volumes {
		raw, err := e.image.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+volume.VolumeName)
		if err != nil {
			return db, nil, errDatabaseBackupHeld
		}
		for _, id := range strings.Fields(string(raw)) {
			if _, ok := known[id]; !ok {
				return db, nil, errDatabaseBackupHeld
			}
		}
	}
	version, err := e.image.docker(ctx, "exec", "--user", "postgres", db.ContainerID, "/usr/local/bin/postgres", "--version")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(version)), "postgres (PostgreSQL) 16.") {
		return db, nil, errDatabaseBackupHeld
	}
	// Only inspect nonsecret execution settings. Config.Env and health output
	// never enter this adapter or an ordinary artifact.
	var profile struct {
		Config struct {
			User            string
			Entrypoint, Cmd []string
		}
		HostConfig struct {
			Privileged, PublishAllPorts  bool
			Binds, CapAdd                []string
			PidMode, IpcMode, UsernsMode string
			Devices                      []json.RawMessage
		}
	}
	raw, err := e.image.docker(ctx, "container", "inspect", db.ContainerID, "--format", `{"Config":{"User":{{json .Config.User}},"Entrypoint":{{json .Config.Entrypoint}},"Cmd":{{json .Config.Cmd}}},"HostConfig":{"Privileged":{{json .HostConfig.Privileged}},"PublishAllPorts":{{json .HostConfig.PublishAllPorts}},"Binds":{{json .HostConfig.Binds}},"CapAdd":{{json .HostConfig.CapAdd}},"PidMode":{{json .HostConfig.PidMode}},"IpcMode":{{json .HostConfig.IpcMode}},"UsernsMode":{{json .HostConfig.UsernsMode}},"Devices":{{json .HostConfig.Devices}}}}`)
	if err != nil || json.Unmarshal(raw, &profile) != nil || profile.Config.User != "" || !reflect.DeepEqual(profile.Config.Entrypoint, []string{"docker-entrypoint.sh"}) || !reflect.DeepEqual(profile.Config.Cmd, []string{"postgres"}) || profile.HostConfig.Privileged || profile.HostConfig.PublishAllPorts || len(profile.HostConfig.CapAdd) > 0 || profile.HostConfig.PidMode != "" || profile.HostConfig.UsernsMode != "" || profile.HostConfig.IpcMode != "private" && profile.HostConfig.IpcMode != "" || len(profile.HostConfig.Devices) > 0 {
		return db, nil, errDatabaseBackupHeld
	}
	sort.Slice(writers, func(i, j int) bool { return writers[i].ContainerID < writers[j].ContainerID })
	return db, writers, nil
}

func (e *databaseBackupEngine) componentPreflight(ctx context.Context, p domain.HostingDatabaseBackupPlan) error {
	h, plan, name := p.Source, p.SourcePlan, p.Component.Source
	if p.Operation.Mode == "restore" {
		h, plan, name = *p.Target, *p.TargetPlan, p.Component.Target
	}
	db, writers, err := e.inspectComponentStack(ctx, h, plan, name)
	if err != nil {
		return err
	}
	if p.Operation.Mode == "backup" {
		return nil
	}
	if e.writers != nil && !reflect.DeepEqual(e.writers, writers) {
		return errDatabaseBackupHeld
	}
	f, err := e.archive(ctx, p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if e.stream(ctx, []string{"exec", "--user", "postgres", "--interactive", db.ContainerID, "/usr/bin/timeout", "-s", "TERM", "-k", "5", "20", "/usr/local/bin/pg_restore", "--list"}, f, io.Discard) != nil {
		return errDatabaseBackupHeld
	}
	e.writers = writers
	return nil
}

func (e *databaseBackupEngine) componentImportQuiescent(ctx context.Context, id string) error {
	raw, err := e.image.docker(ctx, "top", id, "-eo", "comm")
	if err != nil {
		return errDatabaseBackupHeld
	}
	for _, command := range strings.Fields(string(raw)) {
		if command == "pg_restore" {
			return errDatabaseBackupHeld
		}
	}
	return nil
}

func (e *databaseBackupEngine) resumeComponentWriters(ctx context.Context, p domain.HostingDatabaseBackupPlan, before func(context.Context) error) error {
	for _, writer := range e.writers {
		if databaseAuthorize(ctx, before) != nil {
			return errDatabaseBackupHeld
		}
		actual, err := e.image.inspectStackContainer(ctx, writer.ContainerID)
		if err != nil || actual.Image != writer.ImageID || stackContainerMatches(actual, writer.OperationID, *p.TargetPlan, p.Target.Stack.NetworkID) != nil {
			return errDatabaseBackupHeld
		}
		if actual.State.Running != writer.Running {
			if !writer.Running {
				return errDatabaseBackupHeld
			}
			if _, err = e.image.mutate(ctx, p.Target.Release.DaemonID, "container", "start", writer.ContainerID); err != nil {
				return errDatabaseBackupHeld
			}
			actual, err = e.image.inspectStackContainer(ctx, writer.ContainerID)
			if err != nil || !actual.State.Running || stackContainerMatches(actual, writer.OperationID, *p.TargetPlan, p.Target.Stack.NetworkID) != nil {
				return errDatabaseBackupHeld
			}
		}
	}
	e.writersResumed = true
	return nil
}

func (e *databaseBackupEngine) componentRestore(ctx context.Context, p domain.HostingDatabaseBackupPlan, before func(context.Context) error) (blob domain.HostingDatabaseBackupBlob, result error) {
	db, _, err := e.inspectComponentStack(ctx, *p.Target, *p.TargetPlan, p.Component.Target)
	if err != nil {
		return blob, err
	}
	// Cleanup has a separate bounded live-authority check. Cancellation or a
	// missing Docker response never grants permission to resume competing SQL writers.
	importAttempted, importJoined := false, false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if importAttempted && !importJoined || e.componentImportQuiescent(cleanup, db.ContainerID) != nil || databaseAuthorize(cleanup, before) != nil {
			result = errors.Join(result, errDatabaseBackupHeld)
			return
		}
		if err := e.resumeComponentWriters(cleanup, p, before); err != nil {
			result = errors.Join(result, err)
		}
	}()
	for _, writer := range e.writers {
		if !writer.Running {
			continue
		}
		if databaseAuthorize(ctx, before) != nil {
			return blob, errDatabaseBackupHeld
		}
		actual, err := e.image.inspectStackContainer(ctx, writer.ContainerID)
		if err != nil || !actual.State.Running || stackContainerMatches(actual, writer.OperationID, *p.TargetPlan, p.Target.Stack.NetworkID) != nil {
			return blob, errDatabaseBackupHeld
		}
		if _, err = e.image.mutate(ctx, p.Target.Release.DaemonID, "container", "stop", "--time", "10", writer.ContainerID); err != nil {
			return blob, errDatabaseBackupHeld
		}
	}
	if databaseAuthorize(ctx, before) != nil {
		return blob, errDatabaseBackupHeld
	}
	for _, writer := range e.writers {
		actual, err := e.image.inspectStackContainer(ctx, writer.ContainerID)
		if err != nil || actual.State.Running || stackContainerMatches(actual, writer.OperationID, *p.TargetPlan, p.Target.Stack.NetworkID) != nil {
			return blob, errDatabaseBackupHeld
		}
	}
	// Unknown SQL clients are not covered by the platform hold or stack inventory.
	clients, err := e.image.docker(ctx, "exec", "--user", "postgres", db.ContainerID, "/bin/sh", "-ceu", componentPostgresEnvironment+`/usr/local/bin/psql --username="$u" --dbname="$d" --no-psqlrc --tuples-only --no-align --command="SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend' AND pid <> pg_backend_pid()"`)
	if err != nil || strings.TrimSpace(string(clients)) != "0" {
		return blob, errDatabaseBackupHeld
	}
	f, err := e.archive(ctx, p)
	if err != nil {
		return blob, err
	}
	defer func() { _ = f.Close() }()
	if databaseAuthorize(ctx, before) != nil {
		return blob, errDatabaseBackupHeld
	}
	var terminal bytes.Buffer
	output := databaseBackupBoundedWriter{w: &terminal, max: 128}
	importAttempted = true
	if err = e.stream(ctx, databaseBackupComponentArgs("restore", db.ContainerID, p.TimeoutSeconds), f, &output); err != nil {
		return blob, errDatabaseBackupHeld
	}
	text := strings.TrimSpace(terminal.String())
	code, parseErr := strconv.Atoi(strings.TrimPrefix(text, componentImportTerminal))
	if !strings.HasPrefix(text, componentImportTerminal) || parseErr != nil || code < 0 || code > 255 || text != componentImportTerminal+strconv.Itoa(code) {
		return blob, errDatabaseBackupHeld
	}
	importJoined = true
	if code != 0 {
		return blob, errDatabaseBackupHeld
	}

	if _, _, err = e.inspectComponentStack(ctx, *p.Target, *p.TargetPlan, p.Component.Target); err != nil {
		return blob, err
	}
	if databaseAuthorize(ctx, before) != nil {
		return blob, errDatabaseBackupHeld
	}
	return *p.Operation.Blob, nil
}
