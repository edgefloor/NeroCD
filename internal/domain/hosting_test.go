package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateImageReference(t *testing.T) {
	for _, value := range []string{"nginx:1.27", "library/nginx:latest", "ghcr.io/acme/web:v1", "localhost:5000/acme/web:v1", "registry.example:443/team/web@sha256:" + strings.Repeat("a", 64), "nginx:stable@sha256:" + strings.Repeat("f", 64), "[::1]:5000/web:tag",
		"[2001:db8::1]/web:v1",
		"[2001:db8::1]/web@sha256:" + strings.Repeat("a", 64),
		"[2001:db8::1]:5000/web@sha256:" + strings.Repeat("a", 64), "registry.example/team/a__b-c.d:V1"} {
		t.Run(value, func(t *testing.T) {
			if err := ValidateImageReference(value); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, value := range []string{"", "nginx", "host:5000/web", "nginx:", "nginx:-bad", "https://registry/web:v1", "user:pass@registry/web:v1", "sha256:" + strings.Repeat("a", 64), "web@sha256:" + strings.Repeat("a", 63), "web@sha512:" + strings.Repeat("a", 64), "web@sha256:" + strings.Repeat("A", 64), "/web:v1", "web/:v1", "a//b:v1", "UpperCase:v1", "web@sha256:" + strings.Repeat("a", 64) + "@x", "host:0/web:v1", "host:65536/web:v1", "host:port/web:v1",
		"[registry.example]:5000/web:v1", "[registry.example]/web:v1", "[127.0.0.1]:5000/web:v1",
		"[fe80::1%eth0]:5000/web:v1", "[::ffff:192.0.2.1]/web:v1", "[2001:db8::1/web:v1",
		"[2001:db8::1]garbage/web:v1", "[2001:db8::1]:/web:v1", "[2001:db8::1]:0/web:v1",
		"[2001:db8::1]:65536/web:v1", "[2001:db8::1]:+5000/web:v1", "2001:db8::1/web:v1", "host:+5000/web:v1", "bad..host/web:v1", "bad!/web:v1", "web:v1 x", " web:v1", "web:v1\n", "web:v1?x", "a..b:v1", "a___b:v1", "a.-b:v1"} {
		t.Run(value, func(t *testing.T) {
			if err := ValidateImageReference(value); err == nil {
				t.Fatal("invalid image accepted")
			}
		})
	}
}

func TestResourceSourceInputExclusiveVariants(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"kind":"git"}`, `{"kind":"git","git":null}`, `{"kind":"image","image":null}`, `{"kind":"image","image":{"reference":"nginx:1"},"git":{}}`, `{"kind":"git","git":{"source_id":"caller"}}`, `{"kind":"git","git":{"policy":{}}}`, `{"kind":"git","git":{"settings":{"unknown":true}}}`, `{"kind":"image","image":{"reference":"nginx:1","unknown":true}}`} {
		var input ResourceSourceInput
		if json.Unmarshal([]byte(raw), &input) == nil {
			t.Fatal("accepted ambiguous source", raw)
		}
	}
	var input ResourceSourceInput
	if err := json.Unmarshal([]byte(`{"kind":"git","git":{"repository_url":"https://github.com/acme/web","requested_ref":"main","credential_kind":"public","settings":{"strategy":"dockerfile"}}}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.Source().Git.SourceID != "" || input.Source().Image.Reference != "" {
		t.Fatal("manufactured source identity")
	}
	wire, err := json.Marshal(ResourceSource{Kind: "image", Image: ImageSource{Reference: "nginx:1"}})
	if err != nil || string(wire) != `{"kind":"image","image":{"reference":"nginx:1"}}` {
		t.Fatal(string(wire), err)
	}
}
