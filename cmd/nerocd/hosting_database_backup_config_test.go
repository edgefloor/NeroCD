package main

import (
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestDatabaseBackupOperatorLimitsRejectBeforeStartup(t *testing.T) {
	for _, args := range [][]string{{"--database-backup-max-bytes=0"}, {"--database-backup-max-bytes=1073741825"}, {"--database-backup-timeout-seconds=0"}, {"--database-backup-timeout-seconds=3601"}} {
		if err := runServer(args); err == nil || !strings.Contains(err.Error(), "database backup limits") {
			t.Fatal("invalid protected limits reached service startup", args, err)
		}
	}
	if domain.DefaultHostingDatabaseBackupLimits().Validate() != nil {
		t.Fatal("default limits invalid")
	}
}
