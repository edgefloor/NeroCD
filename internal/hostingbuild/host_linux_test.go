package hostingbuild

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHostLockAndCleanupBlock(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acquireLock(dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("second engine admitted: %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := acquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = next.Close(); err != nil {
		t.Fatal(err)
	}
	if err = durableJSON(filepath.Join(dir, "cleanup_unconfirmed.json"), ownedResources{Token: "owned"}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{config: Config{StateDir: dir}}
	_, err = e.Build(t.Context(), Request{BuildID: "build", Checkout: "/source", SourceID: "commit", InputID: "input", DaemonID: "daemon", Strategy: Dockerfile, Platform: "linux/arm64", Limits: testLimits()})
	if !errors.Is(err, ErrCleanupUnconfirmed) {
		t.Fatalf("uncertain cleanup admitted: %v", err)
	}
}
func TestCgroupEnforcement(t *testing.T) {
	dir := t.TempDir()
	l := testLimits()
	for name, value := range map[string]string{"memory.max": "1073741824", "memory.swap.max": "0", "cpu.max": "100000 100000"} {
		writeTestFile(t, filepath.Join(dir, name), []byte(value))
	}
	if err := checkGroup(dir, l); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"memory.max", "memory.swap.max", "cpu.max"} {
		t.Run(file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(dir, file), []byte("max"))
			if err = checkGroup(dir, l); err == nil {
				t.Fatal("unlimited cgroup accepted")
			}
			writeTestFile(t, filepath.Join(dir, file), b)
		})
	}
}

func TestDelegationAndCleanupBoundaries(t *testing.T) {
	dir := t.TempDir()
	if err := validateDelegation(dir); err == nil {
		t.Fatal("ordinary filesystem accepted as delegated cgroup")
	}
	e := &Engine{config: Config{CgroupRoot: dir}}
	if err := e.cleanupGroup(ownedResources{Token: "0123456789abcdef0123456789abcdef", Group: filepath.Join(dir, "foreign")}); err == nil {
		t.Fatal("foreign cgroup cleanup accepted")
	}
}
