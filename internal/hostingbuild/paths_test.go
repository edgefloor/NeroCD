package hostingbuild

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootedSettingPaths(t *testing.T) {
	src := t.TempDir()
	if err := os.Mkdir(filepath.Join(src, "real"), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "real", "file"), []byte("source"))
	if err := os.Symlink("real", filepath.Join(src, "linked")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	selected, err := openSettingRoot(root, "real")
	if err != nil {
		t.Fatal(err)
	}
	_ = selected.Close()
	if _, err = openSettingRoot(root, "linked"); err == nil {
		t.Fatal("symlink setting root admitted")
	}
	if err = regularSetting(root, "real/file"); err != nil {
		t.Fatal(err)
	}
	if err = regularSetting(root, "real"); err == nil {
		t.Fatal("directory admitted as settings file")
	}
	for _, p := range []string{".", "../escape", "/absolute", "a//b", "a\\b", "a\x01b", ".git"} {
		if relativeSetting(p) {
			t.Fatalf("noncanonical setting admitted: %q", p)
		}
	}
}
