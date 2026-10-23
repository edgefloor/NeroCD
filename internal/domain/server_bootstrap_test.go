package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func bootstrapTestOperation() ServerBootstrapOperation {
	return ServerBootstrapOperation{IDs: ServerBootstrapIDs{InstallationID: strings.Repeat("a", 32), InstallOperationID: strings.Repeat("b", 32), RunnerID: "runner_test", CredentialReferenceID: "private_reference"}, Input: ServerBootstrapInput{ProjectID: "project", Name: "server", Target: ServerBootstrapTarget{IP: "192.0.2.1", Port: 22, User: "root", HostFingerprint: "SHA256:" + strings.Repeat("A", 43)}, ControllerURL: "https://controller.example/", Artifact: ServerBootstrapArtifact{Size: 1, SHA256: strings.Repeat("a", 64), Version: "v1", Architecture: "amd64"}, SSHSecretID: "key", SSHSecretVersion: 1}}
}
func bootstrapTestReview(op ServerBootstrapOperation) ServerBootstrapReview {
	raw, _ := json.Marshal(map[string]any{"version": 1, "installation_id": op.IDs.InstallationID, "install_operation_id": op.IDs.InstallOperationID, "runner_id": op.IDs.RunnerID, "credential_reference_id": op.IDs.CredentialReferenceID, "controller_url": op.Input.ControllerURL, "artifact": op.Input.Artifact, "capability_preset": ServerBootstrapPreset})
	h := sha256.Sum256(raw)
	now := time.Now()
	return ServerBootstrapReview{Prerequisites: ServerBootstrapPrerequisites{Profile: "prepared_debian12_v1", OSState: "available", OSID: "debian", OSVersion: "12", Architecture: "amd64", SystemdState: "available", SystemdStatus: "running", DockerState: "available", DockerAPIVersion: "1.54", ComposeState: "available", ComposeVersion: "5.1.1", DiskState: "available", DiskTotalBytes: 2 << 30, DiskAvailableBytes: 1 << 30}, Manifest: raw, ManifestSHA256: hex.EncodeToString(h[:]), ProbeObservedAt: now, ProbeExpiresAt: now.Add(30 * time.Second), TargetHash: HostingDocumentHash(op.Input.Target)}
}
func TestServerBootstrapReviewPinsExactIdentityAndFreshProbe(t *testing.T) {
	op := bootstrapTestOperation()
	if err := op.Input.Validate(); err != nil {
		t.Fatal(err)
	}
	r := bootstrapTestReview(op)
	if err := r.Validate(op, time.Now()); err != nil {
		t.Fatal(err)
	}
	other := op
	other.IDs.RunnerID = "runner_other"
	if r.Validate(other, time.Now()) == nil {
		t.Fatal("runner substitution")
	}
	if r.Validate(op, r.ProbeExpiresAt) == nil {
		t.Fatal("stale probe")
	}
	r.Manifest = append(r.Manifest, ' ')
	if r.Validate(op, time.Now()) == nil {
		t.Fatal("byte identity changed")
	}
	r = bootstrapTestReview(op)
	r.Manifest = []byte(strings.Replace(string(r.Manifest), `"version":1`, `"version":1,"version":1`, 1))
	h := sha256.Sum256(r.Manifest)
	r.ManifestSHA256 = hex.EncodeToString(h[:])
	if r.Validate(op, time.Now()) == nil {
		t.Fatal("duplicate field")
	}
}
func TestServerBootstrapTransitionTableRetainsRemoteHold(t *testing.T) {
	for _, state := range []string{"draft", "reviewed", "running", "installed_waiting_enrollment", "registered", "completed", "uncertain"} {
		if ServerBootstrapActionAllowed("restart_owned", state, false) || ServerBootstrapActionAllowed("cleanup_owned", state, false) {
			t.Fatal("unintegrated mutation")
		}
		if ServerBootstrapActionAllowed("install", state, true) || ServerBootstrapActionAllowed("cancel", state, true) {
			t.Fatal("hold bypass")
		}
	}
	if !ServerBootstrapActionAllowed("grant_destination", "registered", true) || ServerBootstrapActionAllowed("grant_destination", "installed_waiting_enrollment", true) {
		t.Fatal("grant conflates registration")
	}
}

func TestServerBootstrapPreparedProfileRejectsUnqualifiedFacts(t *testing.T) {
	op := bootstrapTestOperation()
	r := bootstrapTestReview(op)
	for _, change := range []func(*ServerBootstrapPrerequisites){func(p *ServerBootstrapPrerequisites) { p.OSID = "ubuntu" }, func(p *ServerBootstrapPrerequisites) { p.OSVersion = "13" }, func(p *ServerBootstrapPrerequisites) { p.Architecture = "arm64" }, func(p *ServerBootstrapPrerequisites) { p.SystemdStatus = "degraded" }, func(p *ServerBootstrapPrerequisites) { p.DockerAPIVersion = "1.53" }, func(p *ServerBootstrapPrerequisites) { p.ComposeState = "missing" }, func(p *ServerBootstrapPrerequisites) { p.ComposeVersion = "5.2.0" }, func(p *ServerBootstrapPrerequisites) { p.DiskAvailableBytes = 1 << 29 }} {
		other := r
		change(&other.Prerequisites)
		if other.Validate(op, time.Now()) == nil {
			t.Fatal("unqualified profile accepted")
		}
	}
}

func TestServerBootstrapLauncherCapabilityParity(t *testing.T) {
	want := []string{"compose-deploy", "image_deploy", "image_release_ports_v1", "hosting_readiness_v1"}
	got := ServerBootstrapCapabilities()
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatal(got)
		}
	}
	got[0] = "shell"
	if ServerBootstrapCapabilities()[0] != want[0] {
		t.Fatal("shared capabilities")
	}
}

func TestServerBootstrapInputRejectsEndpointAliases(t *testing.T) {
	for _, endpoint := range []string{"http://controller.example/", "https://user@controller.example/", "https://controller.example/path", "https://controller.example/?", "https://controller.example/#", "https://controller.example:99999/", "https://controller.example/%2F"} {
		op := bootstrapTestOperation()
		op.Input.ControllerURL = endpoint
		if op.Input.Validate() == nil {
			t.Fatal("controller URL accepted", endpoint)
		}
	}
	for _, ip := range []string{"controller.example", "255.255.255.255", "::ffff:192.0.2.1", "fe80::1%en0", "0.0.0.0"} {
		op := bootstrapTestOperation()
		op.Input.Target.IP = ip
		if op.Input.Validate() == nil {
			t.Fatal("target alias accepted", ip)
		}
	}
}
