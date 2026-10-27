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

func TestHostingComposeStorageBackupScheduleInput(t *testing.T) {
	valid := HostingStorageBackupScheduleInput{RequestID: strings.Repeat("a", 32), ExpectedResourceGeneration: 1, Volume: "uploads", ExpectedDeployedOperationID: "hop_source", AcknowledgeInterruption: true, Frequency: "daily", Timezone: "UTC", Enabled: true}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"no_consent", "no_deployment", "invalid_deployment", "invalid_volume", "invalid_remote", "image_consent", "image_deployment", "disabled_create"} {
		t.Run(name, func(t *testing.T) {
			in := valid
			switch name {
			case "no_consent":
				in.AcknowledgeInterruption = false
			case "no_deployment":
				in.ExpectedDeployedOperationID = ""
			case "invalid_deployment":
				in.ExpectedDeployedOperationID = "../hop"
			case "invalid_volume":
				in.Volume = "../uploads"
			case "invalid_remote":
				in.RemoteTarget = &BackupTargetSelection{TargetID: "target", Revision: 0}
			case "image_consent":
				in.Volume = ""
				in.ExpectedDeployedOperationID = ""
			case "image_deployment":
				in.Volume = ""
				in.AcknowledgeInterruption = false
			case "disabled_create":
				in.Enabled = false
				in.ExpectedDeployedOperationID = ""
			}
			if in.Validate() == nil {
				t.Fatal("invalid selector accepted")
			}
		})
	}
	valid.RemoteTarget = &BackupTargetSelection{TargetID: "target", Revision: 1}
	if err := valid.Validate(); err != nil {
		t.Fatal("remote Compose schedule rejected", err)
	}
	disabled := valid
	disabled.ExpectedVersion = 1
	disabled.Enabled = false
	disabled.ExpectedDeployedOperationID = ""
	disabled.AcknowledgeInterruption = false
	if err := disabled.Validate(); err != nil {
		t.Fatal("runtime-independent disable rejected", err)
	}
}
