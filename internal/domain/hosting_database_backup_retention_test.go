package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBackupRetentionSelectors(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	inventory := func(location string) []BackupRetentionItem {
		v := []BackupRetentionItem{}
		for i := range 6 {
			v = append(v, BackupRetentionItem{CopyID: fmt.Sprint(i), Location: location, CreatedAt: now.AddDate(0, 0, -i), Bytes: 10, Availability: "available"})
		}
		return v
	}
	for _, location := range []string{"local", "remote"} {
		for _, tc := range []struct {
			name string
			l    BackupRetentionLimits
			want []string
		}{
			{"unlimited", BackupRetentionLimits{}, nil},
			{"count", BackupRetentionLimits{KeepCount: 2}, []string{"2", "3", "4", "5"}},
			{"age-newest-relative-strict", BackupRetentionLimits{KeepDays: 2}, []string{"3", "4", "5"}},
			{"size-excludes-newest-and-threshold", BackupRetentionLimits{MaxBytes: 20}, []string{"4", "5"}},
			{"union-dedup", BackupRetentionLimits{KeepCount: 3, KeepDays: 2, MaxBytes: 20}, []string{"3", "4", "5"}},
		} {
			t.Run(location+"/"+tc.name, func(t *testing.T) {
				p := HostingDatabaseBackupRetentionPolicy{Enabled: true}
				if location == "local" {
					p.Local = tc.l
				} else {
					p.Remote = tc.l
				}
				got, local, remote, _ := SelectBackupRetention(p, inventory(location))
				if location == "local" && local == nil || location == "remote" && remote == nil {
					t.Fatal("missing reference")
				}
				var selected []string
				for _, v := range got {
					if v.Selected {
						selected = append(selected, v.CopyID)
					}
				}
				slices.Sort(selected)
				if !slices.Equal(selected, tc.want) {
					t.Fatalf("selected %v want %v", selected, tc.want)
				}
			})
		}
	}
}
func TestBackupRetentionGlobalBatchDoesNotStarve(t *testing.T) {
	now := time.Now().UTC()
	var v []BackupRetentionItem
	for i := range 2200 {
		reasons := []string{}
		if i < 2000 {
			reasons = []string{"shared"}
		}
		v = append(v, BackupRetentionItem{CopyID: fmt.Sprintf("copy_%04d", i), Location: "local", CreatedAt: now.Add(time.Duration(i) * time.Second), Bytes: 10, Availability: "available", ProtectedReasons: reasons})
	}
	got, _, _, truncated := SelectBackupRetention(HostingDatabaseBackupRetentionPolicy{Enabled: true, Local: BackupRetentionLimits{KeepCount: 1}}, v)
	n := 0
	for _, i := range got {
		if i.Selected {
			n++
		}
	}
	if !truncated || n != 100 || len(got) != 1000 {
		t.Fatalf("truncated=%v selected=%d items=%d", truncated, n, len(got))
	}
	if got[0].CopyID != "copy_2000" {
		t.Fatalf("oldest eligible starved: %s", got[0].CopyID)
	}
	if v[0].Selected || len(v[2199].ProtectedReasons) != 0 {
		t.Fatal("input mutated")
	}
}
func TestBackupRetentionProtectsEachReasonAndNewest(t *testing.T) {
	now := time.Now().UTC()
	for _, reason := range []string{"shared", "active", "upload_pending", "unproven_ownership", "external", "unsupported_conditional", "reserved"} {
		v := []BackupRetentionItem{{CopyID: "new", Location: "local", CreatedAt: now, Bytes: 1, Availability: "available"}, {CopyID: "old", Location: "local", CreatedAt: now.Add(-time.Hour), Bytes: 1, Availability: "available", ProtectedReasons: []string{reason}}}
		got, _, _, _ := SelectBackupRetention(HostingDatabaseBackupRetentionPolicy{Enabled: true, Local: BackupRetentionLimits{KeepCount: 1}}, v)
		for _, i := range got {
			if i.Selected {
				t.Fatalf("selected protected %s", reason)
			}
		}
	}
}

func TestBackupRetentionCredentialPurposeAndFamilyAreIndependent(t *testing.T) {
	a := HostingAuthority{OperationID: "retention", RunID: "db_retention_retention", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	in := BackupRemoteCredentialRequest{Authority: a, DeliveryID: "delivery", PlanHash: "sha256:" + strings.Repeat("a", 64), TargetID: "target", TargetRevision: 1, DaemonID: "daemon", RetentionCopyID: "copy"}
	if in.Validate() != nil {
		t.Fatal("dedicated retention request rejected")
	}
	ordinary := in
	ordinary.RetentionCopyID = ""
	if ordinary.Validate() == nil {
		t.Fatal("retention tuple accepted as transfer")
	}
	wrong := in
	wrong.Authority.RunID = "db_transfer_retention"
	if wrong.Validate() == nil {
		t.Fatal("transfer tuple accepted as retention")
	}
	raw, _ := json.Marshal(in)
	for _, bad := range []string{strings.Replace(string(raw), `"retention_copy_id":"copy"`, `"retention_copy_id":"copy","extra":"canary"`, 1), strings.Replace(string(raw), `"delivery_id":"delivery"`, `"delivery_id":"delivery","delivery_id":"other"`, 1)} {
		var decoded BackupRemoteCredentialRequest
		if DecodeHostingDatabaseBackupJSON([]byte(bad), &decoded) == nil {
			t.Fatal("nonclosed credential request accepted")
		}
	}
}

func TestBackupRetentionSizeThresholdPreservesEqualTimestampPrefix(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, location := range []string{"local", "remote"} {
		t.Run(location, func(t *testing.T) {
			// Index 0 is newest in the stable descending copy-ID order, regardless of input order.
			copyID := func(i int) string { return fmt.Sprintf("%c_index_%d", 'z'-rune(i), i) }
			var inventory []BackupRetentionItem
			for _, i := range []int{1, 4, 0, 5, 2, 3} {
				inventory = append(inventory, BackupRetentionItem{CopyID: copyID(i), Location: location, CreatedAt: at, Bytes: 10, Availability: "available"})
			}
			policy := HostingDatabaseBackupRetentionPolicy{Enabled: true}
			if location == "local" {
				policy.Local.MaxBytes = 20
			} else {
				policy.Remote.MaxBytes = 20
			}
			items, _, _, _ := SelectBackupRetention(policy, inventory)
			selected := map[string]bool{}
			for _, item := range items {
				selected[item.CopyID] = item.Selected
			}
			for i := range 6 {
				want := i >= 4
				if selected[copyID(i)] != want {
					t.Errorf("index %d selected=%v want=%v; indices 0–3 must survive", i, selected[copyID(i)], want)
				}
			}
		})
	}
}
