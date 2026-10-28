package runner

import (
	"encoding/base64"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
)

// RedactionMarker replaces secret material in runner output.
const RedactionMarker = "[REDACTED]"

// SecretMaterial describes a value and encodings that must be redacted.
type SecretMaterial struct {
	Value     string
	Encodings []string
}

// Redactor is an exact, streaming redactor. It retains only a suffix that is a
// possible sensitive-pattern prefix, so values split across callbacks cannot
// be persisted before their continuation is observed.
type Redactor struct {
	mu       sync.Mutex
	patterns []string
	pending  map[string]string
	order    []string
}

// NewRedactor constructs a streaming redactor for materials.
func NewRedactor(materials []SecretMaterial) *Redactor {
	unique := map[string]struct{}{}
	for _, material := range materials {
		if material.Value == "" {
			continue
		}
		unique[material.Value] = struct{}{}
		for _, configured := range material.Encodings {
			switch strings.ToLower(strings.TrimSpace(configured)) {
			case "base64":
				unique[base64.StdEncoding.EncodeToString([]byte(material.Value))] = struct{}{}
			case "base64url":
				unique[base64.URLEncoding.EncodeToString([]byte(material.Value))] = struct{}{}
				unique[base64.RawURLEncoding.EncodeToString([]byte(material.Value))] = struct{}{}
			case "hex":
				encoded := hex.EncodeToString([]byte(material.Value))
				unique[encoded] = struct{}{}
				unique[strings.ToUpper(encoded)] = struct{}{}
			}
		}
	}
	patterns := make([]string, 0, len(unique))
	for pattern := range unique {
		if pattern != "" {
			patterns = append(patterns, pattern)
		}
	}
	sort.Slice(patterns, func(i, j int) bool {
		if len(patterns[i]) == len(patterns[j]) {
			return patterns[i] < patterns[j]
		}
		return len(patterns[i]) > len(patterns[j])
	})
	return &Redactor{patterns: patterns, pending: map[string]string{}}
}

// Redact removes known secret material from text.
func (r *Redactor) Redact(text string) string {
	if r == nil {
		return text
	}
	return r.redactLocked(text)
}

// RedactChunk redacts one output chunk while preserving stream boundaries.
func (r *Redactor) RedactChunk(stream, chunk string) string {
	if r == nil || len(r.patterns) == 0 {
		return chunk
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pending[stream]; !exists {
		r.order = append(r.order, stream)
	}
	pending := r.pending[stream]
	var safe strings.Builder
	for index := 0; index < len(chunk); index++ {
		pending += chunk[index : index+1]
		for len(pending) > 0 {
			// A complete short pattern can still be the prefix of a longer secret.
			// Wait until continuation distinguishes them before emitting either.
			if r.isPatternPrefix(pending) {
				break
			}
			if size := r.longestPatternPrefix(pending); size > 0 {
				safe.WriteString(RedactionMarker)
				pending = pending[size:]
				continue
			}
			safe.WriteByte(pending[0])
			pending = pending[1:]
		}
	}
	r.pending[stream] = pending
	return safe.String()
}

// Flush returns any buffered redacted output.
func (r *Redactor) Flush() []ProcessEvent {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	events := make([]ProcessEvent, 0, len(r.order))
	for _, stream := range r.order {
		if pending := r.pending[stream]; pending != "" {
			events = append(events, ProcessEvent{Stream: stream, Message: r.redactLocked(pending)})
		}
		delete(r.pending, stream)
	}
	r.order = nil
	return events
}

// longestPatternPrefix returns the longest complete match at the start of value.
// NewRedactor sorts immutable patterns by descending length.
func (r *Redactor) longestPatternPrefix(value string) int {
	for _, pattern := range r.patterns {
		if strings.HasPrefix(value, pattern) {
			return len(pattern)
		}
	}
	return 0
}

func (r *Redactor) isPatternPrefix(value string) bool {
	for _, pattern := range r.patterns {
		if len(value) < len(pattern) && strings.HasPrefix(pattern, value) {
			return true
		}
	}
	return false
}

func (r *Redactor) redactLocked(text string) string {
	var safe strings.Builder
	for len(text) > 0 {
		if size := r.longestPatternPrefix(text); size > 0 {
			safe.WriteString(RedactionMarker)
			text = text[size:]
			continue
		}
		safe.WriteByte(text[0])
		text = text[1:]
	}
	return safe.String()
}
