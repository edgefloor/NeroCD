package runnerruntime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/parser"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
)

const privateDockerfileMultiline = "first line\nsecond line"

// The first stage's served bytes and final stage's labels require independent
// ARG visibility. Values stay in build settings rather than generated source.
func privateDockerfileArgumentSource(declared, fail bool) string {
	declarations := ""
	if declared {
		declarations = "ARG BUILD_MARKER\nARG MULTILINE\nARG EMPTY=not-empty\n"
	}
	source := "FROM " + hostingbuild.StaticImage + " AS prepared\n" + declarations +
		"RUN test -z \"$EMPTY\" && printf '%s\\n%s\\n%s' \"$BUILD_MARKER\" \"$MULTILINE\" \"$EMPTY\" > /tmp/index.html\n" +
		"FROM " + hostingbuild.StaticImage + "\n" + declarations +
		"COPY --from=prepared --chmod=0644 /tmp/index.html /usr/share/nginx/html/index.html\n" +
		"LABEL nerocd.fixture.marker=\"${BUILD_MARKER}\" nerocd.fixture.multiline=\"${MULTILINE}\" nerocd.fixture.empty=\"${EMPTY}\"\n"
	if fail {
		source += "RUN exit 9\n"
	}
	return source
}

func privateDockerfileArgumentSettings(marker string, inject *bool) domain.HostingBuildSettings {
	return domain.HostingBuildSettings{Strategy: "dockerfile", BuildVariables: []domain.HostingBuildVariable{{Name: "BUILD_MARKER", Value: marker}, {Name: "MULTILINE", Value: privateDockerfileMultiline}, {Name: "EMPTY", Value: ""}}, InjectBuildArgsToDockerfile: inject}
}

func privateDockerfileArgumentBody(marker string) string {
	return marker + "\n" + privateDockerfileMultiline + "\n"
}

// Stored sources have no repository or extra context files. Both stages use the
// existing pinned image and create the complete served artifact from the content.
func storedDockerfileFixtureSource(marker string, fail bool) string {
	source := "FROM " + hostingbuild.StaticImage + " AS prepared\n" +
		"RUN printf '%s\\n' '" + marker + "' > /tmp/index.html\n" +
		"FROM " + hostingbuild.StaticImage + "\n" +
		"COPY --from=prepared --chmod=0644 /tmp/index.html /usr/share/nginx/html/index.html\n"
	if fail {
		source += "RUN exit 9\n"
	}
	return source
}

func TestPrivateDockerfileArgumentFixtureContracts(t *testing.T) {
	disabled := false
	for _, fixture := range []struct {
		name, marker string
		declared     bool
		inject       *bool
	}{{"default-A", "local-A", false, nil}, {"edited-B", "local-B", false, nil}, {"disabled-declared", "local-B", true, &disabled}} {
		t.Run(fixture.name, func(t *testing.T) {
			settings := privateDockerfileArgumentSettings(fixture.marker, fixture.inject)
			if err := domain.ValidateHostingBuildSettings(settings); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{"source": map[string]any{"kind": "git", "git": map[string]any{"repository_url": "https://example.com:443/repo.git", "requested_ref": "main", "credential_kind": "https_basic", "settings": settings}}})
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Source domain.ResourceSourceInput `json:"source"`
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&request); err != nil {
				t.Fatal("production source decoder", err)
			}
			got := request.Source.Git.Settings
			if got.BuildVariables[1].Value != privateDockerfileMultiline || got.BuildVariables[2].Value != "" || (got.InjectBuildArgsToDockerfile == nil) != (fixture.inject == nil) || fixture.inject != nil && *got.InjectBuildArgsToDockerfile != *fixture.inject {
				t.Fatal("source decoder changed literal values or injection choice")
			}
			source := privateDockerfileArgumentSource(fixture.declared, false)
			if strings.Count(source, "FROM ") != 2 || strings.Count(source, "ARG BUILD_MARKER") != map[bool]int{false: 0, true: 2}[fixture.declared] || strings.Contains(source, fixture.marker) || strings.Contains(source, privateDockerfileMultiline) {
				t.Fatal("fixture does not require per-stage injection or leaked configured values into source")
			}
			if !strings.HasSuffix(privateDockerfileArgumentSource(fixture.declared, true), "RUN exit 9\n") {
				t.Fatal("genuine failure command changed")
			}
		})
	}
	if privateDockerfileArgumentBody("local-A") == privateDockerfileArgumentBody("local-B") {
		t.Fatal("edited variable must change served content")
	}
}

func TestStoredDockerfileFixtureContracts(t *testing.T) {
	seen := make(map[string]bool)
	for _, fixture := range []struct {
		marker string
		fail   bool
	}{{"stored-A", false}, {"stored-B", false}, {"stored-failed", true}} {
		t.Run(fixture.marker, func(t *testing.T) {
			content := storedDockerfileFixtureSource(fixture.marker, fixture.fail)
			input := domain.ResourceSourceInput{Kind: "dockerfile", Dockerfile: &domain.DockerfileResourceConfiguration{Content: content, Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}}
			raw, err := json.Marshal(map[string]any{"kind": input.Kind, "dockerfile": input.Dockerfile})
			if err != nil {
				t.Fatal(err)
			}
			var decoded domain.ResourceSourceInput
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&decoded); err != nil {
				t.Fatal(err)
			}
			if err := domain.ValidateResourceSource(decoded.Source()); err != nil {
				t.Fatal(err)
			}
			if decoded.Dockerfile == nil || decoded.Dockerfile.Content != content || decoded.Git != nil {
				t.Fatal("stored source decoder changed content or invented Git configuration")
			}
			digest := domain.HostingDockerfileContentHash(content)
			if seen[digest] {
				t.Fatal("distinct source edits must change frozen content identity")
			}
			seen[digest] = true
			parsed, err := parser.Parse(strings.NewReader(content))
			if err != nil {
				t.Fatal(err)
			}
			stages, copies, failures := 0, 0, 0
			for _, node := range parsed.AST.Children {
				switch strings.ToLower(node.Value) {
				case "from":
					stages++
					if node.Next == nil || node.Next.Value != hostingbuild.StaticImage {
						t.Fatal("fixture changed pinned image")
					}
				case "copy":
					copies++
					if !strings.Contains(node.Original, "--from=prepared") {
						t.Fatal("stored source unexpectedly requires external context")
					}
				case "run":
					if node.Next != nil && node.Next.Value == "exit 9" {
						failures++
					}
				}
			}
			if stages != 2 || copies != 1 || failures != map[bool]int{false: 0, true: 1}[fixture.fail] {
				t.Fatal("fixture lost multistage served content or genuine failure")
			}
		})
	}
}

func TestPrivateSSHFixtureContracts(t *testing.T) {
	fingerprint := "SHA256:" + strings.Repeat("A", 43)
	raw, err := json.Marshal(map[string]any{"source": map[string]any{"kind": "git", "git": map[string]any{"repository_url": "ssh://git@example.com:2222/repo.git", "requested_ref": "refs/heads/main", "credential_kind": "ssh_private_key", "ssh_host_fingerprints": []string{fingerprint}, "settings": map[string]any{"strategy": "dockerfile"}}}})
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Source domain.ResourceSourceInput `json:"source"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		t.Fatal("real SSH source decoder", err)
	}
	g := request.Source.Git
	if g == nil || g.RepositoryURL != "ssh://git@example.com:2222/repo.git" || g.CredentialKind != "ssh_private_key" || len(g.SSHHostFingerprints) != 1 || g.SSHHostFingerprints[0] != fingerprint || g.Settings.Strategy != "dockerfile" {
		t.Fatal("SSH fixture contract changed")
	}
	if domain.HostingSSHGitCapability != "hosting_git_ssh_private_key_v1" {
		t.Fatal("SSH capability contract changed")
	}
	for _, body := range []any{domain.HostingSourceGrantRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, SecretID: "msec_ssh_fixture", SecretVersion: 2, RequestID: strings.Repeat("a", 32)}, domain.HostingBuildRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, GrantID: "hsg_ssh_fixture", IdempotencyKey: "ssh-fixture-build"}} {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private_key") || strings.Contains(string(encoded), "fingerprint") {
			t.Fatal("grant/build request invented key or policy input")
		}
	}
}

// Only fixed provenance markers and the fixture server's public-key events
// identify an intended SSH failure. A generic source_failed status is insufficient.
func privateSSHFailureEvidence(stage, diagnostics, serverLog, wrongFingerprint string) bool {
	marker := func(stage, status string) bool {
		return strings.Contains("\n"+diagnostics, "\nprovenance stage="+stage+" status="+status+"\n")
	}
	if !marker("ssh_keyscan", "start") {
		return false
	}
	switch stage {
	case "wrong-key":
		if !marker("ssh_fingerprint", "matched") || !marker("git_fetch", "start") || marker("git_checkout", "start") || wrongFingerprint == "" {
			return false
		}
		for _, line := range strings.Split(serverLog, "\n") {
			if strings.HasPrefix(line, "Failed publickey for git ") && strings.Contains(line, "ED25519 "+wrongFingerprint) {
				return true
			}
		}
	case "wrong-host":
		return !marker("ssh_fingerprint", "matched") && !marker("git_init", "start") && !marker("git_fetch", "start") && strings.Contains(serverLog, "kex: host key algorithm: ssh-ed25519") && strings.Contains(serverLog, "SSH2_MSG_NEWKEYS sent") && !strings.Contains(serverLog, "Failed publickey") && !strings.Contains(serverLog, "Accepted publickey")
	}
	return false
}

func TestPrivateSSHFailureEvidence(t *testing.T) {
	scan := "provenance stage=ssh_keyscan status=start\n"
	matched := scan + "provenance stage=ssh_fingerprint status=matched\nprovenance stage=git_fetch status=start\n"
	fingerprint := "SHA256:" + strings.Repeat("A", 43)
	auth := "Failed publickey for git from 127.0.0.1 port 1234 ssh2: ED25519 " + fingerprint + "\n"
	host := "debug1: kex: host key algorithm: ssh-ed25519 [preauth]\ndebug1: SSH2_MSG_NEWKEYS sent [preauth]\n"
	for _, tc := range []struct {
		name, stage, diagnostics, server string
		want                             bool
	}{
		{"wrong key", "wrong-key", matched, auth, true},
		{"host mismatch", "wrong-host", scan, host, true},
		{"generic terminal failure", "wrong-key", "source_failed", auth, false},
		{"fetch unavailable", "wrong-key", matched, "connection refused", false},
		{"different key rejected", "wrong-key", matched, strings.ReplaceAll(auth, fingerprint, "SHA256:different"), false},
		{"no host match", "wrong-key", scan, auth, false},
		{"checkout reached", "wrong-key", matched + "provenance stage=git_checkout status=start\n", auth, false},
		{"server unavailable", "wrong-host", scan, "connection refused", false},
		{"key exchange incomplete", "wrong-host", scan, "kex: host key algorithm: ssh-ed25519", false},
		{"matched host", "wrong-host", matched, host, false},
		{"auth attempted", "wrong-host", scan, host + auth, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := privateSSHFailureEvidence(tc.stage, tc.diagnostics, tc.server, fingerprint); got != tc.want {
				t.Fatalf("evidence=%t want %t", got, tc.want)
			}
		})
	}
}
