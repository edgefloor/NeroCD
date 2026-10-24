package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func captureJournalFixture(t *testing.T, engine string) []JournalDatabaseNetworkCapture {
	t.Helper()
	base := databaseJournalFixture(t)[2]
	st, source := *base.StorageMetadata, *base.Plan
	if engine == "redis" {
		r, err := domain.HostingDatabaseRecipeFor(engine, domain.HostingDatabaseRedisRecipe)
		if err != nil {
			t.Fatal(err)
		}
		st.MountPath = r.MountPath
		st.Hash = st.DocumentHash()
		s := source.Snapshot
		s.ImageReference, s.ContainerPort, s.Storage = r.ImageReference, r.ContainerPort, st.Reference()
		s.Database = domain.HostingDatabaseExecutionRef{Engine: engine, RecipeVersion: r.Version}
		refs := append([]domain.HostingDatabaseCredentialRef(nil), r.Credentials...)
		for i := range refs {
			refs[i].SecretID = domain.HostingDatabaseSecretID(s.ResourceID, refs[i].Role)
			refs[i].Version = 1
		}
		s.Database.ConfigurationHash, err = domain.HostingDatabaseConfigurationHash(s, refs)
		if err != nil {
			t.Fatal(err)
		}
		platform := r.Platforms[0]
		release := domain.HostingRelease{CanonicalImage: strings.Split(r.ImageReference, "@")[0] + "@" + platform.Digest, ImageID: "sha256:" + strings.Repeat("b", 64), OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant, DaemonID: source.Release.DaemonID}
		release.Hash = domain.HostingDocumentHash(release)
		sp := *source.Storage
		sp.Reference, sp.MountPath = st.Reference(), st.MountPath
		sp.CreationNonce = domain.HostingStorageCreationNonce(sp.Reference, sp.CreationOperationID, sp.DaemonID)
		source, err = domain.CompileHostingDatabasePlan(s, release, nil, sp, domain.HostingDatabasePlan{Reference: s.Database, NetworkName: domain.HostingDatabaseNetworkName(s.BindingID), Credentials: refs})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately expired: historical journal load must not apply a live clock.
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	sp := *source.Storage
	healthy := domain.HostingHealthy{OperationID: sp.CreationOperationID, Snapshot: source.Snapshot, Release: source.Release, PlanHash: source.Hash, ContainerID: strings.Repeat("a", 64), Storage: &domain.HostingHealthyStorage{Plan: sp, CreationPlanHash: source.Hash}}
	receipt := domain.HostingReceipt{ID: "created", BodyHash: "sha256:" + strings.Repeat("b", 64), CreatedAt: now.Add(-time.Hour)}
	m := domain.HostingStorageMaterialization{StorageRef: st.Reference(), State: "materialized", CreationOperationID: sp.CreationOperationID, CreationPlanHash: source.Hash, CreationNonce: sp.CreationNonce, DaemonID: sp.DaemonID, MaterializedReceipt: &receipt}
	m.Labels = domain.ExpectedHostingStorageLabels(st, m)
	p := domain.HostingDatabaseNetworkCapturePlan{Version: domain.HostingDatabaseNetworkCaptureVersion, OperationID: domain.HostingDatabaseNetworkCaptureID(healthy.OperationID), SourceOperationID: healthy.OperationID, ProjectID: st.ProjectID, ResourceID: st.ResourceID, BindingID: st.BindingID, DestinationID: st.DestinationID, ServerID: source.Snapshot.ServerID, RunnerID: source.Snapshot.RunnerID, SourcePlan: source, SourceHealthy: healthy, Storage: st, Materialization: m, Creation: domain.HostingDatabaseNetworkCaptureCreation{Receipt: receipt, VolumeName: st.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}}}
	p.Hash = p.DocumentHash()
	if p.Validate() != nil {
		t.Fatal("invalid capture plan fixture")
	}
	l := domain.RunLease{ID: "capture_lease", RunID: "capture_" + p.OperationID, RunnerID: p.RunnerID, Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute), Attempt: 1, Fence: "capture_fence"}
	a := AttemptIdentity{RunID: l.RunID, LeaseID: l.ID, Attempt: l.Attempt, Fence: l.Fence, CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt}
	c := domain.HostingDatabaseNetworkCapture{ID: p.OperationID, RunID: l.RunID, State: "running", CreatedBy: "owner", Plan: p, CreatedAt: now.Add(-time.Second), UpdatedAt: now}
	claim := domain.HostingDatabaseNetworkCaptureClaim{Capture: c, Lease: l}
	in := domain.HostingDatabaseNetworkOriginRequest{HostingAuthority: domain.HostingAuthority{OperationID: c.ID, RunID: l.RunID, LeaseID: l.ID, Attempt: l.Attempt, Fence: l.Fence}, ReceiptID: "origin_receipt", PlanHash: p.Hash, ObservedAt: now.Add(time.Second), Origin: domain.HostingDatabaseNetworkOrigin{Version: 1, Binding: p.BindingID, Destination: p.DestinationID, Daemon: m.DaemonID, Configuration: source.Snapshot.Database.ConfigurationHash, OriginOperation: m.CreationOperationID, OriginPlan: m.CreationPlanHash, Nonce: strings.Repeat("c", 32), Storage: st.Reference(), VolumeNonce: m.CreationNonce, VolumeCreatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), NetworkID: strings.Repeat("d", 64), InitReady: true}}
	ack := domain.HostingDatabaseNetworkOriginReceipt{HostingReceipt: domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in), CreatedAt: now.Add(2 * time.Second)}, OperationID: c.ID, SourceOperationID: p.SourceOperationID, PlanHash: p.Hash, OriginHash: in.Origin.DocumentHash(), Origin: in.Origin, ObservedAt: in.ObservedAt, RunnerID: p.RunnerID}
	return []JournalDatabaseNetworkCapture{
		{ID: "capture_claim", Attempt: a, Phase: "claim", Claim: &claim, CreatedAt: now},
		{ID: "capture_plan", Attempt: a, Phase: "plan", Plan: &c, CreatedAt: now},
		{ID: "capture_origin", Attempt: a, Phase: "origin", Origin: &in, CreatedAt: now.Add(time.Second)},
		{ID: "capture_ack", Attempt: a, Phase: "ack", Ack: &ack, CreatedAt: now.Add(2 * time.Second)},
	}
}

func appendCapture(t *testing.T, j *AttemptJournal, entries []JournalDatabaseNetworkCapture) {
	t.Helper()
	for _, e := range entries {
		if _, err := j.AppendDatabaseNetworkCapture(e); err != nil {
			t.Fatalf("%s: %v", e.Phase, err)
		}
	}
}

func TestDatabaseNetworkCaptureJournalRestartOriginalExchange(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			entries := captureJournalFixture(t, engine)
			j, dir := openBuildJournal(t)
			for i, e := range entries {
				appendCapture(t, j, []JournalDatabaseNetworkCapture{e})
				before := j.Snapshot()
				appendCapture(t, j, []JournalDatabaseNetworkCapture{e})
				if !reflect.DeepEqual(before, j.Snapshot()) || j.Depth() != i+1 || j.state.Version != 13 {
					t.Fatal("duplicate changed exchange")
				}
				if i < 3 && j.RetireDatabaseNetworkCapture(e.Attempt) == nil {
					t.Fatal("retired unresolved exchange")
				}
				if j.DiscardAttempt(e.Attempt) == nil {
					t.Fatal("discarded capture")
				}
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				j, err = OpenAttemptJournal(dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := j.Close(); err != nil {
						t.Error(err)
					}
				})
				if !reflect.DeepEqual(before, j.Snapshot()) {
					t.Fatal("restart changed original evidence")
				}
			}
			wrong := entries[0].Attempt
			wrong.Fence = "different"
			if err := j.RetireDatabaseNetworkCapture(wrong); err != nil || j.Depth() != 4 {
				t.Fatal("foreign retirement changed exchange")
			}
			if err := j.RetireDatabaseNetworkCapture(entries[0].Attempt); err != nil || j.Depth() != 0 || j.state.Version != 8 {
				t.Fatal("acknowledged retirement failed", err)
			}
		})
	}
}

func TestDatabaseNetworkCaptureJournalRejectsPhaseSubstitution(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, tc := range []struct {
			name   string
			index  int
			change func(*JournalDatabaseNetworkCapture)
		}{
			{"credential", 0, func(e *JournalDatabaseNetworkCapture) { e.Claim.Lease.CompletionKey = "forbidden" }},
			{"claim_fence", 0, func(e *JournalDatabaseNetworkCapture) { e.Claim.Lease.Fence = "foreign" }},
			{"claim_runner", 0, func(e *JournalDatabaseNetworkCapture) { e.Claim.Lease.RunnerID = "foreign" }},
			{"claim_terminal", 0, func(e *JournalDatabaseNetworkCapture) { e.Claim.Lease.Status = "completed" }},
			{"plan_actor", 1, func(e *JournalDatabaseNetworkCapture) { e.Plan.CreatedBy = "foreign" }},
			{"foreign_plan", 1, func(e *JournalDatabaseNetworkCapture) { e.Plan.Plan.Creation.Receipt.ID = "foreign" }},
			{"mixed_payload", 1, func(e *JournalDatabaseNetworkCapture) { e.Ack = &domain.HostingDatabaseNetworkOriginReceipt{} }},
			{"origin_authority", 2, func(e *JournalDatabaseNetworkCapture) { e.Origin.Fence = "foreign" }},
			{"origin_daemon", 2, func(e *JournalDatabaseNetworkCapture) { e.Origin.Origin.Daemon = "foreign" }},
			{"origin_plan", 2, func(e *JournalDatabaseNetworkCapture) { e.Origin.PlanHash = "sha256:" + strings.Repeat("f", 64) }},
			{"before_lease", 2, func(e *JournalDatabaseNetworkCapture) {
				e.Origin.ObservedAt = e.Attempt.CreatedAt.Add(-31 * time.Second)
			}},
			{"after_lease", 2, func(e *JournalDatabaseNetworkCapture) {
				e.Origin.ObservedAt = e.Attempt.ExpiresAt.Add(31 * time.Second)
			}},
			{"ack_body", 3, func(e *JournalDatabaseNetworkCapture) { e.Ack.BodyHash = e.Ack.PlanHash }},
			{"ack_id", 3, func(e *JournalDatabaseNetworkCapture) { e.Ack.ID = "foreign" }},
			{"ack_runner", 3, func(e *JournalDatabaseNetworkCapture) { e.Ack.RunnerID = "foreign" }},
			{"ack_source", 3, func(e *JournalDatabaseNetworkCapture) { e.Ack.SourceOperationID = "foreign" }},
			{"ack_origin", 3, func(e *JournalDatabaseNetworkCapture) { e.Ack.Origin.NetworkID = strings.Repeat("e", 64) }},
			{"ack_hash", 3, func(e *JournalDatabaseNetworkCapture) { e.Ack.OriginHash = e.Ack.PlanHash }},
			{"ack_time", 3, func(e *JournalDatabaseNetworkCapture) { e.Ack.ObservedAt = e.Ack.ObservedAt.Add(time.Second) }},
			{"ack_early", 3, func(e *JournalDatabaseNetworkCapture) { e.Ack.CreatedAt = e.Ack.ObservedAt.Add(-31 * time.Second) }},
		} {
			t.Run(engine+"/"+tc.name, func(t *testing.T) {
				entries := captureJournalFixture(t, engine)
				j, dir := openBuildJournal(t)
				appendCapture(t, j, entries[:tc.index])
				before := j.Snapshot()
				version := j.state.Version
				e := cloneDatabaseNetworkCaptures(entries)[tc.index]
				tc.change(&e)
				if _, err := j.AppendDatabaseNetworkCapture(e); err == nil {
					t.Fatal("invalid evidence accepted")
				}
				if !reflect.DeepEqual(before, j.Snapshot()) || version != j.state.Version {
					t.Fatal("rejection changed state")
				}
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := OpenAttemptJournal(dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := reopened.Close(); err != nil {
						t.Error(err)
					}
				})
				if !reflect.DeepEqual(before, reopened.Snapshot()) {
					t.Fatal("rejection changed disk")
				}
			})
		}
	}
}

func TestDatabaseNetworkCaptureJournalOrderingAndCopies(t *testing.T) {
	entries := captureJournalFixture(t, "postgresql")
	for _, i := range []int{1, 2, 3} {
		j, _ := openBuildJournal(t)
		if _, err := j.AppendDatabaseNetworkCapture(entries[i]); err == nil || j.Depth() != 0 {
			t.Fatal("out-of-order phase accepted")
		}
	}
	j, _ := openBuildJournal(t)
	returned, err := j.AppendDatabaseNetworkCapture(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Claim.Capture.Plan.Creation.Labels["injected"] = "caller"
	returned.Claim.Capture.Plan.Materialization.Labels["injected"] = "returned"
	snapshot := j.Snapshot()
	snapshot.DatabaseNetworkCapture[0].Claim.Capture.Plan.SourcePlan.Database.Credentials[0].SecretID = "foreign"
	if j.Snapshot().DatabaseNetworkCapture[0].Claim.Capture.Plan.SourcePlan.Database.Credentials[0].SecretID == "foreign" || j.Snapshot().DatabaseNetworkCapture[0].Claim.Capture.Plan.Creation.Labels["injected"] != "" {
		t.Fatal("capture aliases mutable input/output")
	}
	clean := captureJournalFixture(t, "postgresql")
	// Reuse the already accepted immutable plan, whose audit time belongs to that claim.
	clean[0] = j.Snapshot().DatabaseNetworkCapture[0]
	clean[1].Plan = &clean[0].Claim.Capture
	changed := cloneDatabaseNetworkCaptures(clean)[0]
	changed.CreatedAt = changed.CreatedAt.Add(time.Second)
	if _, err := j.AppendDatabaseNetworkCapture(changed); err == nil {
		t.Fatal("changed ID duplicate accepted")
	}
	duplicate := cloneDatabaseNetworkCaptures(clean)[0]
	duplicate.ID = "second_claim"
	if _, err := j.AppendDatabaseNetworkCapture(duplicate); err == nil {
		t.Fatal("second phase accepted")
	}
}

func TestDatabaseNetworkCaptureJournalPersistenceRollback(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(fmt.Sprint(retire), func(t *testing.T) {
			j, dir := openBuildJournal(t)
			entries := captureJournalFixture(t, "postgresql")
			if retire {
				appendCapture(t, j, entries)
			} else {
				appendCapture(t, j, entries[:2])
			}
			before, version := j.Snapshot(), j.state.Version
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			if retire {
				err = j.RetireDatabaseNetworkCapture(entries[0].Attempt)
			} else {
				_, err = j.AppendDatabaseNetworkCapture(entries[2])
			}
			if err == nil || !reflect.DeepEqual(before, j.Snapshot()) || j.state.Version != version {
				t.Fatal("failed persistence changed memory", err)
			}
			reopened, err := OpenAttemptJournal(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := reopened.Close(); err != nil {
					t.Error(err)
				}
			})
			if !reflect.DeepEqual(before, reopened.Snapshot()) {
				t.Fatal("failed persistence changed disk")
			}
		})
	}
}

func TestDatabaseNetworkCaptureJournalStrictV13Documents(t *testing.T) {
	entries := captureJournalFixture(t, "postgresql")
	for version := 1; version <= 12; version++ {
		if validateJournalState(journalState{Version: version, DatabaseNetworkCapture: entries}) == nil {
			t.Fatalf("v%d accepted capture", version)
		}
	}
	if validateJournalState(journalState{Version: 13}) == nil {
		t.Fatal("empty v13 accepted")
	}
	raw, err := json.Marshal(journalState{Version: 13, DatabaseNetworkCapture: entries})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"phase":"claim"`, `"phase":"claim","phase":"plan"`, 1),
		strings.Replace(string(raw), `"phase":"claim"`, `"Phase":"claim","phase":"claim"`, 1),
		strings.Replace(string(raw), `"phase":"claim"`, `"unknown":true,"phase":"claim"`, 1),
		strings.Replace(string(raw), `"phase":"claim"`, `"phase":null`, 1),
		strings.Replace(string(raw), `"version":13`, `"version":14`, 1),
	} {
		j, dir := openBuildJournal(t)
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "journal.json"), []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if reopened, err := OpenAttemptJournal(dir); err == nil {
			if err := reopened.Close(); err != nil {
				t.Error(err)
			}
			t.Fatal("malformed journal loaded")
		}
	}
}

func TestDatabaseNetworkCaptureJournalExpiredClaimCannotBeReplaced(t *testing.T) {
	j, dir := openBuildJournal(t)
	entries := captureJournalFixture(t, "postgresql")
	appendCapture(t, j, entries[:1])
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	before := reopened.Snapshot()
	replacement := cloneDatabaseNetworkCaptures(entries)[0]
	replacement.ID = "renewed_claim"
	replacement.Attempt.Attempt++
	replacement.Attempt.LeaseID = "renewed_lease"
	replacement.Attempt.Fence = "renewed_fence"
	replacement.Claim.Lease.Attempt = replacement.Attempt.Attempt
	replacement.Claim.Lease.ID = replacement.Attempt.LeaseID
	replacement.Claim.Lease.Fence = replacement.Attempt.Fence
	if _, err := reopened.AppendDatabaseNetworkCapture(replacement); err == nil {
		t.Fatal("expiry replaced unresolved claim")
	}
	if !reflect.DeepEqual(before, reopened.Snapshot()) {
		t.Fatal("replacement changed original claim")
	}
}

func TestDatabaseNetworkCaptureJournalMixedFamiliesPreserveV13(t *testing.T) {
	cases := []struct {
		name    string
		version int
		append  func(*testing.T, *AttemptJournal) error
	}{
		{"event", 8, func(t *testing.T, j *AttemptJournal) error {
			_, err := j.AppendEvent(JournalEvent{ID: "other_event", Attempt: testJournalAuthority(), Sequence: 1, Stream: "stdout"})
			return err
		}},
		{"completion", 8, func(t *testing.T, j *AttemptJournal) error {
			_, err := j.AppendCompletion(JournalCompletion{ID: "other_completion", Attempt: testJournalAuthority(), Status: "succeeded"})
			return err
		}},
		{"provenance", 8, func(t *testing.T, j *AttemptJournal) error {
			_, err := j.AppendProvenance(JournalProvenance{ID: "other_provenance", Attempt: testJournalAuthority(), DeploymentID: "deployment", GitCommit: "commit", ComposeHash: "compose", ContentIdentity: "commit:compose", ImageDigests: []string{"sha256:nonsecret"}, CreatedAt: time.Now().UTC()})
			return err
		}},
		{"hosting", 8, func(t *testing.T, j *AttemptJournal) error {
			e := runtimeJournalFixture(t)
			_, err := j.AppendHosting(e)
			return err
		}},
		{"proxy", 8, func(t *testing.T, j *AttemptJournal) error {
			_, err := j.AppendProxy(proxyJournalFixture(t))
			return err
		}},
		{"build", 8, func(t *testing.T, j *AttemptJournal) error {
			e, _, _ := buildJournalFixture()
			_, err := j.AppendBuild(e)
			return err
		}},
		{"build_logs", 8, func(t *testing.T, j *AttemptJournal) error {
			e, _, _ := buildJournalFixture()
			if _, err := j.AppendBuild(e); err != nil {
				return err
			}
			_, err := j.AppendBuildLog(e.Attempt, e.BuildID, "buildkit", "stdout", "nonsecret")
			return err
		}},
		{"storage", 10, func(t *testing.T, j *AttemptJournal) error {
			e, _ := storageJournalFixture(t)
			_, err := j.AppendHosting(e)
			return err
		}},
		{"database", 11, func(t *testing.T, j *AttemptJournal) error {
			_, err := j.AppendHosting(databaseJournalFixture(t)[0])
			return err
		}},
		{"archive", 12, func(t *testing.T, j *AttemptJournal) error {
			_, err := j.AppendStorageArchive(archiveJournalEntry(t))
			return err
		}},
	}
	for _, tc := range cases {
		for _, captureFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/capture_first_%t", tc.name, captureFirst), func(t *testing.T) {
				j, dir := openBuildJournal(t)
				entries := captureJournalFixture(t, "postgresql")
				if captureFirst {
					appendCapture(t, j, entries)
				}
				if err := tc.append(t, j); err != nil {
					t.Fatal(err)
				}
				if !captureFirst {
					appendCapture(t, j, entries)
				}
				if j.state.Version != 13 {
					t.Fatal("legacy append downgraded capture")
				}
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := OpenAttemptJournal(dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := reopened.Close(); err != nil {
						t.Error(err)
					}
				})
				if reopened.Depth() != j.Depth() {
					t.Fatal("mixed journal lost entries")
				}
				if err := reopened.RetireDatabaseNetworkCapture(entries[0].Attempt); err != nil {
					t.Fatal(err)
				}
				if reopened.state.Version != tc.version {
					t.Fatalf("retired version=%d want=%d", reopened.state.Version, tc.version)
				}
				if err := reopened.Close(); err != nil {
					t.Fatal(err)
				}
				final, err := OpenAttemptJournal(dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := final.Close(); err != nil {
						t.Error(err)
					}
				})
				if final.state.Version != tc.version || len(final.Snapshot().DatabaseNetworkCapture) != 0 {
					t.Fatal("retired normalization did not persist")
				}
			})
		}
	}
}

func TestDatabaseNetworkCaptureJournalGlobalIDsAndGenericDenial(t *testing.T) {
	entries := captureJournalFixture(t, "postgresql")
	for _, captureFirst := range []bool{false, true} {
		for _, sameID := range []bool{false, true} {
			t.Run(fmt.Sprintf("capture_first_%t/same_id_%t", captureFirst, sameID), func(t *testing.T) {
				j, _ := openBuildJournal(t)
				e := JournalEvent{ID: "generic_event", Attempt: entries[0].Attempt, Sequence: 1, Stream: "stdout"}
				if sameID {
					e.ID = entries[0].ID
					e.Attempt = testJournalAuthority()
				}
				if captureFirst {
					appendCapture(t, j, entries[:1])
				} else if _, err := j.AppendEvent(e); err != nil {
					t.Fatal(err)
				}
				before, version := j.Snapshot(), j.state.Version
				var err error
				if captureFirst {
					_, err = j.AppendEvent(e)
				} else {
					_, err = j.AppendDatabaseNetworkCapture(entries[0])
				}
				if err == nil || !reflect.DeepEqual(before, j.Snapshot()) || version != j.state.Version {
					t.Fatal("cross-family collision changed state", err)
				}
			})
		}
	}
	j, _ := openBuildJournal(t)
	appendCapture(t, j, entries[:1])
	if _, err := j.AppendCompletion(JournalCompletion{ID: "generic_complete", Attempt: entries[0].Attempt, Status: "succeeded"}); err == nil {
		t.Fatal("generic completion accepted")
	}
}

func TestDatabaseNetworkCaptureJournalOlderAcknowledgmentsKeepV13(t *testing.T) {
	for _, family := range []string{"event", "completion", "provenance", "hosting", "proxy", "build", "archive"} {
		t.Run(family, func(t *testing.T) {
			j, dir := openBuildJournal(t)
			entries := captureJournalFixture(t, "postgresql")
			appendCapture(t, j, entries)
			var ack func() error
			switch family {
			case "event":
				if _, err := j.AppendEvent(JournalEvent{ID: "old_event", Attempt: testJournalAuthority(), Sequence: 1, Stream: "stdout"}); err != nil {
					t.Fatal(err)
				}
				ack = func() error { return j.AckEvents([]string{"old_event"}) }
			case "completion":
				if _, err := j.AppendCompletion(JournalCompletion{ID: "old_complete", Attempt: testJournalAuthority(), Status: "failed"}); err != nil {
					t.Fatal(err)
				}
				ack = func() error { return j.AckCompletion("old_complete") }
			case "provenance":
				if _, err := j.AppendProvenance(JournalProvenance{ID: "old_provenance", Attempt: testJournalAuthority(), DeploymentID: "deployment", GitCommit: "commit", ComposeHash: "compose", ContentIdentity: "commit:compose", ImageDigests: []string{"digest"}, CreatedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				ack = func() error { return j.AckProvenance("old_provenance") }
			case "hosting":
				e := runtimeJournalFixture(t)
				if _, err := j.AppendHosting(e); err != nil {
					t.Fatal(err)
				}
				ack = func() error { return j.AckHosting(e.ID) }
			case "proxy":
				e := proxyJournalFixture(t)
				if _, err := j.AppendProxy(e); err != nil {
					t.Fatal(err)
				}
				ack = func() error { return j.AckProxy(e.ID) }
			case "build":
				e, _, _ := buildJournalFixture()
				if _, err := j.AppendBuild(e); err != nil {
					t.Fatal(err)
				}
				ack = func() error { return j.AckBuild(e.ID) }
			case "archive":
				e := archiveJournalEntry(t)
				if _, err := j.AppendStorageArchive(e); err != nil {
					t.Fatal(err)
				}
				ack = func() error { return j.AckStorageArchive(e.ID) }
			}
			if err := ack(); err != nil {
				t.Fatal(err)
			}
			if j.state.Version != 13 || j.Depth() != 4 {
				t.Fatal("older acknowledgment lost capture or format")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenAttemptJournal(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := reopened.Close(); err != nil {
					t.Error(err)
				}
			})
			if len(reopened.Snapshot().DatabaseNetworkCapture) != 4 {
				t.Fatal("older acknowledgment lost durable capture")
			}
		})
	}
}

func TestDatabaseNetworkCaptureJournalRejectsUnencodableTime(t *testing.T) {
	j, _ := openBuildJournal(t)
	e := captureJournalFixture(t, "postgresql")[0]
	e.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	before := j.Snapshot()
	if _, err := j.AppendDatabaseNetworkCapture(e); err == nil {
		t.Fatal("unencodable time accepted")
	}
	if !reflect.DeepEqual(before, j.Snapshot()) || j.state.Version != 1 {
		t.Fatal("invalid time changed journal")
	}
}
