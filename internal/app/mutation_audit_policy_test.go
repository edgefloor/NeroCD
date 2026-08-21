package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// Successful state changes use a repository request method that includes its
// audit record.  Keep the only direct writeAudit call in the denial helper:
// denial evidence has no state mutation to join to a transaction.
func TestProductionMutationServicesDoNotWriteAuditAfterTheFact(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]map[string]bool{"service.go": {"writeDeniedAudit": true}}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || filepath.Ext(filepath.Base(name)) != ".go" || name == "mutation_audit_policy_test.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		var current string
		ast.Inspect(file, func(node ast.Node) bool {
			if function, ok := node.(*ast.FuncDecl); ok {
				current = function.Name.Name
				return true
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "writeAudit" {
				return true
			}
			receiver, ok := selector.X.(*ast.Ident)
			if ok && receiver.Name == "s" && !allowed[name][current] {
				t.Errorf("%s:%s calls writeAudit outside the denial-only allowlist", name, current)
			}
			return true
		})
	}
}
