// The acceptance client supports the legacy stdin probe and an explicit HTTP app.
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
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
)

type input struct {
	Engine   string `json:"engine"`
	Host     string `json:"host"`
	Address  string `json:"address,omitempty"`
	Username string `json:"username"`
	Password []byte `json:"password"`
	Marker   string `json:"marker"`
	Write    bool   `json:"write"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		if serve() != nil {
			_, _ = io.WriteString(os.Stderr, "database HTTP app failed\n")
			os.Exit(1)
		}
		return
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	address, err := privateEndpoint(ctx, in.Host, in.Address, net.DefaultResolver.LookupHost)
	if err != nil {
		return err
	}
	// Authenticate only against the verified IP. A second DNS lookup could return
	// another endpoint after the alias qualification and expose the credential.
	in.Host = address
	switch in.Engine {
	case "postgresql":
		return postgres(ctx, in)
	case "redis":
		return redis(ctx, in)
	default:
		return errors.New("unsupported client engine")
	}
}

func privateEndpoint(ctx context.Context, host, address string, lookup func(context.Context, string) ([]string, error)) (string, error) {
	if address == "" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsPrivate() || ip.To4() == nil {
			return "", errors.New("invalid private endpoint")
		}
		return host, nil
	}
	if len(host) != 37 || !strings.HasPrefix(host, "ncdb-") || strings.Trim(host[5:], "0123456789abcdef") != "" {
		return "", errors.New("invalid private alias")
	}
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || !ip.IsPrivate() || ip.String() != address {
		return "", errors.New("invalid private endpoint")
	}
	addresses, err := lookup(ctx, host)
	if err != nil || len(addresses) != 1 || addresses[0] != address {
		return "", errors.New("private alias qualification failed")
	}
	return address, nil
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

type httpDatabase struct{ engine, host, username, password, namespace string }

func fixtureWord(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.Trim(value, "abcdefghijklmnopqrstuvwxyz0123456789_") == ""
}

func httpDatabaseEnvironment(env func(string) string) (httpDatabase, error) {
	c := httpDatabase{engine: env("FIXTURE_DATABASE_ENGINE"), namespace: env("FIXTURE_DATABASE_NAMESPACE")}
	invalid := errors.New("HTTP database configuration invalid")
	if !fixtureWord(c.namespace, 64) {
		return c, invalid
	}
	switch c.engine {
	case "postgresql":
		c.host, c.username, c.password = env("PGHOST"), env("PGUSER"), env("PGPASSWORD")
		if env("PGPORT") != "5432" || env("PGDATABASE") != "app" {
			return c, invalid
		}
	case "redis":
		c.host, c.username, c.password = env("REDIS_HOST"), env("REDIS_USERNAME"), env("REDIS_PASSWORD")
		if env("REDIS_PORT") != "6379" {
			return c, invalid
		}
	default:
		return c, invalid
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(c.password)
	defer clear(decoded)
	if c.username != "ncd_app" || len(c.password) != 43 || err != nil || len(decoded) != 32 || len(c.host) != 37 || !strings.HasPrefix(c.host, "ncdb-") || strings.Trim(c.host[5:], "0123456789abcdef") != "" {
		return c, invalid
	}
	return c, nil
}

// A request pins the private alias before opening its own bounded connection.
func (c httpDatabase) record(ctx context.Context, action, key, value string) (string, error) {
	addresses, err := net.DefaultResolver.LookupHost(ctx, c.host)
	if err != nil || len(addresses) != 1 {
		return "", errors.New("HTTP database unavailable")
	}
	host, err := privateEndpoint(ctx, c.host, addresses[0], func(context.Context, string) ([]string, error) { return addresses, nil })
	if err != nil {
		return "", errors.New("HTTP database unavailable")
	}
	if c.engine == "postgresql" {
		return c.postgresRecord(ctx, host, action, key, value)
	}
	return c.redisRecord(ctx, host, action, key, value)
}

func (c httpDatabase) postgresRecord(ctx context.Context, host, action, key, value string) (string, error) {
	invalid := errors.New("HTTP database unavailable")
	cfg, err := pgx.ParseConfig("host=127.0.0.1 port=5432 dbname=app user=ncd_app sslmode=disable")
	if err != nil {
		return "", invalid
	}
	cfg.Host, cfg.Password, cfg.ConnectTimeout = host, c.password, 2*time.Second
	cfg.RuntimeParams = map[string]string{"application_name": "nerocd-http-fixture", "statement_timeout": "2000", "lock_timeout": "1000"}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	cfg.Password = ""
	if err != nil {
		return "", invalid
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	if action == "ready" {
		if conn.Ping(ctx) != nil {
			return "", invalid
		}
		return "ready", nil
	}
	if action == "put" {
		if _, err = conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS public.nerocd_fixture_http (namespace text NOT NULL, key text NOT NULL, value text NOT NULL, PRIMARY KEY(namespace,key))"); err != nil {
			return "", invalid
		}
		if _, err = conn.Exec(ctx, "INSERT INTO public.nerocd_fixture_http(namespace,key,value) VALUES($1,$2,$3) ON CONFLICT(namespace,key) DO UPDATE SET value=excluded.value", c.namespace, key, value); err != nil {
			return "", invalid
		}
	}
	var result string
	if err = conn.QueryRow(ctx, "SELECT value FROM public.nerocd_fixture_http WHERE namespace=$1 AND key=$2", c.namespace, key).Scan(&result); err != nil || len(result) > 4096 {
		return "", invalid
	}
	return result, nil
}

func (c httpDatabase) redisRecord(ctx context.Context, host, action, key, value string) (result string, err error) {
	invalid := errors.New("HTTP database unavailable")
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, "6379"))
	if err != nil {
		return "", invalid
	}
	defer func() {
		if conn.Close() != nil {
			result, err = "", invalid
		}
	}()
	deadline, ok := ctx.Deadline()
	if !ok || conn.SetDeadline(deadline) != nil {
		return "", invalid
	}
	reader := bufio.NewReaderSize(conn, 4096)
	command := func(args ...string) (string, error) {
		var b bytes.Buffer
		fmt.Fprintf(&b, "*%d\r\n", len(args))
		for _, arg := range args {
			fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(arg), arg)
		}
		payload := b.Bytes()
		defer clear(payload)
		if n, e := conn.Write(payload); e != nil || n != len(payload) {
			return "", invalid
		}
		line, e := reader.ReadSlice('\n')
		if e != nil || len(line) < 3 || line[len(line)-2] != '\r' {
			return "", invalid
		}
		if line[0] == '+' {
			return string(line[1 : len(line)-2]), nil
		}
		if line[0] != '$' {
			return "", invalid
		}
		size, e := strconv.Atoi(string(line[1 : len(line)-2]))
		if e != nil || size < 0 || size > 4096 {
			return "", invalid
		}
		raw := make([]byte, size+2)
		defer clear(raw)
		if _, e = io.ReadFull(reader, raw); e != nil || string(raw[size:]) != "\r\n" {
			return "", invalid
		}
		return string(raw[:size]), nil
	}
	if out, e := command("AUTH", c.username, c.password); e != nil || out != "OK" {
		return "", invalid
	}
	if action == "ready" {
		out, e := command("PING")
		if e != nil || out != "PONG" {
			return "", invalid
		}
		return "ready", nil
	}
	name := "nerocd_http:" + c.namespace + ":" + key
	if action == "put" {
		if out, e := command("SET", name, value); e != nil || out != "OK" {
			return "", invalid
		}
	}
	return command("GET", name)
}

func httpDatabaseHandler(record func(context.Context, string, string, string) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		action, key, value := "", "", ""
		switch {
		case r.URL.Path == "/ready" && r.Method == http.MethodGet && r.URL.RawQuery == "":
			action = "ready"
		case strings.HasPrefix(r.URL.Path, "/records/") && (r.Method == http.MethodPut || r.Method == http.MethodGet):
			key = strings.TrimPrefix(r.URL.Path, "/records/")
			if !fixtureWord(key, 64) || r.URL.RawQuery != "" {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			action = "get"
			if r.Method == http.MethodPut {
				raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
				defer clear(raw)
				if err != nil || len(raw) == 0 {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				action, value = "put", string(raw)
			}
		default:
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		out, err := record(ctx, action, key, value)
		if err != nil || len(out) > 4096 {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, out)
	})
}

func httpDatabaseInstanceHandler(instance string, application http.Handler) (http.Handler, error) {
	if len(instance) != 12 || strings.Trim(instance, "0123456789abcdef") != "" || application == nil {
		return nil, errors.New("fixture container identity unavailable")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-NeroCD-Fixture-Instance", instance)
		application.ServeHTTP(w, r)
	}), nil
}

func httpFixtureRecord(getenv func(string) string) (func(context.Context, string, string, string) (string, error), error) {
	if getenv("FIXTURE_DATABASE_ENGINE") == "unrelated" {
		if getenv("FIXTURE_DATABASE_NAMESPACE") != "route_isolation" {
			return nil, errors.New("unrelated fixture namespace invalid")
		}
		return func(_ context.Context, action, key, _ string) (string, error) {
			if action == "ready" {
				return "ready", nil
			}
			if action == "get" && key == "unrelated" {
				return "unrelated_route", nil
			}
			return "", errors.New("unrelated fixture is read only")
		}, nil
	}
	cfg, err := httpDatabaseEnvironment(getenv)
	if err != nil {
		return nil, err
	}
	return cfg.record, nil
}

func serve() error {
	record, err := httpFixtureRecord(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	instance, err := os.Hostname()
	if err != nil {
		return errors.New("fixture container identity unavailable")
	}
	application, err := httpDatabaseInstanceHandler(instance, httpDatabaseHandler(record))
	if err != nil {
		return err
	}
	server := &http.Server{Addr: ":8080", Handler: application, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 6 * time.Second, WriteTimeout: 6 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		err = server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
		joined := <-done
		if !errors.Is(joined, http.ErrServerClosed) {
			return errors.New("HTTP app shutdown failed")
		}
		return err
	}
}
