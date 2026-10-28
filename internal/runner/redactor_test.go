package runner

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
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
				if redactor.pending["stdout"].position-redactor.pending["stdout"].emitted >= int64(len("abcdef")) {
					t.Fatal("pending output exceeded longest-pattern lookahead")
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

func TestRedactorDistinctFragmentsScale(t *testing.T) {
	materials := make([]SecretMaterial, 100000)
	for i := range materials {
		materials[i] = SecretMaterial{Value: fmt.Sprintf("%05x", i)}
	}
	redactor := NewRedactor(materials)
	input := strings.Repeat("!", 16384)
	got := redactor.RedactChunk("stdout", input)
	for _, event := range redactor.Flush() {
		got += event.Message
	}
	if got != input {
		t.Fatal("ordinary output changed")
	}
}

func TestRedactorMergesIntersectingSecretsWithoutSuffixLeaks(t *testing.T) {
	for _, chunks := range [][]string{{"abcdef"}, {"ab", "cd", "ef"}, {"a", "b", "c", "d", "e", "f"}} {
		redactor := NewRedactor([]SecretMaterial{{Value: "abc"}, {Value: "cdef"}})
		var got string
		for _, chunk := range chunks {
			got += redactor.RedactChunk("stdout", chunk)
		}
		for _, event := range redactor.Flush() {
			got += event.Message
		}
		if got != RedactionMarker || redactor.Redact("abcdef") != RedactionMarker {
			t.Fatal("intersecting secrets leaked an unmatched suffix")
		}
		if len(redactor.pending) != 0 || len(redactor.order) != 0 {
			t.Fatal("flush retained stream buffers")
		}
	}
}

func TestRedactorMatchesIndependentSmallAlphabetOracle(t *testing.T) {
	patterns := []string{"a", "b", "aa", "ab", "ba", "bb", "aaa"}
	for i, first := range patterns {
		for _, second := range patterns[i:] {
			selected := []string{first, second}
			for size := 0; size <= 6; size++ {
				for bits := 0; bits < 1<<size; bits++ {
					var text strings.Builder
					for index := 0; index < size; index++ {
						if bits&(1<<index) == 0 {
							text.WriteByte('a')
						} else {
							text.WriteByte('b')
						}
					}
					input := text.String()
					want := redactionOracle(input, selected)
					redactor := NewRedactor([]SecretMaterial{{Value: first}, {Value: second}})
					if got := redactor.Redact(input); got != want {
						t.Fatalf("oracle static mismatch input=%q patterns=%q got=%q want=%q", input, selected, got, want)
					}
					for split := 0; split <= len(input); split++ {
						got := redactor.RedactChunk("stdout", input[:split]) + redactor.RedactChunk("stdout", input[split:])
						for _, event := range redactor.Flush() {
							got += event.Message
						}
						if got != want {
							t.Fatalf("oracle stream mismatch input=%q patterns=%q split=%d got=%q want=%q", input, selected, split, got, want)
						}
					}
				}
			}
		}
	}
}

// redactionOracle locates every literal independently, unions intersecting spans,
// then copies unprotected original text. It does not use matcher implementation.
func redactionOracle(input string, patterns []string) string {
	type span struct{ start, end int }
	var spans []span
	for _, pattern := range patterns {
		for start := 0; start+len(pattern) <= len(input); start++ {
			if input[start:start+len(pattern)] == pattern {
				spans = append(spans, span{start, start + len(pattern)})
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return spans[i].end > spans[j].end
		}
		return spans[i].start < spans[j].start
	})
	var merged []span
	for _, found := range spans {
		if len(merged) > 0 && found.start < merged[len(merged)-1].end {
			merged[len(merged)-1].end = max(merged[len(merged)-1].end, found.end)
		} else {
			merged = append(merged, found)
		}
	}
	var safe strings.Builder
	offset := 0
	for _, found := range merged {
		safe.WriteString(input[offset:found.start])
		safe.WriteString(RedactionMarker)
		offset = found.end
	}
	safe.WriteString(input[offset:])
	return safe.String()
}

func TestRedactorEmitsUnrelatedOutputBeforeFlush(t *testing.T) {
	redactor := NewRedactor([]SecretMaterial{{Value: strings.Repeat("x", 65535) + "Y", Encodings: []string{"hex"}}})
	input := "ordinary build output ready\n"
	if got := redactor.RedactChunk("stdout", input); got != input {
		t.Fatal("ordinary output waited for unrelated longest pattern")
	}
	if len(redactor.Flush()) != 0 {
		t.Fatal("ordinary output retained until flush")
	}
}
