package hostingbuild

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode"
)

func relativeSetting(p string) bool {
	if p == "" {
		return true
	}
	if len(p) > 512 || !fs.ValidPath(p) || path.Clean(p) != p || p == "." || strings.ContainsAny(p, "\\\x00\r\n\t") || strings.Contains(p, ":") || len(strings.Split(p, "/")) > 32 {
		return false
	}
	for _, c := range p {
		if unicode.IsControl(c) {
			return false
		}
	}
	for _, part := range strings.Split(p, "/") {
		if excluded(part) {
			return false
		}
	}
	return true
}

// openSettingRoot rejects links component by component and keeps each subsequent
// access anchored to the supplied immutable root, including after path renames.
func openSettingRoot(root *os.Root, p string) (*os.Root, error) {
	if p == "" {
		return root.OpenRoot(".")
	}
	current := ""
	for _, part := range strings.Split(p, "/") {
		current = path.Join(current, part)
		st, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("settings directory links/special files denied")
		}
	}
	return root.OpenRoot(p)
}
func regularSetting(root *os.Root, p string) error {
	parent := path.Dir(p)
	if parent == "." {
		parent = ""
	}
	dir, err := openSettingRoot(root, parent)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	st, err := dir.Lstat(path.Base(p))
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("settings file links/special files denied")
	}
	return nil
}
