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

// Redactor indexes immutable byte patterns in a failure-linked trie. Each stream
// retains at most the longest pattern minus one bytes before emitting safe text.
// Sparse children have at most 256 entries, independent of the pattern count.
type Redactor struct {
	mu      sync.Mutex
	nodes   []redactionNode
	longest int
	pending map[string]*redactionStream
	order   []string
}

type redactionNode struct {
	child     int32
	sibling   int32
	failure   int32
	match     int32
	lookahead int32
	label     byte
}

type redactionInterval struct{ start, end int64 }

type redactionStream struct {
	state             int32
	position, emitted int64
	ring              []byte
	matches           []redactionInterval
	head              int
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
	sort.Strings(patterns)
	longest, count := 0, 1
	previous := ""
	for _, pattern := range patterns {
		common := commonRedactionPrefix(previous, pattern)
		count += len(pattern) - common
		longest = max(longest, len(pattern))
		previous = pattern
	}
	r := &Redactor{nodes: make([]redactionNode, count), longest: longest, pending: map[string]*redactionStream{}}
	path := make([]int32, longest+1)
	previous = ""
	next := int32(1)
	for _, pattern := range patterns {
		common := commonRedactionPrefix(previous, pattern)
		for depth := common; depth < len(pattern); depth++ {
			parent := path[depth]
			r.nodes[next] = redactionNode{label: pattern[depth], sibling: r.nodes[parent].child, lookahead: int32(depth + 1)}
			r.nodes[parent].child = next
			path[depth+1] = next
			next++
		}
		r.nodes[path[len(pattern)]].match = int32(len(pattern))
		previous = pattern
	}
	r.linkFailures()
	return r
}

func commonRedactionPrefix(a, b string) int {
	n := 0
	for n < min(len(a), len(b)) && a[n] == b[n] {
		n++
	}
	return n
}

func (r *Redactor) child(state int32, label byte) int32 {
	for child := r.nodes[state].child; child != 0; child = r.nodes[child].sibling {
		if r.nodes[child].label == label {
			return child
		}
	}
	return 0
}

func (r *Redactor) advance(state int32, label byte) int32 {
	for {
		if child := r.child(state, label); child != 0 {
			return child
		}
		if state == 0 {
			return 0
		}
		state = r.nodes[state].failure
	}
}

func (r *Redactor) linkFailures() {
	// Keep only the two breadth frontiers, rather than a queue of every node.
	var current, next []int32
	for child := r.nodes[0].child; child != 0; child = r.nodes[child].sibling {
		if r.nodes[child].child == 0 {
			r.nodes[child].lookahead = 0
		}
		current = append(current, child)
	}
	for len(current) > 0 {
		next = next[:0]
		for _, parent := range current {
			for child := r.nodes[parent].child; child != 0; child = r.nodes[child].sibling {
				failure := r.advance(r.nodes[parent].failure, r.nodes[child].label)
				r.nodes[child].failure = failure
				r.nodes[child].match = max(r.nodes[child].match, r.nodes[failure].match)
				if r.nodes[child].child == 0 {
					r.nodes[child].lookahead = r.nodes[failure].lookahead
				}
				next = append(next, child)
			}
		}
		current, next = next, current
	}
}

// Redact removes protected spans without rescanning replacement markers.
func (r *Redactor) Redact(text string) string {
	if r == nil || r.longest == 0 {
		return text
	}
	stream := &redactionStream{ring: make([]byte, r.longest)}
	var safe strings.Builder
	r.writeChunk(stream, text, &safe)
	stream.emit(&safe, stream.position)
	clear(stream.ring)
	return safe.String()
}

// RedactChunk redacts one output chunk while preserving stream boundaries.
func (r *Redactor) RedactChunk(name, chunk string) string {
	if r == nil || r.longest == 0 {
		return chunk
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stream := r.pending[name]
	if stream == nil {
		stream = &redactionStream{ring: make([]byte, r.longest)}
		r.pending[name] = stream
		r.order = append(r.order, name)
	}
	var safe strings.Builder
	r.writeChunk(stream, chunk, &safe)
	return safe.String()
}

func (r *Redactor) writeChunk(stream *redactionStream, chunk string, safe *strings.Builder) {
	for i := 0; i < len(chunk); i++ {
		stream.ring[stream.position%int64(len(stream.ring))] = chunk[i]
		stream.position++
		stream.state = r.advance(stream.state, chunk[i])
		if size := r.nodes[stream.state].match; size > 0 {
			stream.protect(redactionInterval{start: stream.position - int64(size), end: stream.position})
		}
		stream.emit(safe, stream.position-int64(r.nodes[stream.state].lookahead))
	}
}

func (s *redactionStream) protect(span redactionInterval) {
	// End positions increase. Merge only overlapping spans; adjacent secrets
	// retain separate replacement markers. Every interval is inserted once.
	for len(s.matches) > s.head && span.start < s.matches[len(s.matches)-1].end {
		last := s.matches[len(s.matches)-1]
		span.start = min(span.start, last.start)
		s.matches = s.matches[:len(s.matches)-1]
	}
	if s.head > 0 && len(s.matches) == cap(s.matches) {
		copy(s.matches, s.matches[s.head:])
		s.matches = s.matches[:len(s.matches)-s.head]
		s.head = 0
	}
	s.matches = append(s.matches, span)
}

func (s *redactionStream) emit(safe *strings.Builder, until int64) {
	for s.emitted < until {
		for s.head < len(s.matches) && s.matches[s.head].end <= s.emitted {
			s.head++
		}
		if s.head < len(s.matches) && s.matches[s.head].start <= s.emitted {
			if s.matches[s.head].start == s.emitted {
				safe.WriteString(RedactionMarker)
			}
		} else {
			safe.WriteByte(s.ring[s.emitted%int64(len(s.ring))])
		}
		s.emitted++
	}
	if s.head == len(s.matches) {
		s.matches = s.matches[:0]
		s.head = 0
	}
}

// Flush returns remaining protected stream text and releases all stream buffers.
func (r *Redactor) Flush() []ProcessEvent {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	events := make([]ProcessEvent, 0, len(r.order))
	for _, name := range r.order {
		stream := r.pending[name]
		var safe strings.Builder
		stream.emit(&safe, stream.position)
		if safe.Len() > 0 {
			events = append(events, ProcessEvent{Stream: name, Message: safe.String()})
		}
		clear(stream.ring)
		delete(r.pending, name)
	}
	r.order = nil
	return events
}
