package serverbootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func testManifest() InstallManifest {
	return InstallManifest{Version: 1, InstallationID: strings.Repeat("a", 32), InstallOperationID: strings.Repeat("b", 32), RunnerID: "fixture-runner", CredentialReferenceID: "credential-reference-1", ControllerURL: "https://127.0.0.1:18443", Artifact: Artifact{Size: 64, SHA256: strings.Repeat("c", 64), Version: "1.0.0-fixture", Architecture: "arm64"}, CapabilityPreset: "hosting_image_v1"}
}

func TestCanonicalManifestV1(t *testing.T) {
	manifest := testManifest()
	data, digest, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"installation_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","install_operation_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","runner_id":"fixture-runner","credential_reference_id":"credential-reference-1","controller_url":"https://127.0.0.1:18443","artifact":{"size":64,"sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","version":"1.0.0-fixture","architecture":"arm64"},"capability_preset":"hosting_image_v1"}`
	if string(data) != want {
		t.Fatalf("v1 canonical bytes changed: %s", data)
	}
	sum := sha256.Sum256([]byte(want))
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatal("canonical hash does not bind exact bytes")
	}
	data[0] = 'x'
	again, _, _ := CanonicalManifest(manifest)
	if string(again) != want {
		t.Fatal("caller mutated canonical state")
	}
}

func TestCanonicalManifestRejectsInvalidMetadata(t *testing.T) {
	cases := map[string]func(*InstallManifest){
		"version":       func(m *InstallManifest) { m.Version = 2 },
		"operation":     func(m *InstallManifest) { m.InstallOperationID = strings.Repeat("A", 32) },
		"installation":  func(m *InstallManifest) { m.InstallationID = "../outside" },
		"capabilities":  func(m *InstallManifest) { m.CapabilityPreset = "build" },
		"http":          func(m *InstallManifest) { m.ControllerURL = "http://127.0.0.1" },
		"userinfo":      func(m *InstallManifest) { m.ControllerURL = "https://secret@localhost" },
		"query":         func(m *InstallManifest) { m.ControllerURL = "https://localhost?secret=x" },
		"path":          func(m *InstallManifest) { m.ControllerURL = "https://localhost/other" },
		"port":          func(m *InstallManifest) { m.ControllerURL = "https://localhost:65536" },
		"zero port":     func(m *InstallManifest) { m.ControllerURL = "https://localhost:0" },
		"size":          func(m *InstallManifest) { m.Artifact.Size = maxArtifactSize + 1 },
		"digest":        func(m *InstallManifest) { m.Artifact.SHA256 = strings.Repeat("D", 64) },
		"architecture":  func(m *InstallManifest) { m.Artifact.Architecture = "386" },
		"version shell": func(m *InstallManifest) { m.Artifact.Version = "$(false)" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := testManifest()
			change(&m)
			if _, _, err := CanonicalManifest(m); err != InvalidManifest {
				t.Fatalf("got %v", err)
			}
		})
	}
}
