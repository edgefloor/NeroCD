//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/domain"
)

const externalImportTerminal = "nerocd-external-import-v1:"

// External SQL is never passed to the root account used by generated-dump recovery.
const componentMySQLAppSetup = `case "${MYSQL_DATABASE-}" in wordpress) ;; *) exit 64;; esac
case "${MYSQL_USER-}" in ''|[rR][oO][oO][tT]|*[!A-Za-z0-9_]*) exit 64;; esac
test "${#MYSQL_USER}" -le 32
case "${MYSQL_PASSWORD-}" in ''|*[!A-Za-z0-9_-]*) exit 64;; esac
test "${#MYSQL_PASSWORD}" -ge 32
test "${#MYSQL_PASSWORD}" -le 256
umask 077
d=$(/usr/bin/mktemp -d /tmp/nerocd-component-app-import.XXXXXXXX)
trap '/usr/bin/rm -f -- "$d/client.cnf"; /usr/bin/rmdir -- "$d"' EXIT
printf '[client]\nuser=%s\npassword=%s\nsocket=/var/run/mysqld/mysqld.sock\n' "$MYSQL_USER" "$MYSQL_PASSWORD" >"$d/client.cnf"
unset MYSQL_ROOT_PASSWORD MYSQL_PASSWORD MYSQL_DATABASE MYSQL_USER MYSQL_PWD MYSQL_HOME
`

func databaseExternalMySQLComponentArgs(mode, id string, seconds int) []string {
	if seconds < 11 || seconds > 3600 || mode != "probe" && mode != "import" {
		return nil
	}
	command := `/usr/bin/mysql --defaults-extra-file="$d/client.cnf" --database=wordpress --commands=OFF --binary-mode=1 --local-infile=0 --skip-reconnect --batch --skip-column-names`
	if mode == "probe" {
		command += ` --execute='SELECT 1'`
	}
	script := componentMySQLAppSetup + `set +e
` + componentMySQLCleanEnvironment + `/usr/bin/timeout --signal=TERM --kill-after=5s "$1" ` + command + ` >/dev/null
code=$?
set -e
/usr/bin/rm -- "$d/client.cnf"
/usr/bin/rmdir -- "$d"
trap - EXIT
`
	if mode == "import" {
		script += `printf 'nerocd-external-import-v1:%s\n' "$code"
exit 0
`
	} else {
		script += `exit "$code"
`
	}
	return componentMySQLExec(id, true, "/bin/sh", "-ceu", script, "nerocd-app-mysql-import", strconv.Itoa(seconds-10)+"s")
}

type externalImportSQLResult struct {
	Attempted, Joined bool
}

// externalImportPreflight verifies actual app-account access without stopping writers.
func (e *databaseBackupEngine) externalImportPreflight(ctx context.Context, p domain.HostingDatabaseImportPlan) error {
	if p.Validate() != nil {
		return databaseImportError("authority_denied")
	}
	target := p.StagePlan.Target
	db, writers, err := e.inspectComponentStack(ctx, target, p.StagePlan.TargetPlan, "mysql")
	if err != nil || e.stream(ctx, databaseExternalMySQLComponentArgs("probe", db.ContainerID, 20), nil, io.Discard) != nil {
		return databaseImportError("stage_unavailable")
	}
	e.writers = writers
	return nil
}

// externalImportSQL streams the retained stage descriptor after durable intent.
// A nonzero MySQL result can contain committed DDL, so it never resumes writers.
func (e *databaseBackupEngine) externalImportSQL(ctx context.Context, p domain.HostingDatabaseImportPlan, source io.Reader, before func(context.Context) error) (out externalImportSQLResult, result error) {
	if p.Validate() != nil || source == nil || databaseAuthorize(ctx, before) != nil {
		return out, databaseImportError("authority_denied")
	}
	target, plan := p.StagePlan.Target, p.StagePlan.TargetPlan
	db, writers, err := e.inspectComponentStack(ctx, target, plan, "mysql")
	if err != nil || !reflect.DeepEqual(writers, e.writers) {
		return out, databaseImportError("execution_failed")
	}
	defer func() {
		if out.Attempted && (!out.Joined || result != nil) {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if e.componentImportQuiescent(cleanup, db.ContainerID, true) != nil || databaseAuthorize(cleanup, before) != nil {
			result = errors.Join(result, databaseImportError("execution_failed"))
			return
		}
		if e.resumeVerifiedComponentWriters(cleanup, target, plan, before) != nil {
			result = errors.Join(result, databaseImportError("execution_failed"))
		}
	}()
	if err = e.stopVerifiedComponentWriters(ctx, target, plan, before); err != nil {
		return out, databaseImportError("execution_failed")
	}
	clients, err := e.image.docker(ctx, databaseBackupMySQLComponentArgs("clients", db.ContainerID, 20)...)
	if err != nil || strings.TrimSpace(string(clients)) != "0" || databaseAuthorize(ctx, before) != nil {
		return out, databaseImportError("execution_failed")
	}
	var terminal bytes.Buffer
	out.Attempted = true
	if e.stream(ctx, databaseExternalMySQLComponentArgs("import", db.ContainerID, p.StagePlan.TimeoutSeconds), source, &databaseBackupBoundedWriter{w: &terminal, max: 128}) != nil {
		return out, databaseImportError("execution_failed")
	}
	text := strings.TrimSpace(terminal.String())
	code, err := strconv.Atoi(strings.TrimPrefix(text, externalImportTerminal))
	if err != nil || code < 0 || code > 255 || text != externalImportTerminal+strconv.Itoa(code) {
		return out, databaseImportError("execution_failed")
	}
	out.Joined = true
	if code != 0 {
		return out, databaseImportError("execution_failed")
	}
	if _, _, err = e.inspectComponentStack(ctx, target, plan, "mysql"); err != nil || databaseAuthorize(ctx, before) != nil {
		return out, databaseImportError("execution_failed")
	}
	return out, nil
}

// External imports share stack identity checks, but never open or adopt backup CAS.
func openExternalImportEngine(ctx context.Context, c DatabaseExecutionConfig, endpoint string) (*databaseBackupEngine, error) {
	if !c.ExternalImports {
		return nil, databaseImportError("authority_denied")
	}
	host, err := NewDatabaseEngineConfig(c.StateRoot, c.SecretRoot)
	if err != nil {
		return nil, databaseImportError("stage_unavailable")
	}
	image, err := newImageEngine(endpoint)
	if err != nil {
		return nil, databaseImportError("stage_unavailable")
	}
	if validateDatabaseDaemon(ctx, image, host) != nil {
		_ = image.Close()
		return nil, databaseImportError("authority_denied")
	}
	e := &databaseBackupEngine{image: image, host: host}
	e.stream = func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
		all := append([]string{"--host", image.endpoint, "--config", image.configDir}, args...)
		return archiveOSStream(ctx, "docker", all, image.configDir, in, out)
	}
	return e, nil
}
