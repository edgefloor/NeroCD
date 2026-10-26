package api

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// Direct fixture starts must use a prerequisite image already verified by the
// gate. This covers Pebble and Silo without changing production pull behavior.
func storageRuntimeDockerArguments(args []string) []string {
	if len(args) > 0 && args[0] == "run" {
		return append([]string{"run", "--pull=never"}, args[1:]...)
	}
	return args
}

// Docker progress and warnings use stderr. They must never become a container
// ID, JSON document, or other stdout value consumed by the fixture.
func storageRuntimeDockerOutput(command *exec.Cmd) ([]byte, error) {
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, diagnostics.Bytes())
	}
	return output, nil
}

func TestStorageRuntimeDockerOutputSeparatesDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		failure    bool
	}{
		{name: "successful ID with pull-like diagnostics", mode: "success"},
		{name: "failed command keeps stderr", mode: "failure", failure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStorageRuntimeDockerOutputProcess$")
			command.Env = append(os.Environ(), "NEROCD_STORAGE_DOCKER_OUTPUT_TEST="+test.mode)
			output, err := storageRuntimeDockerOutput(command)
			if test.failure {
				if err == nil || !strings.Contains(err.Error(), "fixture Docker diagnostic") || len(output) != 0 {
					t.Fatalf("failure output=%q error=%v", output, err)
				}
				return
			}
			if err != nil || string(output) != strings.Repeat("a", 64)+"\n" {
				t.Fatalf("container ID contaminated: %q error=%v", output, err)
			}
		})
	}
}

func TestStorageRuntimeDockerOutputProcess(t *testing.T) {
	mode := os.Getenv("NEROCD_STORAGE_DOCKER_OUTPUT_TEST")
	if mode == "" {
		return
	}
	fmt.Fprintln(os.Stderr, "fixture Docker diagnostic")
	fmt.Fprintln(os.Stdout, strings.Repeat("a", 64))
	if mode == "failure" {
		os.Exit(17)
	}
	os.Exit(0)
}

func TestStorageRuntimeDockerDirectStartsNeverPull(t *testing.T) {
	original := []string{"run", "--detach", "pinned@sha256:" + strings.Repeat("a", 64)}
	before := append([]string(nil), original...)
	want := append([]string{"run", "--pull=never"}, original[1:]...)
	if actual := storageRuntimeDockerArguments(original); !reflect.DeepEqual(actual, want) {
		t.Fatalf("run arguments=%q want=%q", actual, want)
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("input arguments modified")
	}
	inspect := []string{"image", "inspect", "pinned"}
	if actual := storageRuntimeDockerArguments(inspect); !reflect.DeepEqual(actual, inspect) {
		t.Fatal("non-start command changed")
	}
}
