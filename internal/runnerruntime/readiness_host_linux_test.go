//go:build linux

package runnerruntime

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadinessHostMetadataAndAvailableBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "docker")
	canary := filepath.Join(root, "executed")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntouch "+canary+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	if state := readinessCLI(); state != "available" {
		t.Fatal(state)
	}
	if _, err := os.Stat(canary); !os.IsNotExist(err) {
		t.Fatal("CLI was executed")
	}
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if state, free := readinessRoot(root, true); state != "accessible" || free < 0 {
		t.Fatal(state, free)
	}
	after, err := os.ReadDir(root)
	if err != nil || len(after) != len(before) {
		t.Fatal("metadata created state")
	}
	if err = os.Chmod(binary, 0777); err != nil {
		t.Fatal(err)
	}
	if state := readinessCLI(); state != "unsafe" {
		t.Fatal(state)
	}
	if err = os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if state := readinessCLI(); state != "missing" {
		t.Fatal(state)
	}
	if err = os.Symlink("/bin/sh", binary); err != nil {
		t.Fatal(err)
	}
	if state := readinessCLI(); state != "unsafe" {
		t.Fatal(state)
	}
	if err = os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if err = unix.Mkfifo(binary, 0755); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if state := readinessCLI(); state != "unsafe" || time.Since(started) > time.Second {
		t.Fatal("special CLI path blocked or qualified", state)
	}
	if err = os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if state, _ := readinessRoot(root, true); state != "unsafe" {
		t.Fatal(state)
	}
	link := filepath.Join(t.TempDir(), "root")
	if err = os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if state, _ := readinessRoot(link, false); state != "unsafe" {
		t.Fatal(state)
	}
	if state, _ := readinessRoot(filepath.Join(root, "missing"), false); state != "missing" {
		t.Fatal(state)
	}
	for _, tc := range []struct {
		blocks uint64
		size   int64
		valid  bool
	}{{1, 4096, true}, {0, 4096, true}, {math.MaxUint64, 4096, false}, {1, -1, false}, {1, 0, false}} {
		if _, ok := readinessAvailableBytes(tc.blocks, tc.size); ok != tc.valid {
			t.Fatal(tc)
		}
	}
}
