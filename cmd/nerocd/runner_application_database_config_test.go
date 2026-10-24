package main

import (
	"slices"
	"strings"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/runnerruntime"
)

func TestRunnerApplicationDatabaseFlagsFailBeforeTransportWithoutPrerequisites(t *testing.T) {
	for _, flag := range []string{"--hosting-application-database", "--hosting-database-membership-maintenance"} {
		if err := runRunner([]string{flag, "--once", "--server", "https://controller.invalid"}); err == nil || !strings.Contains(err.Error(), "connected database execution requires") {
			t.Fatal("explicit flag bypassed configuration gate", flag, err)
		}
	}
}

func TestRunnerApplicationDatabaseExplicitPrerequisites(t *testing.T) {
	for _, enabled := range []struct{ application, maintenance bool }{{true, false}, {false, true}, {true, true}} {
		for _, missing := range []string{"database", "capture", "upgrade", "runtime", ""} {
			t.Run(missing, func(t *testing.T) {
				c := &runnerruntime.DatabaseExecutionConfig{NetworkCapture: missing != "capture", NetworkUpgrade: missing != "upgrade"}
				if missing == "database" {
					c = nil
				}
				err := runnerApplicationDatabaseConfig(enabled.application, enabled.maintenance, missing != "runtime", c)
				if (err == nil) != (missing == "") {
					t.Fatal("incorrect prerequisite admission", err)
				}
				if err == nil && (c.ApplicationDatabase != enabled.application || c.MembershipMaintenance != enabled.maintenance) {
					t.Fatal("configuration lost explicit opt-in")
				}
			})
		}
	}
	if err := runnerApplicationDatabaseConfig(false, false, false, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerApplicationDatabaseCapabilitiesDefaultOff(t *testing.T) {
	caps := []string{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseNetworkCaptureCapability, domain.HostingDatabaseUpgradeCapability}
	got, err := runnerApplicationDatabaseCapabilities(false, false, slices.Clone(caps))
	if err != nil || slices.Contains(got, domain.HostingApplicationDatabaseCapability) || slices.Contains(got, domain.HostingDatabaseMaintenanceCapability) {
		t.Fatal("old configuration advertised connected permission")
	}
	got, err = runnerApplicationDatabaseCapabilities(true, true, slices.Clone(caps))
	if err != nil || !slices.Contains(got, domain.HostingApplicationDatabaseCapability) || !slices.Contains(got, domain.HostingDatabaseMaintenanceCapability) {
		t.Fatal("explicit capabilities missing", err)
	}
	if _, err = runnerApplicationDatabaseCapabilities(false, false, got); err == nil {
		t.Fatal("persisted capability silently granted execution")
	}
	for i := range caps {
		in := append(slices.Clone(caps[:i]), caps[i+1:]...)
		if _, err = runnerApplicationDatabaseCapabilities(true, true, in); err == nil {
			t.Fatal("missing prerequisite accepted", caps[i])
		}
	}
	if _, err = runnerApplicationDatabaseCapabilities(true, true, append(caps, "shell")); err == nil {
		t.Fatal("shell profile accepted")
	}
}
