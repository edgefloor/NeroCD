package domain

import (
	"testing"
	"time"
)

func scheduleTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, e := time.Parse(time.RFC3339, s)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestDatabaseBackupScheduleNewestBoundary(t *testing.T) {
	for _, tc := range []struct{ name, frequency, now, want string }{
		{"current", "every_minute", "2026-10-25T12:10:39Z", "2026-10-25T12:10:00Z"},
		{"inclusive", "hourly", "2026-10-25T12:10:00Z", "2026-10-25T12:00:00Z"},
		{"outside", "hourly", "2026-10-25T12:10:01Z", ""},
		{"newest_only", "*/2 * * * *", "2026-10-25T12:09:00Z", "2026-10-25T12:08:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, ok, e := NewestHostingDatabaseBackupOccurrence(tc.frequency, "UTC", scheduleTime(t, tc.now), nil)
			if e != nil || ok != (tc.want != "") || ok && !v.Equal(scheduleTime(t, tc.want)) {
				t.Fatalf("due=%v ok=%v err=%v", v, ok, e)
			}
		})
	}
}
func TestDatabaseBackupScheduleDST(t *testing.T) {
	first := scheduleTime(t, "2026-10-25T00:30:00Z")
	for _, tc := range []struct {
		frequency string
		want      bool
	}{{"30 2 * * *", false}, {"30 2-3 * * *", false}, {"30 * * * *", true}, {"30 */1 * * *", true}} {
		v, ok, e := NewestHostingDatabaseBackupOccurrence(tc.frequency, "Europe/Budapest", scheduleTime(t, "2026-10-25T01:30:00Z"), &first)
		if e != nil || ok != tc.want || ok && !v.Equal(scheduleTime(t, "2026-10-25T01:30:00Z")) {
			t.Fatalf("%s due=%v ok=%v err=%v", tc.frequency, v, ok, e)
		}
	}
	if _, ok, e := NewestHostingDatabaseBackupOccurrence("30 2 * * *", "Europe/Budapest", scheduleTime(t, "2026-03-29T01:35:00Z"), nil); e != nil || ok {
		t.Fatal("spring missing wall time invented", e)
	}
	if _, ok, e := NewestHostingDatabaseBackupOccurrence("every_minute", "UTC", first, &first); e != nil || ok {
		t.Fatal("cursor replayed", e)
	}
}
func TestDatabaseBackupScheduleInput(t *testing.T) {
	for alias := range backupScheduleAliases {
		if (HostingDatabaseBackupScheduleInput{RequestID: "req", ExpectedResourceGeneration: 1, Frequency: alias, Timezone: "UTC"}).Validate() != nil {
			t.Fatal(alias)
		}
	}
	for _, change := range []func(*HostingDatabaseBackupScheduleInput){func(v *HostingDatabaseBackupScheduleInput) { v.Timezone = "Local" }, func(v *HostingDatabaseBackupScheduleInput) { v.Timezone = "../zone" }, func(v *HostingDatabaseBackupScheduleInput) { v.Timezone = "UTC\n" }, func(v *HostingDatabaseBackupScheduleInput) { v.Frequency = "@every 1m" }, func(v *HostingDatabaseBackupScheduleInput) { v.Frequency = "CRON_TZ=UTC * * * * *" }, func(v *HostingDatabaseBackupScheduleInput) { v.Frequency = "0 * * * * *" }, func(v *HostingDatabaseBackupScheduleInput) { v.ExpectedVersion = -1 }, func(v *HostingDatabaseBackupScheduleInput) { v.RequestID = "" }} {
		v := HostingDatabaseBackupScheduleInput{RequestID: "req", ExpectedResourceGeneration: 1, Frequency: "daily", Timezone: "UTC"}
		change(&v)
		if v.Validate() == nil {
			t.Fatal("invalid schedule admitted", v)
		}
	}
}

func TestDatabaseBackupScheduleCapturedMinuteBoundary(t *testing.T) {
	tick := scheduleTime(t, "2026-10-25T12:59:59.999Z")
	for _, frequency := range []string{"every_minute", "59 * * * *", "59 12 * * *"} {
		due, ok, err := NewestHostingDatabaseBackupOccurrence(frequency, "UTC", tick, nil)
		if err != nil || !ok || !due.Equal(scheduleTime(t, "2026-10-25T12:59:00Z")) {
			t.Fatal("captured pass drifted across minute boundary", frequency, due, err)
		}
	}
	after := scheduleTime(t, "2026-10-25T13:00:00Z")
	due, ok, err := NewestHostingDatabaseBackupOccurrence("every_minute", "UTC", after, nil)
	if err != nil || !ok || !due.Equal(after) {
		t.Fatal("next pass did not advance its own captured tick", due, err)
	}
}

// The pinned Go parser deliberately implements a smaller calendar syntax than
// Coolify's dragonmantank/cron-expression v3.6.0. Do not silently accept special
// calendar tokens with a different meaning.
func TestDatabaseBackupScheduleCronSubset(t *testing.T) {
	for _, frequency := range []string{"0 0 * * 0", "0 0 * * SUN", "0 0 * * sun", "*/15 8-17 * JAN,MAR MON-FRI", "0 0 ? * SUN"} {
		in := HostingDatabaseBackupScheduleInput{RequestID: "subset", ExpectedResourceGeneration: 1, Frequency: frequency, Timezone: "UTC"}
		if err := in.Validate(); err != nil {
			t.Fatal("supported cron subset rejected", frequency, err)
		}
	}
	for _, frequency := range []string{"0 0 * * 7", "0 0 * * 1-7", "0 0 L * *", "0 0 15W * *", "0 0 * * 5L", "0 0 * * MON#2"} {
		in := HostingDatabaseBackupScheduleInput{RequestID: "subset", ExpectedResourceGeneration: 1, Frequency: frequency, Timezone: "UTC"}
		if in.Validate() == nil {
			t.Fatal("unsupported calendar token admitted", frequency)
		}
	}
	now := scheduleTime(t, "2026-10-25T00:00:00Z")
	for _, frequency := range []string{"0 0 * * 0", "0 0 * * SUN"} {
		due, ok, err := NewestHostingDatabaseBackupOccurrence(frequency, "UTC", now, nil)
		if err != nil || !ok || !due.Equal(now) {
			t.Fatal("Sunday identity changed", frequency, due, err)
		}
	}
}
