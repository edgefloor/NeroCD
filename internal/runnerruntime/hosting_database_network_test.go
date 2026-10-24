//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDatabaseNetworkLostCreateResponseInspectsExactOrigin(t *testing.T) {
	f, credentials := databaseFixture(t, "redis")
	lost := true
	f.hook = func(ctx context.Context, args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "network" && args[1] == "create" && lost {
			lost = false
			hook := f.hook
			f.hook = nil
			f.authorized = true
			_, err := f.commandLocked(ctx, "docker", append([]string{"--host", f.e.endpoint, "--config", f.e.configDir}, args...), f.e.configDir, nil)
			f.hook = hook
			if err != nil {
				t.Fatal(err)
			}
			return nil, errors.New("network response lost"), true
		}
		return nil, nil, false
	}
	first := f.apply(context.Background(), credentials)
	if first.Ready || !first.Uncertain || f.networkCreates != 1 || f.creates != 0 {
		t.Fatal("lost network create response was accepted", first)
	}
	record, exists, err := f.config.readRecord(f.plan.Database.NetworkName + ".json")
	if err != nil || !exists || record.NetworkID != "" || record.OriginPlan != f.plan.Hash {
		t.Fatal("intent was not durable before network create")
	}
	second := f.apply(context.Background(), credentials)
	if !second.Ready || f.networkCreates != 1 || f.creates != 1 || f.starts != 1 {
		t.Fatal("exact admitted network did not reconcile without another create", second)
	}
}

func TestDatabaseNetworkAdmittedMissingCannotRecreate(t *testing.T) {
	f, credentials := databaseFixture(t, "redis")
	f.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "network" && args[1] == "create" {
			return nil, errors.New("network create unknown"), true
		}
		return nil, nil, false
	}
	first := f.apply(context.Background(), credentials)
	if !first.Uncertain {
		t.Fatal("missing uncertainty")
	}
	f.hook = nil
	second := f.apply(context.Background(), credentials)
	if second.Ready || !second.Uncertain || f.networkCreates != 0 || f.creates != 0 {
		t.Fatal("admitted missing network was recreated")
	}
}

func TestDatabaseNetworkForeignAndTamperedEvidenceRejected(t *testing.T) {
	for _, test := range []string{"no_history", "public_network", "foreign_label", "extra_option", "foreign_attachment", "changed_id", "public_ipam", "extra_network"} {
		t.Run(test, func(t *testing.T) {
			f, credentials := databaseFixture(t, "redis")
			if result := f.apply(context.Background(), credentials); !result.Ready {
				t.Fatal(result)
			}
			switch test {
			case "no_history":
				if err := os.Remove(f.config.stateRoot + "/" + f.plan.Database.NetworkName + ".json"); err != nil {
					t.Fatal(err)
				}
			case "public_network":
				f.network["Internal"] = false
			case "foreign_label":
				f.network["Labels"].(map[string]string)["foreign"] = "label"
			case "extra_option":
				f.network["Options"] = map[string]string{"com.docker.network.bridge.enable_icc": "true"}
			case "foreign_attachment":
				f.network["Containers"].(map[string]any)[strings.Repeat("f", 64)] = map[string]string{"EndpointID": strings.Repeat("c", 64), "IPv4Address": "172.28.0.9/16"}
			case "changed_id":
				f.network["Id"] = strings.Repeat("c", 64)
			case "public_ipam":
				f.network["IPAM"] = map[string]any{"Driver": "default", "Config": []map[string]string{{"Subnet": "8.8.0.0/16", "Gateway": "8.8.0.1"}}}
			case "extra_network":
				for _, container := range f.containers {
					container["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)["bridge"] = map[string]string{"NetworkID": strings.Repeat("a", 64)}
				}
			}
			creates, starts := f.creates, f.starts
			result := f.apply(context.Background(), credentials)
			if result.Ready || f.creates != creates || f.starts != starts {
				t.Fatal("tampered network evidence admitted a writer")
			}
		})
	}
}

func TestDatabaseBindingLockCancellationAndNoDuplicateOperation(t *testing.T) {
	f, _ := databaseFixture(t, "redis")
	first, err := f.config.lock(context.Background(), f.plan.Snapshot.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	second, err := f.config.lock(ctx, f.plan.Snapshot.BindingID)
	if err == nil || second != nil || time.Since(start) > time.Second {
		t.Fatal("binding lock was not canceled within bound")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := f.config.lock(context.Background(), f.plan.Snapshot.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	if err = third.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseNetworkHistoryRejectsUnknownAndOversized(t *testing.T) {
	f, _ := databaseFixture(t, "redis")
	name := f.plan.Database.NetworkName + ".json"
	for _, data := range [][]byte{[]byte(`{"version":1,"Unexpected":"x"}`), []byte(strings.Repeat("x", 8193)), []byte(`{"version":1,"version":1}`)} {
		if err := os.WriteFile(f.config.stateRoot+"/"+name, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.config.readRecord(name); err == nil {
			t.Fatal("untrusted local history accepted")
		}
	}
	var record databaseNetworkRecord
	if json.Unmarshal([]byte(`{}`), &record) != nil {
		t.Fatal("fixture decode")
	}
}
