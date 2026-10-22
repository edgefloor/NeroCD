package runnerruntime

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestImageDockerIntegration(t *testing.T) {
	tag, versionB := os.Getenv("NEROCD_IMAGE_TEST_TAG"), os.Getenv("NEROCD_IMAGE_TEST_B")
	if tag == "" || versionB == "" {
		t.Skip("run acceptance/image-hosting/gate.sh for disposable Docker fixtures")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	engine := testImageEngine(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 16)
	snapshot := imageTestPlan(t).Snapshot
	snapshot.ImageReference = tag
	snapshot.ContainerPort, snapshot.HostPort = 8080, port
	snapshot.Readiness = domain.HostingReadiness{Path: "/", TimeoutSeconds: 3}
	snapshot.BindingID = "binding_docker_" + suffix
	containers := []string{}
	t.Cleanup(func() {
		for _, id := range containers {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
			container, inspectErr := engine.inspectContainer(cleanupCtx, id)
			if inspectErr == nil && container.Config.Labels[imageBindingLabel] == snapshot.BindingID {
				if _, removeErr := engine.docker(cleanupCtx, "container", "rm", "--force", id); removeErr != nil {
					t.Error(removeErr)
				}
			}
			stop()
		}
	})
	assertHTTP := func(want string) {
		t.Helper()
		client := &http.Client{Timeout: time.Second}
		response, requestErr := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != 200 || strings.TrimSpace(string(body)) != want {
			t.Fatalf("ordinary HTTP want=%s got=%q status=%d err=%v", want, body, response.StatusCode, readErr)
		}
	}
	releaseA, err := engine.Prepare(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// Change the registry tag after preparation. Application must use the
	// recorded digest and image ID, never resolve the moving tag again.
	for _, args := range [][]string{{"tag", versionB, tag}, {"push", tag}} {
		if output, commandErr := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); commandErr != nil {
			t.Fatalf("fixture tag movement: %v %s", commandErr, output)
		}
	}
	planA, err := domain.CompileHostingPlan(snapshot, releaseA, nil)
	if err != nil {
		t.Fatal(err)
	}
	resultA := engine.Apply(ctx, "hop_a_"+suffix, planA)
	containers = append(containers, resultA.ContainerID)
	if resultA.Outcome != "succeeded" || !resultA.Ready || resultA.Uncertain {
		t.Fatalf("A apply: %+v", resultA)
	}
	assertHTTP("A")
	healthyA := &domain.HostingHealthy{Snapshot: snapshot, OperationID: "hop_a_" + suffix, ContainerID: resultA.ContainerID, Release: releaseA, PlanHash: planA.Hash}
	releaseB, err := engine.Prepare(ctx, snapshot)
	if err != nil || releaseA.CanonicalImage == releaseB.CanonicalImage || releaseA.ImageID == releaseB.ImageID {
		t.Fatalf("moving tag did not become distinct release: %v", err)
	}
	planB, err := domain.CompileHostingPlan(snapshot, releaseB, healthyA)
	if err != nil {
		t.Fatal(err)
	}
	resultB := engine.Apply(ctx, "hop_b_"+suffix, planB)
	containers = append(containers, resultB.ContainerID)
	if resultB.Outcome != "succeeded" || !resultB.Ready {
		t.Fatalf("B apply: %+v", resultB)
	}
	assertHTTP("B")
	failedPull := snapshot
	failedPull.ImageReference = tag + "-missing"
	if _, err = engine.Prepare(ctx, failedPull); err == nil {
		t.Fatal("missing image unexpectedly pulled")
	}
	assertHTTP("B")
	healthyB := &domain.HostingHealthy{Snapshot: snapshot, OperationID: "hop_b_" + suffix, ContainerID: resultB.ContainerID, Release: releaseB, PlanHash: planB.Hash}
	unready := snapshot
	unready.Readiness = domain.HostingReadiness{Path: "/unready", TimeoutSeconds: 1}
	failedPlan, err := domain.CompileHostingPlan(unready, releaseA, healthyB)
	if err != nil {
		t.Fatal(err)
	}
	failedResult := engine.Apply(ctx, "hop_unready_"+suffix, failedPlan)
	if failedResult.Outcome != "failed" || !failedResult.Restored || failedResult.Uncertain || failedResult.PreviousContainerID != resultB.ContainerID {
		t.Fatalf("post-apply HTTP failure did not restore B safely: %+v", failedResult)
	}
	assertHTTP("B")
	t.Logf("real Docker: A frozen at %s after tag moved; B %s; ordinary HTTP A/B; pull failure preserved B; HTTP readiness failure restored B", releaseA.CanonicalImage, releaseB.CanonicalImage)
}
