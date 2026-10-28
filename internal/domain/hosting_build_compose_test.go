package domain_test

import (
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func composeResultFixture(t *testing.T) (domain.HostingBuild, domain.HostingBuildComposeResult) {
	t.Helper()
	settings := domain.HostingBuildSettings{Strategy: "compose", ComposeFile: "deploy/compose.yaml", PublicComponent: "web", ContainerPort: 8080}
	source := domain.HostingStackSource{PublicComponent: "web", ContainerPort: 8080, Original: "services:\n  web:\n    image: local-app:v1\n    build:\n      context: ../app\n      dockerfile: Dockerfile\n      target: release\n  worker:\n    image: local-app:v1\n  db:\n    image: postgres:17\n    volumes: [data:/var/lib/postgresql/data]\nvolumes:\n  data: {}\n"}
	definition, err := domain.ParseHostingGitCompose(source, settings)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	build := domain.HostingBuild{ID: "parent", DaemonID: "daemon", Snapshot: domain.HostingBuildSnapshot{Source: domain.HostingGitSource{ID: "source", Settings: settings}, InputID: digest, Policy: domain.HostingBuildPolicy{GitCompose: true, Platform: "linux/amd64"}}, SourceReceipt: &domain.HostingBuildSourceReceipt{BuildID: "parent", SourceID: "source", InputID: digest, Commit: strings.Repeat("a", 40), TreeDigest: digest, PolicyHash: digest}}
	artifact := domain.HostingBuildArtifact{Origin: "local_build", BuildID: domain.HostingComposeBuildID(build.ID, "web"), SourceID: "source", InputID: digest, SourceReceiptHash: domain.HostingDocumentHash(build.SourceReceipt), ContextDigest: digest, RequestDigest: digest, DaemonID: "daemon", ImageID: digest, IndexDigest: digest, ManifestDigest: digest, ConfigDigest: digest, Platform: "linux/amd64"}
	result := domain.HostingBuildComposeResult{SourceReceiptHash: domain.HostingDocumentHash(build.SourceReceipt), Source: source, Definition: definition, Services: []domain.HostingBuildComposeService{{Component: "web", Artifact: artifact}}}
	result.Hash = domain.HostingDocumentHash(result)
	return build, result
}

func TestHostingGitComposeParsingAndPastedIsolation(t *testing.T) {
	build, result := composeResultFixture(t)
	if domain.ValidateHostingBuildComposeResult(result, build) != nil {
		t.Fatal("valid mixed Compose result rejected")
	}
	if _, err := domain.ParseHostingStack(result.Source); err == nil {
		t.Fatal("pasted Compose accepted build fields")
	}
	owner, err := domain.HostingStackBuildOwner(result.Definition, "worker")
	if err != nil || owner != "web" {
		t.Fatal("shared image alias did not select built owner", err)
	}
	if _, ok := domain.HostingComposeBuildArtifact(result, "db"); ok {
		t.Fatal("pulled image became a local artifact")
	}
	if a, ok := domain.HostingComposeBuildArtifact(result, "worker"); !ok || a.BuildID != result.Services[0].Artifact.BuildID {
		t.Fatal("alias artifact changed")
	}
	for _, service := range result.Definition.Services {
		if service.Name == "web" && (service.Build == nil || service.Build.Context != "app" || service.Build.Dockerfile != "Dockerfile") {
			t.Fatal("build paths did not resolve relative to Compose file")
		}
	}
	for _, context := range []string{"../../escape", "https://example.com/context", "/tmp/app", "$CONTEXT"} {
		bad := result.Source
		bad.Original = strings.Replace(bad.Original, "../app", context, 1)
		if _, err = domain.ParseHostingGitCompose(bad, build.Snapshot.Source.Settings); err == nil {
			t.Fatalf("unsafe context accepted: %s", context)
		}
	}
	for _, extra := range []string{"      secrets: [key]\n"} {
		bad := result.Source
		bad.Original = strings.Replace(bad.Original, "      target: release\n", "      target: release\n"+extra, 1)
		if _, err = domain.ParseHostingGitCompose(bad, build.Snapshot.Source.Settings); err == nil {
			t.Fatal("unsupported build inputs accepted")
		}
	}
	bad := result.Source
	bad.Original = strings.Replace(bad.Original, "  worker:\n    image: local-app:v1", "  worker:\n    image: local-app:v1\n    build: ../app", 1)
	if _, err = domain.ParseHostingGitCompose(bad, build.Snapshot.Source.Settings); err == nil {
		t.Fatal("ambiguous shared image build owners accepted")
	}
}

func TestHostingGitComposeResultBinding(t *testing.T) {
	for _, field := range []string{"source", "definition", "coverage", "daemon", "child", "receipt", "hash"} {
		t.Run(field, func(t *testing.T) {
			build, result := composeResultFixture(t)
			switch field {
			case "source":
				result.Source.Original += "\n"
			case "definition":
				result.Definition.PublicComponent = "db"
			case "coverage":
				result.Services = nil
			case "daemon":
				result.Services[0].Artifact.DaemonID = "other"
			case "child":
				result.Services[0].Artifact.BuildID = "parent"
			case "receipt":
				result.SourceReceiptHash = "sha256:" + strings.Repeat("b", 64)
			case "hash":
				result.Hash = "changed"
			}
			if field != "hash" {
				result.Hash = ""
				result.Hash = domain.HostingDocumentHash(result)
			}
			if domain.ValidateHostingBuildComposeResult(result, build) == nil {
				t.Fatal("changed composed result accepted")
			}
		})
	}
}

func TestHostingGitComposeImageOnlyResultHasEmptyArtifactArray(t *testing.T) {
	build, result := composeResultFixture(t)
	result.Source.Original = "services:\n  web:\n    image: nginx:alpine\n"
	var err error
	result.Definition, err = domain.ParseHostingGitCompose(result.Source, build.Snapshot.Source.Settings)
	if err != nil {
		t.Fatal(err)
	}
	result.Services = []domain.HostingBuildComposeService{}
	result.Hash = ""
	result.Hash = domain.HostingDocumentHash(result)
	if domain.ValidateHostingBuildComposeResult(result, build) != nil {
		t.Fatal("image-only prepared Git stack rejected")
	}
	result.Services = nil
	result.Hash = ""
	result.Hash = domain.HostingDocumentHash(result)
	if domain.ValidateHostingBuildComposeResult(result, build) == nil {
		t.Fatal("null artifact array accepted")
	}
}
