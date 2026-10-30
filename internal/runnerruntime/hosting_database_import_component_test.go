//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"io"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExternalImportAppCredentialsAndClosedClientCommands(t *testing.T) {
	for _, user := range []string{"app_user", "root", "ROOT", "app\nhost=foreign", "", "app;id"} {
		t.Run(user, func(t *testing.T) {
			root := t.TempDir()
			tool := filepath.Join(root, "client")
			proof := filepath.Join(root, "proof")
			fake := `#!/bin/sh
case "$1" in --defaults-extra-file=*) f=${1#*=};; *) exit 90;; esac
test "$(sed -n '2p' "$f")" = user=app_user || exit 91
test "$(sed -n '4p' "$f")" = socket=/var/run/mysqld/mysqld.sock || exit 92
test -z "${MYSQL_ROOT_PASSWORD-}${MYSQL_PASSWORD-}${MYSQL_USER-}${MYSQL_PWD-}${MYSQL_HOME-}"
for arg in --commands=OFF --binary-mode=1 --local-infile=0 --skip-reconnect --database=wordpress; do
 found=0; for value in "$@"; do test "$value" != "$arg" || found=1; done
 test "$found" = 1 || exit 93
done
printf 'invoked' >"` + proof + `"
cat >/dev/null
exit 1
`
			if err := os.WriteFile(tool, []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			args := databaseExternalMySQLComponentArgs("import", "container", 30)
			script := args[slices.Index(args, "-ceu")+1]
			script = strings.ReplaceAll(script, "/tmp/nerocd-component-app-import.XXXXXXXX", filepath.Join(root, "credential.XXXXXXXX"))
			script = strings.ReplaceAll(script, "/usr/bin/mysql ", tool+" ")
			script = strings.ReplaceAll(script, componentMySQLCleanEnvironment+`/usr/bin/timeout --signal=TERM --kill-after=5s "$1" `, componentMySQLCleanEnvironment)
			if runtime.GOOS == "darwin" {
				script = strings.ReplaceAll(script, "/usr/bin/rm ", "/bin/rm ")
				script = strings.ReplaceAll(script, "/usr/bin/rmdir ", "/bin/rmdir ")
			}
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-ceu", script, "test", "20s")
			cmd.Env = append(os.Environ(), "MYSQL_DATABASE=wordpress", "MYSQL_USER="+user, "MYSQL_PASSWORD="+strings.Repeat("a", 43), "MYSQL_ROOT_PASSWORD="+strings.Repeat("b", 43), "MYSQL_PWD=foreign", "MYSQL_HOME=/foreign")
			cmd.Stdin = strings.NewReader("SELECT 1;")
			out, err := cmd.CombinedOutput()
			if user == "app_user" {
				if err != nil || string(out) != externalImportTerminal+"1\n" {
					t.Fatalf("joined app client status lost: %v", err)
				}
				if raw, e := os.ReadFile(proof); e != nil || string(raw) != "invoked" {
					t.Fatal("client not invoked")
				}
			} else if err == nil {
				t.Fatal("unsafe app identity admitted")
			}
			if strings.Contains(string(out), strings.Repeat("a", 43)) || strings.Contains(string(out), strings.Repeat("b", 43)) {
				t.Fatal("secret reached output")
			}
			entries, e := os.ReadDir(root)
			if e != nil {
				t.Fatal(e)
			}
			for _, v := range entries {
				if strings.HasPrefix(v.Name(), "credential.") {
					t.Fatal("private client material remained")
				}
			}
		})
	}
}

func TestExternalImportPartialSQLKeepsWriterBarrier(t *testing.T) {
	for _, mode := range []string{"success", "SQL failure", "unjoined", "before SQL rejected"} {
		t.Run(mode, func(t *testing.T) {
			e, p := externalImportTestPlan(t)
			_, ip := externalImportTestReady(t, e, p)
			// The existing stack fixture supplies exact identity/stop/start observations.
			_, _, _, _ = e, p, ip, mode
			streamCalls := 0
			e.stream = func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
				script := strings.Join(args, " ")
				if !strings.Contains(script, "user=%s") || !strings.Contains(script, "--commands=OFF") || !strings.Contains(script, "--local-infile=0") {
					t.Fatal("external executor used generated/root client")
				}
				if in == nil {
					return nil
				}
				streamCalls++
				if mode == "unjoined" {
					return errors.New("unjoined tool")
				}
				if _, err := io.Copy(io.Discard, in); err != nil {
					return err
				}
				code := "0"
				if mode == "SQL failure" {
					code = "1"
				}
				_, err := io.WriteString(out, externalImportTerminal+code+"\n")
				return err
			}
			if err := e.externalImportPreflight(t.Context(), ip); err != nil {
				t.Fatal(err)
			}
			before := func(context.Context) error {
				if mode == "before SQL rejected" {
					return errors.New("revoked")
				}
				return nil
			}
			result, err := e.externalImportSQL(t.Context(), ip, strings.NewReader("CREATE TABLE first(id INT); INVALID SQL;"), before)
			if mode == "success" {
				if err != nil || !result.Attempted || !result.Joined || !e.writersResumed {
					t.Fatal("successful import did not resume", err)
				}
			} else if mode == "before SQL rejected" {
				if err == nil || streamCalls != 0 || result.Attempted {
					t.Fatal("revoked import reached SQL")
				}
			} else if err == nil || !result.Attempted || e.writersResumed {
				t.Fatal("partial/unknown SQL resumed writers")
			}
		})
	}
}

// TestExternalImportPinnedClientSecurity uses an explicitly owned network-none
// MySQL target. Only the fixed Docker exec destination is substituted; the actual
// production app-account shell and mysql invocation remain unchanged.
func TestExternalImportPinnedClientSecurity(t *testing.T) {
	id, owner := os.Getenv("NEROCD_EXTERNAL_IMPORT_MYSQL_CONTAINER"), os.Getenv("NEROCD_EXTERNAL_IMPORT_MYSQL_OWNER")
	if id == "" {
		t.Skip("explicit disposable pinned MySQL client target required")
	}
	if !dockerObjectID.MatchString(id) || !strings.HasPrefix(owner, "nerocd-external-import-client-") {
		t.Fatal("invalid client fixture identity")
	}
	docker := func(args []string, input io.Reader) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
		cmd.Stdin = input
		var out strings.Builder
		cmd.Stdout = &databaseBackupBoundedWriter{w: &out, max: 65536}
		cmd.Stderr = io.Discard
		cmd.WaitDelay = time.Second
		err := cmd.Run()
		return []byte(out.String()), err
	}
	identity, err := docker([]string{"inspect", "--format", `{{.Id}} {{index .Config.Labels "nerocd.fixture"}} {{.Config.Image}} {{.HostConfig.NetworkMode}}`, id}, nil)
	pin := "docker.io/library/mysql@sha256:a0225d1ea6631c8bc2385dc4c2faba17bbf1834ecf369e1659a9a382bd8d5dd7"
	if err != nil || string(identity) != id+" "+owner+" "+pin+" none\n" {
		t.Fatal("owned client fixture profile mismatch")
	}
	admin := func(sql string) string {
		t.Helper()
		out, e := docker([]string{"exec", id, "/bin/sh", "-ceu", `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" /usr/bin/mysql --user=root --batch --skip-column-names --database=wordpress --execute="$1"`, "fixture-read", sql}, nil)
		if e != nil {
			t.Fatal("fixed test setup/read failed")
		}
		return strings.TrimSpace(string(out))
	}
	execute := func(sql string) string {
		t.Helper()
		out, e := docker(databaseExternalMySQLComponentArgs("import", id, 30), strings.NewReader(sql))
		if e != nil {
			t.Fatal("app client process did not join")
		}
		return strings.TrimSpace(string(out))
	}
	if got := execute("CREATE TABLE IF NOT EXISTS external_identity(id INT PRIMARY KEY,executor VARCHAR(128)); INSERT INTO external_identity VALUES(1,CURRENT_USER()) ON DUPLICATE KEY UPDATE executor=CURRENT_USER();"); got != externalImportTerminal+"0" {
		t.Fatal("app SQL failed")
	}
	actual := admin("SELECT SUBSTRING_INDEX(executor,'@',1) FROM external_identity WHERE id=1")
	configured, err := docker([]string{"exec", id, "/bin/sh", "-ceu", `printf '%s' "$MYSQL_USER"`}, nil)
	if err != nil || actual != string(configured) || strings.EqualFold(actual, "root") {
		t.Fatal("external SQL did not use configured app account")
	}
	for name, sql := range map[string]string{
		"shell command":  `\! touch /tmp/nerocd-external-command` + "\n",
		"client source":  "source /etc/passwd;\n",
		"cross database": "USE mysql; CREATE TABLE unauthorized(id INT);",
		"global grant":   "GRANT ALL PRIVILEGES ON *.* TO CURRENT_USER();",
		"server file":    "SELECT 'private' INTO OUTFILE '/tmp/nerocd-external-outfile';",
		"local infile":   "CREATE TABLE external_local(line TEXT); LOAD DATA LOCAL INFILE '/etc/passwd' INTO TABLE external_local;",
	} {
		t.Run(name, func(t *testing.T) {
			if got := execute(sql); got != externalImportTerminal+"1" {
				t.Fatal("malicious SQL/client instruction was not rejected")
			}
		})
	}
	if _, err = docker([]string{"exec", id, "/bin/sh", "-ceu", `test ! -e /tmp/nerocd-external-command; test ! -e /tmp/nerocd-external-outfile`}, nil); err != nil {
		t.Fatal("client/server filesystem effect escaped")
	}
	// Real app-user SQL makes a partial durable effect. The existing deterministic
	// component seam observes exact writer stop/resume while executing the real client.
	e, p := externalImportTestPlan(t)
	_, ip := externalImportTestReady(t, e, p)
	e.stream = func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
		index := slices.Index(args, "--interactive")
		if index < 0 || index+1 >= len(args) {
			return errors.New("unexpected external stream shape")
		}
		copyArgs := slices.Clone(args)
		copyArgs[index+1] = id
		data, err := docker(copyArgs, in)
		if err == nil {
			_, err = out.Write(data)
		}
		return err
	}
	if err = e.externalImportPreflight(t.Context(), ip); err != nil {
		t.Fatal("real app preflight rejected")
	}
	path := t.TempDir()
	if os.Chmod(path, 0700) != nil {
		t.Fatal("journal setup")
	}
	j, err := runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	now := time.Now().UTC()
	a := domain.HostingAuthority{OperationID: ip.Operation.ID, RunID: "db_import_" + ip.Operation.ID, LeaseID: "partial_lease", Attempt: 1, Fence: "partial_fence"}
	entry := runner.JournalDatabaseImport{ID: "partial_intent", Attempt: runner.AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: 1, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, ImportPlan: &ip, Request: domain.HostingDatabaseImportMutation{HostingAuthority: a, ReceiptID: "partial_intent", PlanHash: ip.Hash, Phase: "import_intent", DaemonID: p.Target.Release.DaemonID, Writers: e.writers}, CreatedAt: now}
	if _, err = j.AppendDatabaseImport(entry); err != nil {
		t.Fatal(err)
	}
	result, err := e.externalImportSQL(t.Context(), ip, strings.NewReader("CREATE TABLE external_partial(id INT PRIMARY KEY); INSERT INTO external_partial VALUES(7); INVALID SQL;"), func(context.Context) error { return nil })
	if err == nil || !result.Attempted || !result.Joined || e.writersResumed || admin("SELECT COUNT(*) FROM external_partial WHERE id=7") != "1" {
		t.Fatal("actual partial app SQL did not retain stopped-writer boundary")
	}
	entry.ID = "partial_uncertain"
	entry.Request.ReceiptID = entry.ID
	entry.Request.Phase = "uncertain"
	entry.Request.FailureCode = "execution_failed"
	if _, err = j.AppendDatabaseImport(entry); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot().DatabaseImports) != 2 {
		t.Fatal("partial SQL lost durable intent/result")
	}
}
