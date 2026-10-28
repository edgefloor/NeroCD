package runnerruntime

import (
	"strings"
	"sync"
	"unicode/utf8"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

const maxProcessLogRecordBytes = 32 << 10

// processLogFramer receives only redacted bytes. It preserves UTF-8 characters
// across reads and turns stdout/stderr into bounded logical log records.
type processLogFramer struct {
	mu      sync.Mutex
	pending map[string]string
	order   []string
}

func (f *processLogFramer) push(stream, text string) []runner.ProcessEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending == nil {
		f.pending = map[string]string{}
	}
	if _, exists := f.pending[stream]; !exists {
		f.order = append(f.order, stream)
	}
	f.pending[stream] += text
	return f.drain(stream, false)
}

func (f *processLogFramer) flush() []runner.ProcessEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var events []runner.ProcessEvent
	for _, stream := range f.order {
		events = append(events, f.drain(stream, true)...)
	}
	clear(f.pending)
	f.order = nil
	return events
}

func (f *processLogFramer) drain(stream string, final bool) []runner.ProcessEvent {
	var events []runner.ProcessEvent
	text := f.pending[stream]
	for len(text) > 0 {
		end := min(len(text), maxProcessLogRecordBytes)
		newline := strings.IndexByte(text[:end], '\n')
		if newline >= 0 {
			end = newline + 1
		} else if !final && len(text) < maxProcessLogRecordBytes && (stream == domain.LogStdout || stream == domain.LogStderr) {
			break
		} else if !final || end < len(text) {
			// At most the final three bytes can be an unfinished UTF-8 rune.
			for start := max(0, end-utf8.UTFMax+1); start < end; start++ {
				if utf8.RuneStart(text[start]) && !utf8.FullRuneInString(text[start:end]) {
					end = start
					break
				}
			}
		}
		if end == 0 {
			break
		}
		message := text[:end]
		if newline >= 0 {
			message = strings.TrimSuffix(strings.TrimSuffix(message, "\n"), "\r")
		}
		// Invalid binary output is replaced only after masking and framing.
		message = strings.ToValidUTF8(message, "\uFFFD")
		if message != "" {
			events = append(events, runner.ProcessEvent{Stream: stream, Message: message})
		}
		text = text[end:]
	}
	f.pending[stream] = text
	return events
}
