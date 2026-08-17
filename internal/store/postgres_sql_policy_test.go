package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPostgresSQLLivesInSQLCQuerySources(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	directExecution := regexp.MustCompile(`\.(Exec|Query|QueryRow)\s*\(`)
	inlineSQL := regexp.MustCompile("`\\s*(SELECT|INSERT|UPDATE|DELETE|WITH)\\b")
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "postgres") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		content, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		if directExecution.Match(content) || inlineSQL.Match(content) {
			t.Errorf("%s contains repository SQL outside internal/store/sql and sqlcgen", name)
		}
	}
}
