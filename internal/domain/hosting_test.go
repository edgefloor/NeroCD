package domain

import (
	"strings"
	"testing"
)

func TestValidateImageReference(t *testing.T) {
	for _, value := range []string{"nginx:1.27", "library/nginx:latest", "ghcr.io/acme/web:v1", "localhost:5000/acme/web:v1", "registry.example:443/team/web@sha256:" + strings.Repeat("a", 64), "nginx:stable@sha256:" + strings.Repeat("f", 64), "[::1]:5000/web:tag", "registry.example/team/a__b-c.d:V1"} {
		t.Run(value, func(t *testing.T) {
			if err := ValidateImageReference(value); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, value := range []string{"", "nginx", "host:5000/web", "nginx:", "nginx:-bad", "https://registry/web:v1", "user:pass@registry/web:v1", "sha256:" + strings.Repeat("a", 64), "web@sha256:" + strings.Repeat("a", 63), "web@sha512:" + strings.Repeat("a", 64), "web@sha256:" + strings.Repeat("A", 64), "/web:v1", "web/:v1", "a//b:v1", "UpperCase:v1", "web@sha256:" + strings.Repeat("a", 64) + "@x", "host:0/web:v1", "host:65536/web:v1", "host:port/web:v1", "host:+5000/web:v1", "bad..host/web:v1", "bad!/web:v1", "web:v1 x", " web:v1", "web:v1\n", "web:v1?x", "a..b:v1", "a___b:v1", "a.-b:v1"} {
		t.Run(value, func(t *testing.T) {
			if err := ValidateImageReference(value); err == nil {
				t.Fatal("invalid image accepted")
			}
		})
	}
}
