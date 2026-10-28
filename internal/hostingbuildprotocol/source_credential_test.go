package hostingbuildprotocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestSourceCredentialWireStrictAndRedacted(t *testing.T) {
	a := domain.HostingBuildAuthority{BuildID: "test", RunID: "build_run_test", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	in := domain.HostingSourceCredentialRequest{Authority: a, DeliveryID: strings.Repeat("a", 32)}
	c := SourceCredential{Authority: a, DeliveryID: in.DeliveryID, RequestHash: SourceCredentialRequestHash("runner", in), Kind: "https_basic", Username: "private-user", Bytes: []byte("private-password"), UseUntil: time.Now().Add(time.Minute)}
	var buffer bytes.Buffer
	if err := WriteSourceCredential(&buffer, c); err != nil {
		t.Fatal(err)
	}
	raw := buffer.String()
	out, err := ReadSourceCredential(strings.NewReader(raw))
	if err != nil || !bytes.Equal(out.Bytes, c.Bytes) {
		t.Fatal("round trip", err)
	}
	owned := out.Bytes
	out.Close()
	if bytes.Count(owned, []byte{0}) != len(owned) {
		t.Fatal("Close did not clear owned bytes")
	}
	encoded, _ := json.Marshal(c)
	if string(encoded) != "{}" || strings.Contains(fmt.Sprintf("%+v %#v %s", c, c, c), "private") {
		t.Fatal("ordinary serialization leaked")
	}
	for name, bad := range map[string]string{
		"duplicate":          strings.Replace(raw, `"username":`, `"username":"other","username":`, 1),
		"nested duplicate":   strings.Replace(raw, `"attempt":1`, `"attempt":1,"attempt":1`, 1),
		"casing":             strings.Replace(raw, `"username":`, `"Username":`, 1),
		"unknown":            strings.Replace(raw, `"username":`, `"secret":"private","username":`, 1),
		"unpaired surrogate": strings.Replace(raw, `"username":"private-user"`, `"username":"\ud800"`, 1),
		"trailing":           raw + ` {}`,
		"bad base64":         strings.Replace(raw, `"credential":"cHJpdmF0ZS1wYXNzd29yZA=="`, `"credential":"!"`, 1),
		"array bytes":        strings.Replace(raw, `"credential":"cHJpdmF0ZS1wYXNzd29yZA=="`, `"credential":[65]`, 1),
		"oversize":           strings.Repeat(" ", MaxSourceCredentialWireBytes) + raw,
	} {
		t.Run(name, func(t *testing.T) {
			v, e := ReadSourceCredential(strings.NewReader(bad))
			if e == nil || len(v.Bytes) != 0 || strings.Contains(e.Error(), "private") {
				t.Fatal("malformed secret accepted or disclosed")
			}
		})
	}
}

func TestBasicCredentialCombinedBudget(t *testing.T) {
	if !ValidBasicCredential("u", bytes.Repeat([]byte("p"), 4094)) || ValidBasicCredential("u", bytes.Repeat([]byte("p"), 4095)) {
		t.Fatal("combined budget")
	}
	for _, u := range []string{"", strings.Repeat("u", 257), "u:p", "u\n", string([]byte{255})} {
		if ValidBasicCredential(u, []byte("p")) {
			t.Fatal("invalid username accepted")
		}
	}
	for _, p := range [][]byte{nil, []byte("p\r"), {255}} {
		if ValidBasicCredential("u", p) {
			t.Fatal("invalid password accepted")
		}
	}
}

func TestSSHSourceCredentialWireBudgetAndKind(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)
	raw = append(raw, bytes.Repeat([]byte(" "), 65536-len(raw))...)
	defer clear(raw)
	c := SourceCredential{Authority: domain.HostingBuildAuthority{BuildID: "test", RunID: "build_run_test", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), RequestHash: "sha256:" + strings.Repeat("b", 64), Kind: "ssh_private_key", Bytes: raw, UseUntil: time.Now().Add(time.Minute)}
	var wire bytes.Buffer
	if err = WriteSourceCredential(&wire, c); err != nil {
		t.Fatal(err)
	}
	if wire.Len() <= 16<<10 || wire.Len() > MaxSourceCredentialWireBytes {
		t.Fatal("maximum key did not exercise bounded wire", wire.Len())
	}
	out, err := ReadSourceCredential(&wire)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if out.Kind != c.Kind || out.Username != "" || !bytes.Equal(out.Bytes, raw) {
		t.Fatal("SSH roundtrip changed material")
	}
	for _, mutate := range []func(*SourceCredential){func(v *SourceCredential) { v.Username = "git" }, func(v *SourceCredential) { v.Kind = "https_basic" }, func(v *SourceCredential) { v.Bytes = append(bytes.Clone(raw), ' ') }, func(v *SourceCredential) { v.Bytes = []byte("invalid key") }} {
		invalid := c
		mutate(&invalid)
		if WriteSourceCredential(io.Discard, invalid) == nil {
			t.Fatal("invalid SSH material accepted")
		}
	}
}
