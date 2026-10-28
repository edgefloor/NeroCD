//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMySQLRecipeFilesAndWriterLifecycle(t *testing.T) {
	f, credentials := databaseFixture(t, "mysql")
	defer credentials.Close()
	result := f.apply(t.Context(), credentials)
	if result.Outcome != "succeeded" {
		t.Fatal("MySQL apply", result)
	}
	env, args := databaseRecipeArgs(f.plan)
	for _, value := range env {
		if strings.Contains(value, "PASSWORD=") {
			t.Fatal("plaintext password environment")
		}
	}
	if !slices.Contains(env, "MYSQL_DATABASE=app") || !slices.Contains(args, "--local-infile=OFF") {
		t.Fatal("MySQL server configuration missing")
	}
	values := credentials.values
	files := databaseSecretFiles("operation", f.plan, values)
	defer func() {
		for _, value := range files {
			clear(value)
		}
	}()
	if !bytes.Equal(files["admin-password"], databasePassword(values, "admin")) || !bytes.Equal(files["app-password"], databasePassword(values, "app")) || bytes.Equal(files["app-password"], files["admin-password"]) {
		t.Fatal("MySQL credential roles changed")
	}
	if !bytes.Contains(files["admin.cnf"], []byte("user=root\n")) || !bytes.Contains(files["app.cnf"], []byte("user=ncd_app\n")) {
		t.Fatal("protected client profiles changed")
	}
	for _, call := range f.calls {
		if strings.Contains(call, string(files["admin-password"])) || strings.Contains(call, string(files["app-password"])) {
			t.Fatal("MySQL credentials entered process arguments")
		}
	}
}
func TestMySQLLogicalDumpFormatAndNoPlaintextArgs(t *testing.T) {
	e, f, p := backupEngineFixture(t, "mysql")
	var commands [][]string
	e.stream = func(_ context.Context, args []string, in io.Reader, out io.Writer) error {
		commands = append(commands, slices.Clone(args))
		if slices.Contains(args, "mysqldump") {
			_, err := io.WriteString(out, "CREATE TABLE fixture(id INT);\n")
			return err
		}
		_, err := io.WriteString(out, "0\n")
		return err
	}
	blob, err := e.run(t.Context(), p, f.before)
	if err != nil || blob.Format != "mysql_sql" {
		t.Fatal("MySQL dump failed", err)
	}
	if len(commands) != 2 || !slices.Contains(commands[1], "--single-transaction") || !slices.Contains(commands[1], "--defaults-extra-file="+databaseSecretMount+"/admin.cnf") {
		t.Fatal("MySQL dump command changed", commands)
	}
	p.Operation.Blob = &blob
	r, err := e.archive(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || !bytes.HasPrefix(raw, []byte(databaseMySQLDumpHeader)) {
		t.Fatal("MySQL archive lost explicit format")
	}
	p.Operation.Blob.Format = "postgresql_custom"
	if _, err = e.archive(t.Context(), p); err == nil {
		t.Fatal("MySQL archive accepted PostgreSQL format")
	}
}
func TestMySQLRestoreFailureAndCancellationRetainEvidence(t *testing.T) {
	for _, failure := range []string{"none", "import", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			e, f, p := backupRestoreEngineFixture(t, "mysql")
			imports := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			e.stream = func(c context.Context, args []string, in io.Reader, out io.Writer) error {
				if in != nil {
					imports++
					if !slices.Contains(args, "--binary-mode=1") || !slices.Contains(args, "--local-infile=0") {
						t.Fatal("unsafe MySQL import client flags")
					}
					if failure == "cancel" {
						cancel()
						return c.Err()
					}
					if failure == "import" {
						return errors.New("private diagnostic")
					}
					_, err := io.Copy(io.Discard, in)
					return err
				}
				return nil
			}
			_, err := e.run(ctx, p, f.before)
			if (err == nil) != (failure == "none") || imports != 1 {
				t.Fatal("MySQL import outcome", failure, err, imports)
			}
			if failure != "none" && strings.Contains(err.Error(), "private diagnostic") {
				t.Fatal("import leaked diagnostics")
			}
			if archive, err := e.archive(t.Context(), p); err != nil {
				t.Fatal("failed import lost original dump", err)
			} else {
				_ = archive.Close()
			}
		})
	}
}

func TestMySQLReadinessConnectorBoundsAndFixedDiagnostics(t *testing.T) {
	_, credentials := databaseFixture(t, "mysql")
	defer credentials.Close()
	config, err := databaseMySQLConfig("172.28.0.2", credentials.values)
	if err != nil || config.User != "ncd_app" || config.DBName != "app" || config.Addr != "172.28.0.2:3306" || config.MultiStatements || config.AllowAllFiles || config.AllowCleartextPasswords || config.Timeout != 2*time.Second || config.ReadTimeout != 2*time.Second || config.WriteTimeout != 2*time.Second {
		t.Fatal("MySQL readiness connector policy changed", err)
	}
	if _, ok := config.Logger.(databaseMySQLLogger); !ok {
		t.Fatal("readiness uses raw global diagnostics")
	}
	for _, address := range []string{"127.0.0.1", "8.8.8.8", "::1", "host.example", "172.28.0.2:3306"} {
		if _, err := databaseMySQLConfig(address, credentials.values); err == nil {
			t.Fatal("unsafe readiness address accepted", address)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = databaseMySQLReady(ctx, "172.28.0.2", credentials.values)
	if !errors.Is(err, errDatabaseReadiness) || strings.Contains(err.Error(), string(databasePassword(credentials.values, "app"))) {
		t.Fatal("readiness diagnostics escaped", err)
	}
}
