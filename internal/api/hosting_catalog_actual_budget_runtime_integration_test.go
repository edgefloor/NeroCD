//go:build linux && nerocd_proxy_fixture

package api

import (
	"os"
	"testing"

	"nerocd/internal/domain"
)

// TestHostingCatalogActualBudgetCompiledRunnerHTTPS requires the same owned
// catalog gate as Kuma. Fresh contexts download server data rather than reuse
// IndexedDB, and the shared lifecycle pins this application's exact literals.
func TestHostingCatalogActualBudgetCompiledRunnerHTTPS(t *testing.T) {
	if testing.Short() || os.Getenv("NEROCD_CATALOG_RUNTIME") != "disposable-dind" || os.Getenv("NEROCD_CATALOG_APPLICATION") != "actualbudget" {
		t.Skip("requires approved isolated Actual Budget catalog runtime gate")
	}
	password := "password"
	catalogRuntimePersistentApplication(t, catalogRuntimeApplicationProfile{ID: "actualbudget", Version: domain.HostingCatalogActualBudgetVersion, RequestPrefix: "actualbudget", Port: 5006, ReadinessPath: "/health", FailurePath: "/sync/nerocd-required-missing", HealthContent: "UP", Entries: []domain.HostingRuntimeEntry{{Name: "ACTUAL_LOGIN_METHOD", Kind: "literal", Value: &password}}})
}
