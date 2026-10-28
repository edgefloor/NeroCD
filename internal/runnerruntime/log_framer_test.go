package runnerruntime

import (
	"strings"
	"testing"
	"unicode/utf8"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func TestProcessLogFramerPreservesSplitRunesAndLines(t *testing.T) {
	const output = "first € line\r\nsecond 日本語 line\nlast 🦊"
	for cut := 0; cut <= len(output); cut++ {
		var framer processLogFramer
		events := framer.push(domain.LogStdout, output[:cut])
		events = append(events, framer.push(domain.LogStdout, output[cut:])...)
		events = append(events, framer.flush()...)
		var lines []string
		for _, event := range events {
			if !utf8.ValidString(event.Message) {
				t.Fatalf("cut %d produced invalid UTF-8", cut)
			}
			lines = append(lines, event.Message)
		}
		if strings.Join(lines, "\n") != "first € line\nsecond 日本語 line\nlast 🦊" {
			t.Fatalf("cut %d: %q", cut, lines)
		}
	}
}

func TestProcessLogFramerBoundsUnterminatedLinesAndSeparatesStreams(t *testing.T) {
	var framer processLogFramer
	text := strings.Repeat("€", maxProcessLogRecordBytes)
	var events []runner.ProcessEvent
	for offset := 0; offset < len(text); offset += 17 {
		events = append(events, framer.push(domain.LogStdout, text[offset:min(offset+17, len(text))])...)
		if len(framer.pending[domain.LogStdout]) > maxProcessLogRecordBytes {
			t.Fatal("unterminated output grew beyond the framing budget")
		}
	}
	events = append(events, framer.push(domain.LogStderr, "error")...)
	events = append(events, framer.flush()...)
	var stdout, stderr strings.Builder
	for _, event := range events {
		if !utf8.ValidString(event.Message) || len(event.Message) > maxProcessLogRecordBytes {
			t.Fatal("record is invalid or unbounded")
		}
		if event.Stream == domain.LogStdout {
			stdout.WriteString(event.Message)
		} else {
			stderr.WriteString(event.Message)
		}
	}
	if stdout.String() != text || stderr.String() != "error" || len(framer.flush()) != 0 {
		t.Fatal("stream output was changed or flushed twice")
	}
}
