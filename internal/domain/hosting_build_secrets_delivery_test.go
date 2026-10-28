package domain_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"nerocd/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestBuildSecretsProtectedCodec(t *testing.T) {
	d := &domain.HostingBuildSecretsDelivery{Secrets: []domain.HostingBuildSecretValue{{Name: "TOKEN", Value: strings.Repeat("x", 65536)}, {Name: "EMPTY", Value: ""}, {Name: "MULTI", Value: "line\nsecond\r\nend"}}, UseUntil: time.Now().Add(time.Minute)}
	raw, err := json.Marshal(d)
	if err != nil || string(raw) != "{}" || strings.Contains(fmt.Sprintf("%+v", d), "second") {
		t.Fatal("accidental protected serialization")
	}
	var out bytes.Buffer
	if err = domain.WriteHostingBuildSecretsDelivery(&out, d); err != nil {
		t.Fatal(err)
	}
	decoded, err := domain.ReadHostingBuildSecretsDelivery(&out)
	if err != nil || len(decoded.Secrets) != 3 || decoded.Secrets[2].Value != d.Secrets[2].Value || decoded.Secrets[0].Value != d.Secrets[0].Value {
		t.Fatal("exact protected delivery failed", err)
	}
	decoded.Close()
	if len(decoded.Secrets) != 0 {
		t.Fatal("protected close retained references")
	}
	for _, body := range []string{`null`, `{}`, `{"secrets":[{"name":"TOKEN"}],"use_until":"2030-01-01T00:00:00Z"}`, `{"secrets":[{"name":"TOKEN","value":"a\u0000b"}],"use_until":"2030-01-01T00:00:00Z"}`, `{"secrets":[{"name":"TOKEN","value":"one","value":"two"}],"use_until":"2030-01-01T00:00:00Z"}`, `{"secrets":[{"name":"TOKEN","value":"one"},{"name":"TOKEN","value":"two"}],"use_until":"2030-01-01T00:00:00Z"}`, `{"secrets":[{"name":"TOKEN","value":"one","extra":true}],"use_until":"2030-01-01T00:00:00Z"}`} {
		if got, err := domain.ReadHostingBuildSecretsDelivery(strings.NewReader(body)); err == nil {
			got.Close()
			t.Fatal("invalid delivery accepted")
		}
	}
}
