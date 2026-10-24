//go:build unix

package runnerruntime

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDatabaseRESPBoundedParsingAndRedaction(t *testing.T) {
	for _, raw := range []string{"-ERR secret credential payload\r\n", "$9000000\r\n", "$4\r\nabc", "+" + strings.Repeat("a", 300) + "\r\n", "*1\r\n+OK\r\n", "$-1\r\n"} {
		if _, err := databaseRESPValue(bufio.NewReader(strings.NewReader(raw))); err == nil || strings.Contains(err.Error(), "credential payload") {
			t.Fatal("unbounded/raw RESP evidence escaped")
		}
	}
	for _, raw := range []string{"+PONG\r\n", "$4\r\nPONG\r\n"} {
		got, err := databaseRESPValue(bufio.NewReader(strings.NewReader(raw)))
		if err != nil || string(got) != "PONG" {
			t.Fatal("valid bounded RESP rejected")
		}
	}
}

func TestDatabaseRESPAuthenticatesOwnedBytesAndJoinsCancellation(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		reader := bufio.NewReader(server)
		for _, want := range []string{"*3\r\n", "$4\r\n", "AUTH\r\n", "$7\r\n", "ncd_app\r\n", "$32\r\n", strings.Repeat("p", 32) + "\r\n"} {
			line, err := reader.ReadString('\n')
			if err != nil || line != want {
				return
			}
		}
		if _, err := io.WriteString(server, "+OK\r\n"); err != nil {
			return
		}
		for range 3 {
			if _, err := reader.ReadString('\n'); err != nil {
				return
			}
		}
		cancel()
	}()
	t.Cleanup(func() {
		cancel()
		_ = server.Close()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("RESP peer did not join")
		}
	})
	start := time.Now()
	_, err := databaseRESPExchange(ctx, client, "ncd_app", []byte(strings.Repeat("p", 32)), [][]string{{"PING"}})
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("active RESP socket ignored cancellation")
	}
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("RESP peer leaked")
	}
}

func TestDatabaseRedisProbeUsesNXExpiryAndObserveIsReadOnly(t *testing.T) {
	f, credentials := databaseFixture(t, "redis")
	err := credentials.use(context.Background(), f.plan, func(ctx context.Context, values []DatabaseCredentialValue) error {
		for _, readOnly := range []bool{false, true} {
			calls := 0
			mutations := 0
			exchange := func(_ context.Context, _ string, user string, password []byte, commands [][]string) ([][]byte, error) {
				calls++
				if len(password) < 32 {
					t.Fatal("missing protected protocol credential")
				}
				if calls == 1 {
					if user != "ncd_probe" || !slices.Equal(commands[0], []string{"PING"}) {
						t.Fatal("wrong probe role")
					}
					return [][]byte{[]byte("PONG")}, nil
				}
				if user != "ncd_app" {
					t.Fatal("readiness omitted application identity")
				}
				if readOnly {
					if len(commands) != 1 || !slices.Equal(commands[0], []string{"PING"}) {
						t.Fatal("read-only observation issued Redis mutation")
					}
					return [][]byte{[]byte("PONG")}, nil
				}
				if mutations != 1 || len(commands) != 2 || len(commands[0]) != 6 || commands[0][0] != "SET" || !strings.HasPrefix(commands[0][1], "__nerocd_probe:") || commands[0][3] != "EX" || commands[0][4] != "10" || commands[0][5] != "NX" || !slices.Equal(commands[1], []string{"GET", commands[0][1]}) {
					t.Fatal("Redis readiness could overwrite user key or lacks authority/TTL")
				}
				return [][]byte{[]byte("OK"), []byte(commands[0][2])}, nil
			}
			before := func(context.Context) error { mutations++; return nil }
			if err := databaseRedisProbe(ctx, "172.28.0.2", values, readOnly, before, exchange); err != nil {
				t.Fatal(err)
			}
			if readOnly && mutations != 0 {
				t.Fatal("observation requested mutation authority")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type databasePGTxFake struct {
	pgx.Tx
	t                                      *testing.T
	password                               string
	execs, commits, rollbacks, authorities int
	failAt                                 int
}

func (f *databasePGTxFake) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execs++
	if strings.Contains(sql, f.password) {
		f.t.Fatal("password interpolated into SQL")
	}
	if f.execs == 1 {
		if sql != "SELECT pg_catalog.set_config('nerocd.init_password', $1, true)" || len(args) != 1 || args[0] != f.password {
			f.t.Fatal("PostgreSQL init did not bind protected parameter")
		}
	} else if f.authorities < f.execs-1 {
		f.t.Fatal("role/grant SQL lacks authority callback")
	}
	if f.execs == f.failAt {
		return pgconn.CommandTag{}, errors.New("raw database password payload")
	}
	return pgconn.CommandTag{}, nil
}
func (f *databasePGTxFake) Commit(context.Context) error {
	f.commits++
	if f.authorities < 4 {
		f.t.Fatal("commit lacked current authority")
	}
	return nil
}
func (f *databasePGTxFake) Rollback(ctx context.Context) error {
	f.rollbacks++
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
		f.t.Fatal("unbounded SQL rollback")
	}
	return pgx.ErrTxClosed
}

func TestDatabasePostgresInitializationBoundTransactionAndStaticErrors(t *testing.T) {
	for _, failAt := range []int{0, 1, 2, 3, 4} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			f, credentials := databaseFixture(t, "postgresql")
			err := credentials.use(context.Background(), f.plan, func(ctx context.Context, values []DatabaseCredentialValue) error {
				tx := &databasePGTxFake{t: t, password: string(databasePassword(values, "app")), failAt: failAt}
				err := databasePostgresRoleTransaction(ctx, tx, values, func(context.Context) error { tx.authorities++; return nil })
				if tx.rollbacks != 1 || failAt == 0 && (err != nil || tx.commits != 1) || failAt != 0 && (err == nil || tx.commits != 0 || strings.Contains(err.Error(), "password payload")) {
					t.Fatal("initialization transaction/cleanup/redaction contract failed")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
