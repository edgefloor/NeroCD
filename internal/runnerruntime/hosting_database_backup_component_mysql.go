//go:build unix

package runnerruntime

import "strconv"

// The pinned catalog generates the same bounded ASCII credentials as the native
// recipe. Reject unsupported bytes before creating a file or starting a client.
// Password bytes remain in the guest and never become command arguments/output.
const componentMySQLSetup = `case "${MYSQL_DATABASE-}" in wordpress) ;; *) exit 64;; esac
case "${MYSQL_ROOT_PASSWORD-}" in ''|*[!A-Za-z0-9_-]*) exit 64;; esac
test "${#MYSQL_ROOT_PASSWORD}" -ge 32
test "${#MYSQL_ROOT_PASSWORD}" -le 256
umask 077
d=$(/usr/bin/mktemp -d /tmp/nerocd-component-mysql.XXXXXXXX)
trap '/usr/bin/rm -f -- "$d/client.cnf"; /usr/bin/rmdir -- "$d"' EXIT
printf '[client]\nuser=root\npassword=%s\nsocket=/var/run/mysqld/mysqld.sock\n' "$MYSQL_ROOT_PASSWORD" >"$d/client.cnf"
unset MYSQL_ROOT_PASSWORD MYSQL_PASSWORD MYSQL_DATABASE MYSQL_USER MYSQL_PWD MYSQL_HOME
`

const componentMySQLCleanEnvironment = `/usr/bin/env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C `

func componentMySQLExec(id string, interactive bool, command ...string) []string {
	args := componentDatabaseExec(id, interactive, command...)
	args[2] = "mysql"
	return args
}

func databaseBackupMySQLComponentArgs(mode, id string, seconds int) []string {
	seconds = max(1, seconds-10)
	client := `/usr/bin/mysql --defaults-extra-file="$d/client.cnf" --database=wordpress --binary-mode=1 --local-infile=0 --skip-reconnect --batch --skip-column-names`
	command := client
	switch mode {
	case "backup":
		command = `/usr/bin/mysqldump --defaults-extra-file="$d/client.cnf" --single-transaction --quick --skip-lock-tables --no-tablespaces --set-gtid-purged=OFF --routines --events --triggers --hex-blob --column-statistics=0 wordpress`
	case "probe":
		command += ` --execute='SELECT 1'`
	case "engine":
		command += ` --execute="SELECT COUNT(*) FROM information_schema.tables WHERE TABLE_SCHEMA='wordpress' AND TABLE_TYPE='BASE TABLE' AND ENGINE <> 'InnoDB'"`
	case "clients":
		command += ` --execute="SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID() AND USER <> 'event_scheduler'"`
	case "restore":
	default:
		return nil
	}
	script := componentMySQLSetup + `set +e
` + componentMySQLCleanEnvironment + `/usr/bin/timeout --signal=TERM --kill-after=5s "$1" ` + command + `
code=$?
set -e
/usr/bin/rm -- "$d/client.cnf"
/usr/bin/rmdir -- "$d"
trap - EXIT
`
	if mode == "restore" {
		script += `printf 'nerocd-component-import-v1:%s\n' "$code"
exit 0
`
	} else {
		script += `exit "$code"
`
	}
	return componentMySQLExec(id, true, "/bin/sh", "-ceu", script, "nerocd-component-mysql", strconv.Itoa(seconds)+"s")
}
