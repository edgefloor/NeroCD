package domain

import (
	"strings"
	"testing"
)

func TestHostingStorageBackupScheduleInput(t *testing.T) {
	valid := HostingStorageBackupScheduleInput{RequestID: strings.Repeat("a", 32), ExpectedResourceGeneration: 1, Frequency: "daily", Timezone: "Europe/Budapest", Enabled: true}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"request", "timezone", "frequency", "generation", "version", "target"} {
		t.Run(name, func(t *testing.T) {
			v := valid
			switch name {
			case "request":
				v.RequestID = "schedule_request"
			case "timezone":
				v.Timezone = "Local"
			case "frequency":
				v.Frequency = "@every 5m"
			case "generation":
				v.ExpectedResourceGeneration = 0
			case "version":
				v.ExpectedVersion = -1
			case "target":
				v.RemoteTarget = &BackupTargetSelection{}
			}
			if v.Validate() == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}
