//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
)

func TestOwnedStorageRuntimeApplyProtectedTransportAndWindow(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "expired", "stale_reference", "legacy_entry", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := mountedTestFixture(t, "")
			snap := f.plan.Snapshot
			snap.PolicyVersion = domain.HostingOwnedStorageRuntimePolicyVersion
			snap.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("e", 64)}
			p, err := domain.CompileHostingOwnedStorageRuntimePlan(snap, f.plan.Release, nil, nil, *f.plan.Storage)
			if err != nil {
				t.Fatal(err)
			}
			f.plan = p
			installHostingEnvironmentTestDocker(t)
			ref := snap.RuntimeConfiguration
			if mode == "stale_reference" {
				ref.Generation++
			}
			until := time.Now().Add(time.Minute)
			if mode == "expired" {
				until = time.Now().Add(-time.Second)
			}
			material, err := hostingruntimeprotocol.NewEnvironment(hostingruntimeprotocol.Metadata{Authority: domain.HostingAuthority{OperationID: "hop_candidate", RunID: "run_hop_candidate", LeaseID: "lease", Attempt: 1, Fence: "fence"}, PlanHash: p.Hash, RuntimeConfiguration: ref, DeliveryID: strings.Repeat("a", 32), RequestHash: "sha256:" + strings.Repeat("b", 64), Purpose: hostingruntimeprotocol.Purpose, UseUntil: until}, []hostingruntimeprotocol.Entry{{Name: "APP_EMPTY", Value: []byte{}}, {Name: "APP_SECRET", Value: []byte("fixture-value-$#= Unicode-árvíz  ")}})
			if err != nil {
				t.Fatal(err)
			}
			defer material.Close()
			if mode == "missing" {
				material.Close()
				material = nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			var result domain.HostingResult
			if mode == "legacy_entry" {
				result = f.engine.ApplyWithOwnedStorage(ctx, "hop_candidate", p, f.request, f.evidence)
			} else {
				result = f.engine.ApplyWithOwnedStorageRuntimeEnvironment(ctx, "hop_candidate", p, f.request, f.evidence, material, time.Now().Add(time.Minute))
			}
			raw, readErr := os.ReadFile(filepath.Join(f.engine.configDir, "observation.json"))
			if mode != "valid" {
				if readErr == nil || f.created || result.Outcome != "failed" {
					t.Fatal("invalid runtime admission launched mutation")
				}
				return
			}
			if readErr != nil {
				t.Fatal("protected create did not reach helper", readErr)
			}
			var observed struct {
				Args, Env []string
				Launches  int
			}
			if json.Unmarshal(raw, &observed) != nil || observed.Launches != 1 {
				t.Fatal("unexpected protected launches")
			}
			joined := strings.Join(observed.Args, " ")
			if !strings.Contains(joined, "--env-file /dev/stdin") || !strings.Contains(joined, "--mount type=volume,src="+p.Storage.VolumeName+",dst="+p.Storage.MountPath+",volume-nocopy") || strings.Contains(string(raw), "fixture-value") {
				t.Fatal("secret escaped stdin or storage mount missing")
			}
			// The helper supplies no daemon container observation, so the shared engine
			// correctly retains uncertainty after creation instead of inventing success.
			if !result.Uncertain {
				t.Fatal("unobserved create became success")
			}
		})
	}
}
