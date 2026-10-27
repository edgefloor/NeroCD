//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestComponentDatabaseGuestValuesAreArguments(t *testing.T) {
	for _, tc := range []struct{ name, user, database, wantUser, wantDatabase string }{
		{name: "defaults", wantUser: "postgres", wantDatabase: "postgres"},
		{name: "user default database", user: "literal$cash${NO_EXPAND}", wantUser: "literal$cash${NO_EXPAND}", wantDatabase: "literal$cash${NO_EXPAND}"},
		{name: "explicit database", user: "custom", database: "database with spaces$cash", wantUser: "custom", wantDatabase: "database with spaces$cash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A fake tool records argv. The production shell source is used unchanged;
			// only its fixed executable is substituted for this local argument test.
			path := filepath.Join(t.TempDir(), "argv")
			if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$PGHOST\" \"${PGOPTIONS-unset}\" \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			args := databaseBackupComponentArgs("backup", strings.Repeat("a", 64), 30)
			script := strings.Replace(args[len(args)-1], "/usr/local/bin/pg_dump ", path+" ", 1)
			command := exec.CommandContext(t.Context(), "sh", "-ceu", script)
			command.Env = append(os.Environ(), "POSTGRES_USER="+tc.user, "POSTGRES_DB="+tc.database, "PGHOST=foreign", "PGOPTIONS=-c attacker=value")
			raw, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			values := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if !slices.Equal(values, []string{"/var/run/postgresql", "unset", "--username=" + tc.wantUser, "--dbname=" + tc.wantDatabase, "--format=custom", "--no-owner", "--no-acl"}) {
				t.Fatalf("guest arguments changed: %q", values)
			}
		})
	}
	for _, database := range []string{"host=foreign dbname=app", "postgres://foreign/app", "postgresql://foreign/app"} {
		command := exec.CommandContext(t.Context(), "sh", "-ceu", componentPostgresEnvironment+"true")
		command.Env = append(os.Environ(), "POSTGRES_DB="+database)
		if err := command.Run(); err == nil {
			t.Fatal("libpq connection string database accepted")
		}
	}
	for _, name := range []string{"LD_PRELOAD=", "LD_LIBRARY_PATH=", "ENV=", "BASH_ENV="} {
		args := componentDatabaseExec(strings.Repeat("a", 64), false, "/usr/local/bin/postgres", "--version")
		if index := slices.Index(args, name); index < 1 || args[index-1] != "--env" {
			t.Fatal("guest startup override inherited", name)
		}
	}
	restore := databaseBackupComponentArgs("restore", strings.Repeat("a", 64), 30)
	for _, flag := range []string{"--single-transaction", "--exit-on-error", "--clean", "--if-exists", "--no-owner", "--no-acl"} {
		if !strings.Contains(restore[slices.Index(restore, "-ceu")+1], flag) {
			t.Fatal("missing replacement bound", flag)
		}
	}
}

func componentBackupEngineFixture(t *testing.T, mode string) (*databaseBackupEngine, domain.HostingDatabaseBackupPlan, *map[string]bool, *int) {
	t.Helper()
	e, _, _ := backupEngineFixture(t)
	stop := stackStopTestPlan(t)
	plan, h := stop.TargetPlan, stop.Target
	volume := domain.HostingStackVolume{Name: "pgdata", VolumeName: domain.HostingStackObjectID(plan.Snapshot.BindingID, "volume", "pgdata"), CreationOperationID: h.OperationID, CreationNonce: strings.Repeat("a", 32), CreatedAt: "2026-10-27T00:00:00Z"}
	plan.Stack.Definition.Volumes = []string{"pgdata"}
	plan.Stack.Definition.Services[1].Mounts = []domain.HostingStackMount{{Volume: "pgdata", Target: "/var/lib/postgresql/data"}}
	plan.Stack.Volumes = []domain.HostingStackVolume{volume}
	plan.Snapshot.Stack.Hash = domain.HostingDocumentHash(plan.Stack.Definition)
	plan.Hash = ""
	plan.Hash = domain.HostingDocumentHash(plan)
	h.Snapshot, h.PlanHash, h.Stack.Volumes = plan.Snapshot, plan.Hash, []domain.HostingStackVolume{volume}
	p := domain.HostingDatabaseBackupPlan{Component: &domain.HostingDatabaseBackupComponentPlan{Source: "db"}, Operation: domain.HostingDatabaseBackupOperation{ID: "component_backup", Version: domain.HostingDatabaseBackupComponentVersion, ProjectID: "project", SourceResourceID: h.Snapshot.ResourceID, SourceGeneration: h.Snapshot.ResourceGeneration, SourceComponent: "db", SourceDeployedOperationID: h.OperationID, BackupID: "component_backup", Mode: "backup", State: "queued", OwnershipHeld: true, MaxBytes: 1 << 20, TimeoutSeconds: 30}, ActorID: "actor", RunnerID: h.Snapshot.RunnerID, Source: h, SourcePlan: plan, LocationID: "component_backup", MaxBytes: 1 << 20, TimeoutSeconds: 30}
	p.Hash = domain.HostingDocumentHash(p)
	if err := p.Validate(); err != nil {
		t.Fatal("component plan", err)
	}
	running := map[string]bool{}
	for _, c := range h.Stack.Components {
		running[c.ContainerID] = true
	}
	mutations := 0
	e.image.command = imageCommandFunc(func(ctx context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		a := args[4:]
		switch a[0] {
		case "info":
			return json.Marshal(h.Release.DaemonID)
		case "network":
			if a[1] == "ls" {
				return []byte(h.Stack.NetworkID), nil
			}
			users := map[string]any{}
			for _, c := range h.Stack.Components {
				users[c.ContainerID] = map[string]any{}
			}
			if mode == "foreign network" {
				users[strings.Repeat("f", 64)] = map[string]any{}
			}
			return json.Marshal(map[string]any{"Id": h.Stack.NetworkID, "Name": plan.Stack.NetworkName, "Driver": "bridge", "Scope": "local", "Labels": map[string]string{imageBindingLabel: h.Snapshot.BindingID}, "Containers": users})
		case "volume":
			if a[1] == "ls" {
				return []byte(volume.VolumeName), nil
			}
			return json.Marshal(map[string]any{"Name": volume.VolumeName, "Driver": "local", "Scope": "local", "CreatedAt": volume.CreatedAt, "Labels": map[string]string{imageBindingLabel: h.Snapshot.BindingID, stackVolumeLabel: volume.Name, stackOriginLabel: volume.CreationOperationID, stackNonceLabel: volume.CreationNonce}})
		case "container":
			switch a[1] {
			case "ls":
				ids := []string{}
				for _, c := range h.Stack.Components {
					if !strings.Contains(strings.Join(a, " "), "volume=") || c.Component == "db" {
						ids = append(ids, c.ContainerID)
					}
				}
				if mode == "foreign volume" && strings.Contains(strings.Join(a, " "), "volume=") {
					ids = append(ids, strings.Repeat("f", 64))
				}
				return []byte(strings.Join(ids, "\n")), nil
			case "inspect":
				if strings.Contains(a[len(a)-1], "Entrypoint") {
					return json.Marshal(map[string]any{"Config": map[string]any{"Entrypoint": []string{"docker-entrypoint.sh"}, "Cmd": []string{"postgres"}}, "HostConfig": map[string]any{"Privileged": mode == "privileged"}})
				}
				for _, c := range h.Stack.Components {
					if c.ContainerID != a[2] {
						continue
					}
					ports := map[string]any{}
					mounts := []any{}
					image := c.ImageID
					if c.Component == "app" {
						ports["80/tcp"] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "18080"}}
					} else {
						mounts = append(mounts, map[string]any{"Type": "volume", "Name": volume.VolumeName, "Destination": "/var/lib/postgresql/data", "RW": true})
						if mode == "wrong image" {
							image = "sha256:" + strings.Repeat("f", 64)
						}
					}
					return json.Marshal(map[string]any{"Id": c.ContainerID, "Image": image, "Labels": map[string]string{imageBindingLabel: h.Snapshot.BindingID, imageOperationLabel: h.OperationID, imagePlanLabel: plan.Hash, imageReleaseLabel: plan.Release.Hash, stackComponentLabel: c.Component}, "State": map[string]any{"Running": running[c.ContainerID]}, "Mounts": mounts, "Networks": map[string]any{plan.Stack.NetworkName: map[string]any{"NetworkID": h.Stack.NetworkID, "Aliases": []string{c.Component}}}, "Ports": ports})
				}
			case "stop":
				mutations++
				running[a[len(a)-1]] = false
				return nil, nil
			case "start":
				mutations++
				running[a[len(a)-1]] = true
				return nil, nil
			}
		case "exec":
			if strings.Contains(strings.Join(a, " "), "--version") {
				if mode == "wrong major" {
					return []byte("postgres (PostgreSQL) 17.6"), nil
				}
				return []byte("postgres (PostgreSQL) 16.11"), nil
			}
			if mode == "foreign SQL writer" {
				return []byte("1"), nil
			}
			return []byte("0"), nil
		case "top":
			if mode == "unresolved import" {
				return []byte("COMMAND\npg_restore\n"), nil
			}
			return []byte("COMMAND\npostgres\n"), nil
		}
		return nil, errors.New("unexpected component fixture command")
	})
	e.stream = func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
		if mode == "unsupported local authentication" && strings.Contains(args[len(args)-1], "psql ") {
			return errors.New("local authentication rejected")
		}
		if slices.Contains(args, "--list") {
			if mode == "corrupt archive" {
				return errors.New("invalid archive")
			}
			return nil
		}
		if slices.Contains(args, "nerocd-import") {
			if mode == "unresolved import" || mode == "late launch" || mode == "cancelled import" {
				return errors.New("tool failed")
			}
			code := "0"
			if mode == "restore failure" {
				code = "1"
			}
			if mode == "tool timeout" {
				code = "124"
			}
			_, err := io.WriteString(out, componentImportTerminal+code+"\n")
			return err
		}
		_, err := out.Write(append([]byte("PGDMP"), bytes.Repeat([]byte("x"), 100)...))
		return err
	}
	return e, p, &running, &mutations
}

func TestComponentDatabaseProfileAndImportWriterBarrier(t *testing.T) {
	for _, mode := range []string{"success", "wrong image", "wrong major", "unsupported local authentication", "privileged", "foreign network", "foreign volume", "foreign SQL writer", "restore failure", "unresolved import", "late launch", "cancelled import", "corrupt archive", "tool timeout", "revoked authority", "already stopped writer"} {
		t.Run(mode, func(t *testing.T) {
			e, p, running, mutations := componentBackupEngineFixture(t, mode)
			blob, err := e.run(t.Context(), p, func(context.Context) error { return nil })
			reject := slices.Contains([]string{"wrong image", "wrong major", "unsupported local authentication", "privileged", "foreign network", "foreign volume"}, mode)
			if reject {
				if err == nil || *mutations != 0 {
					t.Fatal("unsupported profile mutated", err, *mutations)
				}
				return
			}
			if err != nil {
				t.Fatal("source dump", err)
			}
			target, targetPlan := p.Source, p.SourcePlan
			source, sourcePlan := p.Source, p.SourcePlan
			source.Snapshot.ResourceID = "resource_source"
			source.Snapshot.BindingID = "binding_source"
			source.OperationID = "hop_source"
			sourceState := *source.Stack
			sourceState.NetworkID = strings.Repeat("f", 64)
			sourceState.Components = append([]domain.HostingStackComponent(nil), sourceState.Components...)
			for i := range sourceState.Components {
				sourceState.Components[i].ContainerID = strings.Repeat(string(rune('e'+i)), 64)
				sourceState.Components[i].OperationID = source.OperationID
			}
			sourceState.Volumes = append([]domain.HostingStackVolume(nil), sourceState.Volumes...)
			for i := range sourceState.Volumes {
				sourceState.Volumes[i].VolumeName = domain.HostingStackObjectID(source.Snapshot.BindingID, "volume", sourceState.Volumes[i].Name)
				sourceState.Volumes[i].CreationOperationID = source.OperationID
			}
			source.Stack = &sourceState
			sourceStack := *sourcePlan.Stack
			sourceStack.Volumes = sourceState.Volumes
			sourceStack.NetworkName = domain.HostingStackObjectID(source.Snapshot.BindingID, "network", "private")
			sourcePlan.Stack = &sourceStack
			sourcePlan.Snapshot = source.Snapshot
			sourcePlan.Hash = ""
			sourcePlan.Hash = domain.HostingDocumentHash(sourcePlan)
			source.PlanHash = sourcePlan.Hash
			p.Source, p.SourcePlan = source, sourcePlan
			p.Operation.SourceResourceID, p.Operation.SourceDeployedOperationID = source.Snapshot.ResourceID, source.OperationID
			p.Target, p.TargetPlan = &target, &targetPlan
			p.Operation.ID = "component_restore"
			p.Operation.Mode = "restore"
			p.Operation.BackupID = "component_backup"
			p.Operation.TargetResourceID = p.Target.Snapshot.ResourceID
			p.Operation.TargetComponent = "db"
			p.Operation.TargetDeployedOperationID = p.Target.OperationID
			p.Operation.Blob = &blob
			p.Component = &domain.HostingDatabaseBackupComponentPlan{Source: "db", Target: "db"}
			p.Hash = ""
			p.Hash = domain.HostingDocumentHash(p)
			if mode == "already stopped writer" {
				for _, c := range p.Target.Stack.Components {
					if c.Component == "app" {
						(*running)[c.ContainerID] = false
					}
				}
			}
			if err = p.Validate(); err != nil {
				t.Fatal("restore plan", err)
			}
			calls := 0
			_, err = e.run(t.Context(), p, func(context.Context) error {
				calls++
				if mode == "revoked authority" && calls > 2 {
					return errors.New("revoked")
				}
				return nil
			})
			if mode == "corrupt archive" {
				if err == nil || *mutations != 0 {
					t.Fatal("corrupt archive changed writers", err, *mutations)
				}
				return
			}
			wantSuccess := mode == "success" || mode == "already stopped writer"
			if (err == nil) != wantSuccess {
				t.Fatal("import outcome", err)
			}
			if mode == "unresolved import" || mode == "late launch" || mode == "cancelled import" || mode == "revoked authority" {
				if e.writersResumed {
					t.Fatal("uncertain authority resumed writers")
				}
			} else if !e.writersResumed {
				t.Fatal("joined import failed to resume prior writers")
			}
			if mode == "already stopped writer" && *mutations != 0 {
				t.Fatal("originally stopped writer was changed")
			}
			if mode == "success" && *mutations != 2 {
				t.Fatal("missing physical writer barrier", *mutations)
			}
		})
	}
}
