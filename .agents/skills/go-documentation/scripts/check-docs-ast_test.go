package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type checkerResult struct {
	Missing []struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"missing"`
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
}

func TestCheckDocsWrapperAcceptsDocumentedExportsAndIgnoresUnexported(t *testing.T) {
	fixture := writeFixture(t, `// Package fixture provides test declarations.
package fixture

// DocumentedType is an exported type with documentation.
type DocumentedType struct{}

// DocumentedFunc is an exported function with documentation.
func DocumentedFunc() {}

func undocumentedPrivate() {}
`)

	output, err := runChecker(t, fixture)
	if err != nil {
		t.Fatalf("check-docs.sh returned an error: %v\noutput: %s", err, output)
	}
	if output != "All exported symbols are documented.\n" {
		t.Fatalf("check-docs.sh output = %q, want clean result", output)
	}
}

func TestCheckDocsWrapperJSONReportsUndocumentedExportAndExitStatus(t *testing.T) {
	fixture := writeFixture(t, `// Package fixture provides test declarations.
package fixture

func MissingDoc() {}
`)

	output, err := runChecker(t, "--json", fixture)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("check-docs.sh error = %v, want exit status 1\noutput: %s", err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("check-docs.sh exit status = %d, want 1\noutput: %s", exitErr.ExitCode(), output)
	}

	var result checkerResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("check-docs.sh JSON output is invalid: %v\noutput: %s", err, output)
	}
	if result.Total != 1 || result.Truncated || len(result.Missing) != 1 {
		t.Fatalf("check-docs.sh JSON result = %+v, want one untruncated finding", result)
	}
	if result.Missing[0].Kind != "function" || result.Missing[0].Name != "MissingDoc" {
		t.Fatalf("check-docs.sh finding = %+v, want function MissingDoc", result.Missing[0])
	}
}

func writeFixture(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

func runChecker(t *testing.T, args ...string) (string, error) {
	t.Helper()
	wrapper, err := filepath.Abs("check-docs.sh")
	if err != nil {
		t.Fatalf("resolve check-docs.sh: %v", err)
	}
	command := exec.Command("bash", append([]string{wrapper}, args...)...)
	command.Env = append(os.Environ(), "XDG_CACHE_HOME="+t.TempDir(), "GOCACHE="+t.TempDir())
	output, err := command.CombinedOutput()
	return string(output), err
}
