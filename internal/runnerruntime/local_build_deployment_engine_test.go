package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func localEnginePlan(t *testing.T) domain.HostingApplyPlan {
	t.Helper()
	d := "sha256:" + strings.Repeat("a", 64)
	a := domain.HostingBuildArtifact{Origin: "local_build", BuildID: "build", SourceID: "source", InputID: "input", SourceReceiptHash: d, ContextDigest: d, RequestDigest: d, DaemonID: "daemon-image-test", ImageID: d, IndexDigest: d, ManifestDigest: d, ConfigDigest: d, Platform: "linux/amd64"}
	r, err := domain.NewHostingLocalBuildRelease(a)
	if err != nil {
		t.Fatal(err)
	}
	s := imageTestPlan(t).Snapshot
	s.ImageReference = ""
	s.PolicyVersion = domain.HostingLocalBuildPolicyVersion
	s.LocalBuild = domain.HostingLocalBuildSource{SourceGeneration: 1, Artifact: a, ArtifactHash: domain.HostingDocumentHash(a)}
	p, err := domain.CompileLocalBuildHostingPlan(s, r, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocalArtifactPreparationAndMissingImageNeverFetchOrMutate(t *testing.T) {
	for _, mode := range []string{"present", "missing", "wrong-daemon", "wrong-platform", "wrong-id"} {
		t.Run(mode, func(t *testing.T) {
			p := localEnginePlan(t)
			e := testImageEngine(t)
			e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				if args[4] == "info" {
					if mode == "wrong-daemon" {
						return []byte(`"other"`), nil
					}
					return []byte(`"daemon-image-test"`), nil
				}
				if args[4] != "image" || args[5] != "inspect" || args[6] != p.Release.ImageID {
					t.Fatalf("local image used fetch/tag/mutation: %v", args)
				}
				if mode == "missing" {
					return nil, errors.New("absent")
				}
				i := inspectedImage{ID: p.Release.ImageID, OS: "linux", Architecture: "amd64", RepoDigests: []string{"unrelated:tag"}}
				if mode == "wrong-platform" {
					i.Architecture = "arm64"
				}
				if mode == "wrong-id" {
					i.ID = "sha256:" + strings.Repeat("b", 64)
				}
				return json.Marshal(i)
			})
			r, err := e.Prepare(t.Context(), p.Snapshot)
			if (err == nil) != (mode == "present") {
				t.Fatal("incorrect preparation evidence")
			}
			if mode == "present" && r != p.Release {
				t.Fatal("artifact changed")
			}
			if err = e.EnsurePreparedRelease(t.Context(), p.Release); (err == nil) != (mode == "present") {
				t.Fatal("cache verification changed outcome")
			}
			if mode != "present" && e.Apply(t.Context(), "operation", p).Outcome == "succeeded" {
				t.Fatal("missing/unrelated image applied")
			}
		})
	}
}

func TestLocalArtifactApplyUsesExactIDAndNeverPulls(t *testing.T) {
	p := localEnginePlan(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	port, err := strconv.Atoi(strings.TrimPrefix(server.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	p.Snapshot.HostPort = port
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	e := testImageEngine(t)
	id := strings.Repeat("c", 64)
	created := false
	e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		if args[4] == "info" {
			return []byte(`"daemon-image-test"`), nil
		}
		if args[4] == "image" && args[5] == "inspect" && args[6] == p.Release.ImageID {
			return json.Marshal(inspectedImage{ID: p.Release.ImageID, OS: "linux", Architecture: "amd64"})
		}
		if args[4] == "container" {
			switch args[5] {
			case "ls":
				return nil, nil
			case "create":
				if args[len(args)-1] != p.Release.ImageID || !strings.Contains(strings.Join(args, " "), "--pull=never") {
					t.Fatalf("local create not pinned: %v", args)
				}
				created = true
				return []byte(id), nil
			case "start":
				return nil, nil
			case "inspect":
				return json.Marshal(map[string]any{"Id": id, "Image": p.Release.ImageID, "Config": map[string]any{"Labels": map[string]string{imageBindingLabel: p.Snapshot.BindingID, imageOperationLabel: "operation", imageReleaseLabel: p.Release.Hash, imagePlanLabel: p.Hash}}, "State": map[string]bool{"Running": true}, "HostConfig": map[string]any{"PortBindings": map[string]any{"80/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(port)}}}}})
			}
		}
		t.Fatalf("local apply attempted unexpected effect: %v", args)
		return nil, nil
	})
	if r := e.Apply(t.Context(), "operation", p); !created || r.Outcome != "succeeded" || !r.Ready {
		t.Fatalf("local apply: %+v", r)
	}
}

func TestLocalArtifactCleanupDoesNotRequireImageCache(t *testing.T) {
	p := localEnginePlan(t)
	id := strings.Repeat("c", 64)
	target := domain.HostingHealthy{Snapshot: p.Snapshot, Release: p.Release, OperationID: "old", ContainerID: id, PlanHash: p.Hash}
	cleanup, err := domain.CompileHostingCleanupPlan(target, "current")
	if err != nil {
		t.Fatal(err)
	}
	e := testImageEngine(t)
	removed := false
	e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		if args[4] == "info" {
			return []byte(`"daemon-image-test"`), nil
		}
		if args[4] != "container" {
			t.Fatalf("cleanup consulted missing image cache: %v", args)
		}
		switch args[5] {
		case "ls":
			if removed {
				return nil, nil
			}
			return []byte(id), nil
		case "inspect":
			return json.Marshal(map[string]any{"Id": id, "Image": p.Release.ImageID, "Config": map[string]any{"Labels": map[string]string{imageBindingLabel: p.Snapshot.BindingID, imageOperationLabel: "old", imageReleaseLabel: p.Release.Hash, imagePlanLabel: p.Hash}}, "State": map[string]bool{"Running": true}, "HostConfig": map[string]any{"PortBindings": map[string]any{"80/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(p.Snapshot.HostPort)}}}}})
		case "stop":
			if args[len(args)-1] != id {
				t.Fatal("wrong cleanup target")
			}
			return nil, nil
		case "rm":
			if args[len(args)-1] != id {
				t.Fatal("wrong cleanup target")
			}
			removed = true
			return nil, nil
		}
		t.Fatalf("unexpected cleanup effect: %v", args)
		return nil, nil
	})
	if result := e.Cleanup(t.Context(), cleanup); result.Outcome != "succeeded" || !removed || result.Uncertain {
		t.Fatalf("cache-independent cleanup: %+v", result)
	}
}
