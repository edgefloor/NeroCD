package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// HostingBuildLogBatchLimit bounds receipt and event allocation.
const HostingBuildLogBatchLimit = 64

// HostingBuildLogMessageLimit bounds one UTF-8 message in bytes.
const HostingBuildLogMessageLimit = 16 * 1024

// HostingBuildLogByteLimit bounds encoded batches and public pages.
const HostingBuildLogByteLimit = 256 * 1024

// HostingBuildLogReadLimit bounds retained rows per reconnect.
const HostingBuildLogReadLimit = 200

// HostingBuildLogEvent is nonsecret output bound to one immutable request identity.
type HostingBuildLogEvent struct {
	EventKey        string     `json:"event_key"`
	Sequence        int        `json:"sequence"`
	Component       string     `json:"component"`
	Stream          string     `json:"stream"`
	Message         string     `json:"message"`
	SourceTimestamp *time.Time `json:"source_timestamp,omitempty"`
}

// HostingBuildLogBatch binds output to one full fenced attempt.
type HostingBuildLogBatch struct {
	Authority HostingBuildAuthority  `json:"authority"`
	Events    []HostingBuildLogEvent `json:"events"`
}

// HostingBuildLogReceipt acknowledges content independently of plaintext retention.
type HostingBuildLogReceipt struct {
	EventKey          string `json:"event_key"`
	RequestedSequence int    `json:"requested_sequence"`
	Sequence          int    `json:"sequence"`
	BodyHash          string `json:"body_hash"`
}

// HostingBuildLogAck never echoes plaintext. Clients must match the full tuple,
// ordered event identities and canonical hashes before acknowledging a journal.
type HostingBuildLogAck struct {
	Authority HostingBuildAuthority    `json:"authority"`
	Receipts  []HostingBuildLogReceipt `json:"receipts"`
}

// HostingBuildLogItem is the public output projection without execution authority.
type HostingBuildLogItem struct {
	Sequence         int        `json:"sequence"`
	Component        string     `json:"component"`
	Stream           string     `json:"stream"`
	Message          string     `json:"message"`
	CreatedAt        time.Time  `json:"created_at"`
	SourceTimestamp  *time.Time `json:"source_timestamp,omitempty"`
	MessageTruncated bool       `json:"message_truncated,omitempty"`
}

// HostingBuildLogPage distinguishes retained pagination from lost history.
type HostingBuildLogPage struct {
	Items                     []HostingBuildLogItem `json:"items"`
	NextSequence              int                   `json:"next_sequence"`
	EarliestAvailableSequence int                   `json:"earliest_available_sequence"`
	DeletedThroughSequence    int                   `json:"deleted_through_sequence"`
	Gap                       bool                  `json:"gap"`
	Terminal                  bool                  `json:"terminal"`
	Truncated                 bool                  `json:"truncated"`
	HistoryComplete           bool                  `json:"history_complete"`
}

// CanonicalHostingBuildLogs validates before copying, then normalizes timestamps
// to UTC PostgreSQL microseconds by truncation (never by rounding). The hash uses
// this submitted identity, independently of subsequent plaintext retention.
func CanonicalHostingBuildLogs(in HostingBuildLogBatch) (HostingBuildLogBatch, error) {
	a := in.Authority
	if a.BuildID == "" || a.RunID != "build_run_"+a.BuildID || a.LeaseID == "" || a.Fence == "" || a.Attempt < 1 || a.Attempt > math.MaxInt32 || len(in.Events) < 1 || len(in.Events) > HostingBuildLogBatchLimit {
		return HostingBuildLogBatch{}, fmt.Errorf("invalid build log authority or batch")
	}
	out := HostingBuildLogBatch{Authority: a, Events: append([]HostingBuildLogEvent(nil), in.Events...)}
	seen := map[string]bool{}
	for i, e := range out.Events {
		if e.EventKey == "" || len(e.EventKey) > 128 || strings.ContainsFunc(e.EventKey, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || seen[e.EventKey] || e.Sequence < 1 || e.Sequence > math.MaxInt32 || !utf8.ValidString(e.Message) || strings.ContainsRune(e.Message, 0) || len(e.Message) > HostingBuildLogMessageLimit {
			return HostingBuildLogBatch{}, fmt.Errorf("invalid build log event")
		}
		seen[e.EventKey] = true
		if e.Component != "build" && e.Component != "buildkit" && e.Component != "railpack" {
			return HostingBuildLogBatch{}, fmt.Errorf("invalid build log component")
		}
		if e.Stream != "system" && e.Stream != "stdout" && e.Stream != "stderr" {
			return HostingBuildLogBatch{}, fmt.Errorf("invalid build log stream")
		}
		if e.SourceTimestamp != nil {
			t := e.SourceTimestamp.UTC().Truncate(time.Microsecond)
			if t.Year() < 1 || t.Year() > 9999 {
				return HostingBuildLogBatch{}, fmt.Errorf("invalid source timestamp")
			}
			out.Events[i].SourceTimestamp = &t
		}
	}
	data, err := json.Marshal(out)
	if err != nil || len(data) > HostingBuildLogByteLimit {
		return HostingBuildLogBatch{}, fmt.Errorf("build log batch exceeds byte limit")
	}
	return out, nil
}

// HostingBuildLogBodyHash hashes canonical submitted content and full authority.
func HostingBuildLogBodyHash(a HostingBuildAuthority, e HostingBuildLogEvent) string {
	return HostingDocumentHash(struct {
		Authority HostingBuildAuthority `json:"authority"`
		Event     HostingBuildLogEvent  `json:"event"`
	}{a, e})
}

// ContainsHostingBuildExecution visits each accepted workflow pointer once.
func ContainsHostingBuildExecution(spec RunSpec, workflow Workflow) bool {
	internal := func(s RunSpec) bool {
		return strings.TrimSpace(s.Type) == HostingBuildTaskType || s.HostingBuildID != ""
	}
	if internal(spec) {
		return true
	}
	pending := []*Workflow{&workflow, spec.Workflow}
	seen := map[*Workflow]bool{}
	for len(pending) > 0 {
		w := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if w == nil || seen[w] {
			continue
		}
		seen[w] = true
		for _, step := range w.Steps {
			if internal(step.RunSpec) {
				return true
			}
			pending = append(pending, step.RunSpec.Workflow)
		}
	}
	return false
}
