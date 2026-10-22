package hostingbuildprotocol

import (
	"bytes"
	"encoding/json"
	"fmt"
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
