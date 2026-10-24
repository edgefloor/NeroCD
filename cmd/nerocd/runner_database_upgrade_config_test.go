package main

import (
	"slices"
	"strings"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/runnerruntime"
)

func TestRunnerDatabaseUpgradeRequiresExplicitOptIn(t *testing.T) {
	config := &runnerruntime.DatabaseExecutionConfig{}
	if err := runnerDatabaseUpgradeConfig(true, nil); err == nil {
		t.Fatal("upgrade without database config accepted")
	}
	if err := runnerDatabaseUpgradeConfig(false, config); err != nil || config.NetworkUpgrade {
		t.Fatal("default enabled upgrade")
	}
	caps, err := runnerDatabaseCapabilities(true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	caps, err = runnerDatabaseUpgradeCapabilities(false, true, caps)
	if err != nil || slices.Contains(caps, domain.HostingDatabaseUpgradeCapability) {
		t.Fatal("ordinary database config advertised upgrade")
	}
	if _, err = runnerDatabaseUpgradeCapabilities(false, true, append(caps, domain.HostingDatabaseUpgradeCapability)); err == nil {
		t.Fatal("capability silently enabled upgrade")
	}
	if _, err = runnerDatabaseUpgradeCapabilities(true, false, nil); err == nil {
		t.Fatal("upgrade without database capability accepted")
	}
	if err = runnerDatabaseUpgradeConfig(true, config); err != nil || !config.NetworkUpgrade {
		t.Fatal("explicit upgrade configuration lost")
	}
	caps, err = runnerDatabaseUpgradeCapabilities(true, true, caps)
	if err != nil || !slices.Contains(caps, domain.HostingDatabaseUpgradeCapability) {
		t.Fatal("explicit upgrade capability missing")
	}
}

func TestRunnerDatabaseUpgradeFlagCannotShareExactCapture(t *testing.T) {
	flags := captureExplicitFlags()
	flags["hosting-database-network-upgrade"] = true
	if validateRunnerDatabaseCaptureFlags("hnc_"+strings.Repeat("a", 32), true, "", flags) == nil {
		t.Fatal("capture accepted simultaneous upgrade execution flag")
	}
}
