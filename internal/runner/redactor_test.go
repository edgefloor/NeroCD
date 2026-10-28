package runner

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestRedactorRemovesRawConfiguredEncodingsAndOverlaps(t *testing.T) {
	value := "split-secret-42"
	encoded := []string{
		base64.StdEncoding.EncodeToString([]byte(value)),
		base64.URLEncoding.EncodeToString([]byte(value)),
		base64.RawURLEncoding.EncodeToString([]byte(value)),
		hex.EncodeToString([]byte(value)),
		strings.ToUpper(hex.EncodeToString([]byte(value))),
	}
	redactor := NewRedactor([]SecretMaterial{
		{Value: value, Encodings: []string{"base64", "base64url", "hex"}},
		{Value: "split-secret", Encodings: nil},
	})
	input := "before " + value + " " + strings.Join(encoded, " ") + " after"
	output := redactor.Redact(input)
	for _, forbidden := range append([]string{value}, encoded...) {
		if strings.Contains(output, forbidden) {
			t.Fatalf("redacted output contains %q: %q", forbidden, output)
		}
	}
	if strings.Count(output, RedactionMarker) < 5 {
		t.Fatalf("redacted output=%q", output)
	}
}

func TestRedactorCatchesValuesSplitAcrossChunksBeforePersistence(t *testing.T) {
	value := "chunk-boundary-secret"
	encoded := base64.StdEncoding.EncodeToString([]byte(value))
	redactor := NewRedactor([]SecretMaterial{{Value: value, Encodings: []string{"base64"}}})
	chunks := []string{"safe-one ", "chunk-bound", "ary-secret safe-two ", encoded[:7], encoded[7:], " safe-three"}
	var persisted strings.Builder
	for _, chunk := range chunks {
		persisted.WriteString(redactor.RedactChunk("stdout", chunk))
	}
	for _, event := range redactor.Flush() {
		persisted.WriteString(event.Message)
	}
	output := persisted.String()
	if strings.Contains(output, value) || strings.Contains(output, encoded) {
		t.Fatalf("streaming redactor leaked sensitive form: %q", output)
	}
	for _, sentinel := range []string{"safe-one", "safe-two", "safe-three"} {
		if !strings.Contains(output, sentinel) {
			t.Fatalf("streaming redactor lost %q: %q", sentinel, output)
		}
	}
	if strings.Count(output, RedactionMarker) != 2 {
		t.Fatalf("streaming redactor markers=%q", output)
	}
}

func TestRedactorKeepsIndependentStreamBoundaries(t *testing.T) {
	redactor := NewRedactor([]SecretMaterial{{Value: "secret"}})
	stdout := redactor.RedactChunk("stdout", "sec")
	stderr := redactor.RedactChunk("stderr", "ret")
	for _, event := range redactor.Flush() {
		switch event.Stream {
		case "stdout":
			stdout += event.Message
		case "stderr":
			stderr += event.Message
		}
	}
	if stdout != "sec" || stderr != "ret" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRedactorWaitsForLongestPrefixAndNeverRescansMarkers(t *testing.T) {
	for _, input := range []string{"abcdef abc", "abcX abcdef", "abc"} {
		for split := 0; split <= len(input); split++ {
			redactor := NewRedactor([]SecretMaterial{{Value: "abc"}, {Value: "abcdef"}, {Value: "D"}})
			var got string
			for _, chunk := range []string{input[:split], input[split:]} {
				got += redactor.RedactChunk("stdout", chunk)
				if len(redactor.pending["stdout"]) >= len("abcdef") {
					t.Fatal("pending output exceeded longest proper pattern prefix")
				}
			}
			for _, event := range redactor.Flush() {
				got += event.Message
			}
			want := map[string]string{"abcdef abc": RedactionMarker + " " + RedactionMarker, "abcX abcdef": RedactionMarker + "X " + RedactionMarker, "abc": RedactionMarker}[input]
			if got != want || redactor.Redact(input) != want {
				t.Fatalf("overlap redaction=%q static=%q want%q", got, redactor.Redact(input), want)
			}
		}
	}
}
