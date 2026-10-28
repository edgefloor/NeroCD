//go:build unix

package runnerruntime

import (
	"bytes"
	"nerocd/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestMySQLComponentProtectedGuestCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, password, mode, exit string
		valid                      bool
	}{
		{"generated", strings.Repeat("a", 43), "backup", "0", true},
		{"max", strings.Repeat("_-09Az", 42) + "_-09", "backup", "0", true},
		{"partial", strings.Repeat("b", 43), "restore", "1", true},
		{"timeout", strings.Repeat("b", 43), "restore", "124", true},
		{"quote", strings.Repeat("a", 32) + "\"", "backup", "0", false},
		{"slash", strings.Repeat("a", 32) + "\\", "backup", "0", false},
		{"newline", strings.Repeat("a", 32) + "\nhost=foreign", "backup", "0", false},
		{"short", "short", "backup", "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tool := filepath.Join(root, "client")
			stat := `/usr/bin/stat -c %a "$f"`
			if runtime.GOOS == "darwin" {
				stat = `/usr/bin/stat -f %Lp "$f"`
			}
			fake := `#!/bin/sh
case "$1" in --defaults-extra-file=*) f=${1#*=};; *) exit 90;; esac
test -z "${MYSQL_ROOT_PASSWORD-}" && test -z "${MYSQL_PWD-}" && test -z "${MYSQL_HOME-}"
test "$( ` + stat + ` )" = 600 || exit 91
test "$(sed -n '2p' "$f")" = user=root || exit 92
test "$(sed -n '4p' "$f")" = socket=/var/run/mysqld/mysqld.sock || exit 93
printf 'protected-client\n'
exit ` + tc.exit + "\n"
			if err := os.WriteFile(tool, []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			args := databaseBackupMySQLComponentArgs(tc.mode, "container", 30)
			script := args[slices.Index(args, "-ceu")+1]
			script = strings.ReplaceAll(script, "/tmp/nerocd-component-mysql.XXXXXXXX", filepath.Join(root, "credential.XXXXXXXX"))
			if runtime.GOOS == "darwin" {
				script = strings.ReplaceAll(script, "/usr/bin/rm ", "/bin/rm ")
				script = strings.ReplaceAll(script, "/usr/bin/rmdir ", "/bin/rmdir ")
			}
			script = strings.ReplaceAll(script, "/usr/bin/mysqldump", tool)
			script = strings.ReplaceAll(script, "/usr/bin/mysql ", tool+" ")
			// Only substitute the fixed timeout executable for this portable shell test.
			script = strings.ReplaceAll(script, componentMySQLCleanEnvironment+`/usr/bin/timeout --signal=TERM --kill-after=5s "$1" `, componentMySQLCleanEnvironment)
			command := exec.CommandContext(t.Context(), "/bin/sh", "-ceu", script, "test", "20s")
			command.Env = append(os.Environ(), "MYSQL_DATABASE=wordpress", "MYSQL_ROOT_PASSWORD="+tc.password, "MYSQL_PWD=foreign", "MYSQL_HOME=/foreign")
			raw, err := command.CombinedOutput()
			if tc.valid {
				if err != nil {
					t.Fatal("supported generated credential rejected", err)
				}
				if !bytes.Contains(raw, []byte("protected-client")) {
					t.Fatal("client not invoked")
				}
				if tc.mode == "restore" && !bytes.Contains(raw, []byte(componentImportTerminal+tc.exit)) {
					t.Fatal("joined child status lost")
				}
			} else if err == nil || bytes.Contains(raw, []byte("protected-client")) {
				t.Fatal("unsupported credential reached client")
			}
			if bytes.Contains(raw, []byte(tc.password)) {
				t.Fatal("credential leaked")
			}
			entries, e := os.ReadDir(root)
			if e != nil || len(entries) != 1 {
				t.Fatal("credential temporary material survived", e)
			}
			if strings.Contains(strings.Join(args, " "), tc.password) {
				t.Fatal("password entered argv")
			}
		})
	}
}

func TestMySQLComponentProfileRejectsUnpinnedOrSharedData(t *testing.T) {
	for _, mode := range []string{"image", "mount", "database", "root_expression", "public", "shared", "command"} {
		t.Run(mode, func(t *testing.T) {
			_, p, _, _ := componentBackupEngineFixtureFor(t, "success", true)
			plan := p.SourcePlan
			healthy := p.Source
			switch mode {
			case "image":
				plan.Stack.Images[1].Release.CanonicalImage = "docker.io/library/mysql@sha256:" + strings.Repeat("f", 64)
			case "mount":
				plan.Stack.Definition.Services[1].Mounts[0].Target = "/foreign"
			case "database":
				plan.Stack.Definition.Services[1].Environment["MYSQL_DATABASE"] = "foreign"
			case "root_expression":
				plan.Stack.Definition.Services[1].Environment["MYSQL_ROOT_PASSWORD"] = "plain"
			case "public":
				plan.Stack.Definition.PublicComponent = "mysql"
			case "shared":
				plan.Stack.Definition.Services[0].Mounts = []domain.HostingStackMount{{Volume: "mysql-data", Target: "/data"}}
			case "command":
				plan.Stack.Definition.Services[1].Command = []string{"mysqld", "--skip-grant-tables"}
			}
			// Rehash definition/plan so rejection is caused by profile restrictions too.
			plan.Snapshot.Stack.Hash = domain.HostingDocumentHash(plan.Stack.Definition)
			healthy.Snapshot = plan.Snapshot
			plan.Hash = ""
			plan.Hash = domain.HostingDocumentHash(plan)
			healthy.PlanHash = plan.Hash
			if _, err := domain.HostingMySQLComponent(healthy, plan, "mysql"); err == nil {
				t.Fatal("unsupported MySQL profile accepted")
			}
		})
	}
}
