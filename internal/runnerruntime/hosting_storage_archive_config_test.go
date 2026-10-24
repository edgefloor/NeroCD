package runnerruntime

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestStorageArchiveConfigIsExplicitRedactedAndCannotCompleteGenerically(t *testing.T) {
	p := archiveRuntimePlan(t)
	c := StorageArchiveExecutionConfig{ArtifactRoot: "/srv/private_canary_artifacts", TemporaryRoot: "/srv/private_canary_temporary", DockerBinary: "/usr/private_canary_docker", Helper: p.Helper}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if strings.Contains(fmt.Sprintf(verb, c), "private_canary") {
			t.Fatal("protected config formatted")
		}
	}
	if _, err := json.Marshal(c); err == nil {
		t.Fatal("protected config serialized")
	}
	for _, config := range []Config{{StorageArchive: &c, Server: "https://controller", Execute: false}, {StorageArchive: &c, Server: "https://controller", Execute: true, CompleteStatus: "succeeded"}, {StorageArchive: &c, Server: "http://controller", Execute: true}} {
		if validateStorageArchiveExecutionConfig(config) == nil {
			t.Fatal("disabled/insecure/generic archive config accepted")
		}
	}
	if err := validateStorageArchiveExecutionConfig(Config{StorageArchive: &c, Server: "https://controller", Execute: true}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []StorageArchiveExecutionConfig{
		{ArtifactRoot: c.ArtifactRoot, TemporaryRoot: c.ArtifactRoot, DockerBinary: c.DockerBinary, Helper: c.Helper},
		{ArtifactRoot: c.ArtifactRoot, TemporaryRoot: c.ArtifactRoot + "/child", DockerBinary: c.DockerBinary, Helper: c.Helper},
		{ArtifactRoot: "relative", TemporaryRoot: c.TemporaryRoot, DockerBinary: c.DockerBinary, Helper: c.Helper},
		{ArtifactRoot: c.ArtifactRoot, TemporaryRoot: c.TemporaryRoot, DockerBinary: c.DockerBinary},
	} {
		if bad.validate() == nil {
			t.Fatal("missing or aliased archive identity accepted")
		}
	}
}
