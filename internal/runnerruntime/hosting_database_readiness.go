//go:build unix

package runnerruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
)

var errDatabaseReadiness = errors.New("database authenticated readiness not confirmed")

// All protocol errors remain static. Driver diagnostics, SQL parameter values,
// authentication payloads, endpoint addresses, and credential verifiers are not
// returned to the runner's result/event channel.
func databasePostgresConnect(ctx context.Context, address, user string, password []byte) (*pgx.Conn, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return nil, errDatabaseReadiness
	}
	config, err := pgx.ParseConfig("host=127.0.0.1 port=5432 dbname=app user=ncd_app sslmode=disable")
	if err != nil {
		return nil, errDatabaseReadiness
	}
	config.Host = address
	config.User = user
	config.Password = string(password)
	config.TLSConfig = nil
	config.Fallbacks = nil
	config.ConnectTimeout = 2 * time.Second
	config.RuntimeParams = map[string]string{"application_name": "nerocd-private-readiness", "search_path": "pg_catalog", "statement_timeout": "2000", "lock_timeout": "1000"}
	conn, err := pgx.ConnectConfig(ctx, config)
	config.Password = ""
	if err != nil {
		return nil, errDatabaseReadiness
	}
	return conn, nil
}

func databasePostgresApp(ctx context.Context, address string, values []DatabaseCredentialValue) error {
	conn, err := databasePostgresConnect(ctx, address, "ncd_app", databasePassword(values, "app"))
	if err != nil {
		return errDatabaseReadiness
	}
	defer func() { _ = conn.Close(ctx) }()
	var one int
	if conn.QueryRow(ctx, "SELECT 1").Scan(&one) != nil || one != 1 {
		return errDatabaseReadiness
	}
	return nil
}

// The password is bound as a protocol parameter, never concatenated into client
// SQL. The fixed DO block catches dynamic-statement diagnostics before they can
// expose a password in server context. Server logging parameters are also fixed
// by the recipe. Existing roles are never altered, including during recovery.
const databasePostgresCreateRole = `DO $ncd$
BEGIN
 IF EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'ncd_app') THEN
  RAISE EXCEPTION 'application role already exists';
 END IF;
 EXECUTE pg_catalog.format('CREATE ROLE ncd_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD %L', pg_catalog.current_setting('nerocd.init_password'));
EXCEPTION WHEN OTHERS THEN
 RAISE EXCEPTION 'private database initialization refused' USING ERRCODE = 'P0001';
END $ncd$;`

func databasePostgresInitialize(ctx context.Context, address string, values []DatabaseCredentialValue, before func(context.Context) error) error {
	// An already-authenticated app recognizes a committed transaction even when
	// its response/local ready marker was lost. This path performs no SQL writes.
	if databasePostgresApp(ctx, address, values) == nil {
		return nil
	}
	conn, err := databasePostgresConnect(ctx, address, "postgres", databasePassword(values, "admin"))
	if err != nil {
		return errDatabaseReadiness
	}
	defer func() { _ = conn.Close(ctx) }()
	var exists bool
	if conn.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'ncd_app')").Scan(&exists) != nil || exists {
		return errors.New("database initialization requires exact absent application role")
	}
	if err = databaseAuthorize(ctx, before); err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return errDatabaseReadiness
	}
	if err = databasePostgresRoleTransaction(ctx, tx, values, before); err != nil {
		return err
	}
	return databasePostgresApp(ctx, address, values)
}

func databasePostgresRoleTransaction(ctx context.Context, tx pgx.Tx, values []DatabaseCredentialValue, before func(context.Context) error) (outcome error) {
	var err error
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if err := tx.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			outcome = errDatabaseReadiness
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_catalog.set_config('nerocd.init_password', $1, true)", string(databasePassword(values, "app"))); err != nil {
		return errDatabaseReadiness
	}
	for _, sql := range []string{databasePostgresCreateRole, "GRANT ALL ON DATABASE app TO ncd_app", "GRANT USAGE, CREATE ON SCHEMA public TO ncd_app"} {
		if err = databaseAuthorize(ctx, before); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, sql); err != nil {
			return errDatabaseReadiness
		}
	}
	if err = databaseAuthorize(ctx, before); err != nil {
		return err
	}
	if tx.Commit(ctx) != nil {
		return errDatabaseReadiness
	}
	return nil
}

func databaseRESP(ctx context.Context, address string, user string, password []byte, commands [][]string) ([][]byte, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.IsPrivate() || !ip.Is4() {
		return nil, errDatabaseReadiness
	}
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(address, "6379"))
	if err != nil {
		return nil, errDatabaseReadiness
	}
	return databaseRESPExchange(ctx, conn, user, password, commands)
}

func databaseRESPExchange(ctx context.Context, conn net.Conn, user string, password []byte, commands [][]string) ([][]byte, error) {
	var err error
	defer func() { _ = conn.Close() }()
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(joined) })
	defer func() {
		if !stop() {
			<-joined
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if conn.SetDeadline(deadline) != nil {
		return nil, errDatabaseReadiness
	}
	reader := bufio.NewReaderSize(conn, 1024)
	// AUTH is written as protected bytes, not a URI, argv, config, or log value.
	if _, err = fmt.Fprintf(conn, "*3\r\n$4\r\nAUTH\r\n$%d\r\n%s\r\n$%d\r\n", len(user), user, len(password)); err != nil {
		return nil, errDatabaseReadiness
	}
	if _, err = conn.Write(password); err != nil {
		return nil, errDatabaseReadiness
	}
	if _, err = io.WriteString(conn, "\r\n"); err != nil {
		return nil, errDatabaseReadiness
	}
	response, err := databaseRESPValue(reader)
	if err != nil || !bytes.Equal(response, []byte("OK")) {
		return nil, errDatabaseReadiness
	}
	var replies [][]byte
	for _, command := range commands {
		if ctx.Err() != nil {
			return nil, errDatabaseReadiness
		}
		if _, err = fmt.Fprintf(conn, "*%d\r\n", len(command)); err != nil {
			return nil, errDatabaseReadiness
		}
		for _, arg := range command {
			if _, err = fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
				return nil, errDatabaseReadiness
			}
		}
		value, err := databaseRESPValue(reader)
		if err != nil {
			return nil, errDatabaseReadiness
		}
		replies = append(replies, value)
	}
	return replies, nil
}

func databaseRESPValue(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) < 3 || len(line) > 128 || line[len(line)-2] != '\r' {
		return nil, errDatabaseReadiness
	}
	value := line[1 : len(line)-2]
	if line[0] == '+' {
		return bytes.Clone(value), nil
	}
	if line[0] != '$' {
		return nil, errDatabaseReadiness
	}
	length, err := strconv.Atoi(string(value))
	if err != nil || length < 0 || length > 1024 {
		return nil, errDatabaseReadiness
	}
	raw := make([]byte, length+2)
	if _, err = io.ReadFull(reader, raw); err != nil || raw[length] != '\r' || raw[length+1] != '\n' {
		return nil, errDatabaseReadiness
	}
	return raw[:length], nil
}

func databaseRedisReady(ctx context.Context, address string, values []DatabaseCredentialValue, readOnly bool, before func(context.Context) error) error {
	return databaseRedisProbe(ctx, address, values, readOnly, before, databaseRESP)
}

func databaseRedisProbe(ctx context.Context, address string, values []DatabaseCredentialValue, readOnly bool, before func(context.Context) error, exchange func(context.Context, string, string, []byte, [][]string) ([][]byte, error)) error {
	probe, err := exchange(ctx, address, "ncd_probe", databasePassword(values, "probe"), [][]string{{"PING"}})
	if err != nil || len(probe) != 1 || !bytes.Equal(probe[0], []byte("PONG")) {
		return errDatabaseReadiness
	}
	commands := [][]string{{"PING"}}
	var marker string
	if !readOnly {
		var nonce [32]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return errDatabaseReadiness
		}
		marker = hex.EncodeToString(nonce[16:])
		// 128 random bits and NX prevent overwriting any existing user key. The
		// reserved probe expires without DEL (which would be an additional mutation).
		key := "__nerocd_probe:" + hex.EncodeToString(nonce[:16])
		if err = databaseAuthorize(ctx, before); err != nil {
			return err
		}
		commands = [][]string{{"SET", key, marker, "EX", "10", "NX"}, {"GET", key}}
	}
	replies, err := exchange(ctx, address, "ncd_app", databasePassword(values, "app"), commands)
	if err != nil {
		return errDatabaseReadiness
	}
	if readOnly {
		if len(replies) != 1 || !bytes.Equal(replies[0], []byte("PONG")) {
			return errDatabaseReadiness
		}
		return nil
	}
	if len(replies) != 2 || !bytes.Equal(replies[0], []byte("OK")) || !bytes.Equal(replies[1], []byte(marker)) {
		return errDatabaseReadiness
	}
	return nil
}

func databaseReady(ctx context.Context, address string, plan domain.HostingApplyPlan, values []DatabaseCredentialValue, readOnly bool, initialize bool, before func(context.Context) error) error {
	if plan.Database.Reference.Engine == "redis" {
		return databaseRedisReady(ctx, address, values, readOnly, before)
	}
	if initialize && !readOnly {
		return databasePostgresInitialize(ctx, address, values, before)
	}
	return databasePostgresApp(ctx, address, values)
}
