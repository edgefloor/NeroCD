// The disposable acceptance client accepts credentials only through bounded stdin.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
)

type input struct {
	Engine   string `json:"engine"`
	Host     string `json:"host"`
	Username string `json:"username"`
	Password []byte `json:"password"`
	Marker   string `json:"marker"`
	Write    bool   `json:"write"`
}

func main() {
	if run() != nil {
		_, _ = io.WriteString(os.Stderr, "database marker client failed\n")
		os.Exit(1)
	}
	_, _ = io.WriteString(os.Stdout, "marker verified\n")
}

func run() error {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	defer clear(raw)
	if err != nil || len(raw) > 4096 {
		return errors.New("invalid client input")
	}
	var in input
	defer func() { clear(in.Password) }()
	if domain.DecodeHostingStorageProtocolJSON(raw, &in) != nil || len(in.Password) != 43 || in.Username != "ncd_app" || len(in.Marker) < 16 || len(in.Marker) > 128 || strings.Trim(in.Marker, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return errors.New("invalid client input")
	}
	var entropy [32]byte
	n, e := base64.RawURLEncoding.Strict().Decode(entropy[:], in.Password)
	clear(entropy[:])
	if e != nil || n != 32 {
		return errors.New("invalid client credential")
	}
	ip := net.ParseIP(in.Host)
	if ip == nil || !ip.IsPrivate() || ip.To4() == nil {
		return errors.New("invalid private endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	switch in.Engine {
	case "postgresql":
		return postgres(ctx, in)
	case "redis":
		return redis(ctx, in)
	default:
		return errors.New("unsupported client engine")
	}
}

func postgres(ctx context.Context, in input) (err error) {
	cfg, err := pgx.ParseConfig("host=127.0.0.1 port=5432 dbname=app user=ncd_app sslmode=disable")
	if err != nil {
		return errors.New("client configuration failed")
	}
	cfg.Host, cfg.Password = in.Host, string(in.Password)
	cfg.TLSConfig = nil
	cfg.Fallbacks = nil
	cfg.ConnectTimeout = 3 * time.Second
	cfg.RuntimeParams = map[string]string{"application_name": "nerocd-disposable-marker", "statement_timeout": "3000", "lock_timeout": "1000"}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	cfg.Password = ""
	if err != nil {
		return errors.New("client authentication failed")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if conn.Close(cleanup) != nil {
			err = errors.Join(err, errors.New("client connection cleanup failed"))
		}
	}()
	if in.Write {
		if _, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.nerocd_fixture_marker(id integer PRIMARY KEY,value text NOT NULL)`); err != nil {
			return errors.New("marker table failed")
		}
		if _, err = conn.Exec(ctx, `INSERT INTO public.nerocd_fixture_marker(id,value) VALUES(1,$1) ON CONFLICT(id) DO UPDATE SET value=EXCLUDED.value`, in.Marker); err != nil {
			return errors.New("marker write failed")
		}
	}
	var got string
	if conn.QueryRow(ctx, `SELECT value FROM public.nerocd_fixture_marker WHERE id=1`).Scan(&got) != nil || got != in.Marker {
		return errors.New("marker read failed")
	}
	return nil
}

func redis(ctx context.Context, in input) (err error) {
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(in.Host, "6379"))
	if err != nil {
		return errors.New("client connect failed")
	}
	defer func() {
		if conn.Close() != nil {
			err = errors.Join(err, errors.New("client connection cleanup failed"))
		}
	}()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return errors.New("client deadline failed")
	}
	r := bufio.NewReaderSize(conn, 4096)
	command := func(args ...[]byte) error {
		var payload bytes.Buffer
		defer clear(payload.Bytes())
		_, _ = fmt.Fprintf(&payload, "*%d\r\n", len(args))
		for _, a := range args {
			_, _ = fmt.Fprintf(&payload, "$%d\r\n", len(a))
			_, _ = payload.Write(a)
			_, _ = payload.WriteString("\r\n")
		}
		n, e := conn.Write(payload.Bytes())
		if e != nil || n != payload.Len() {
			return errors.New("client protocol write failed")
		}
		return nil
	}
	ok := func() bool { line, e := r.ReadSlice('\n'); return e == nil && bytes.Equal(line, []byte("+OK\r\n")) }
	if command([]byte("AUTH"), []byte(in.Username), in.Password) != nil || !ok() {
		return errors.New("client authentication failed")
	}
	if in.Write {
		if command([]byte("SET"), []byte("nerocd_fixture_marker"), []byte(in.Marker)) != nil || !ok() {
			return errors.New("marker write failed")
		}
	}
	if command([]byte("GET"), []byte("nerocd_fixture_marker")) != nil {
		return errors.New("marker read failed")
	}
	line, err := r.ReadSlice('\n')
	if err != nil || !bytes.Equal(line, []byte("$"+strconv.Itoa(len(in.Marker))+"\r\n")) {
		return errors.New("marker response failed")
	}
	value := make([]byte, len(in.Marker)+2)
	defer clear(value)
	if _, err = io.ReadFull(r, value); err != nil || !bytes.Equal(value, []byte(in.Marker+"\r\n")) {
		return errors.New("marker mismatch")
	}
	return nil
}
