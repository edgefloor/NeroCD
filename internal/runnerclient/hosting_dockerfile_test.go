package runnerclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

func TestStoredDockerfileMaximumRunnerSnapshotDecoder(t *testing.T) {
	source := domain.HostingGitSource{Kind: "dockerfile", ID: "source", ResourceID: "resource", ProjectID: "project", Generation: 1, Content: strings.Repeat("\x01", domain.HostingDockerfileContentLimit), Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}
	source.ContentHash = domain.HostingDockerfileContentHash(source.Content)
	for i := range 64 {
		source.Settings.BuildVariables = append(source.Settings.BuildVariables, domain.HostingBuildVariable{Name: fmt.Sprintf("VAR_%d", i), Value: strings.Repeat("\x01", 4096)})
	}
	want := hostingbuildprotocol.StatusResponse{Build: domain.HostingBuild{ID: "build", State: "queued", Snapshot: domain.HostingBuildSnapshot{Source: source}}}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 1<<20 || len(raw) > maxBuildResponseBytes {
		t.Fatal("fixture does not prove expanded bounded response", len(raw))
	}
	var response buildResponse[hostingbuildprotocol.StatusResponse]
	if err = response.decodeResponse(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if response.value.Build.Snapshot.Source.Content != source.Content {
		t.Fatal("source changed in transfer")
	}
}
