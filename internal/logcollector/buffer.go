package logcollector

import (
	"strings"
	"sync"
	"time"
)

const (
	outputBufferTimeout = 300 * time.Millisecond
	outputBufferMaxLen  = 4096
)

// OutputBuffer aggregates streaming "output" tokens into complete lines
// before writing them to the log. Non-output events are passed through
// immediately. Call Close to flush any remaining buffered content.
type OutputBuffer struct {
	writer  *Writer
	crewID  string
	agentID string

	mu    sync.Mutex
	buf   strings.Builder
	ts    time.Time
	evt   string // current buffered event type
	timer *time.Timer
}

func NewOutputBuffer(w *Writer, crewID, agentID string) *OutputBuffer {
	return &OutputBuffer{
		writer:  w,
		crewID:  crewID,
		agentID: agentID,
	}
}

// Append buffers "output" events and flushes on newline or timeout.
// All other event types are written immediately.
// isStreamedEvent returns true for event types that arrive as small token
// fragments and benefit from aggregation (text, thinking, output).
// Events like result, system, tool_call, tool_result are passed through immediately.
func isStreamedEvent(event string) bool {
	return event == "output" || event == "text" || event == "thinking"
}

func eventLevel(event string) string {
	switch event {
	case "error":
		return "error"
	case "system", "rate_limit", "failover":
		return "warn"
	default:
		return "info"
	}
}

func (ob *OutputBuffer) Append(entry LogEntry) error {
	if !isStreamedEvent(entry.Event) {
		ob.flush()
		if entry.Level == "" {
			entry.Level = eventLevel(entry.Event)
		}
		return ob.writer.Append(ob.crewID, ob.agentID, entry)
	}

	ob.mu.Lock()
	defer ob.mu.Unlock()

	// Flush if event type changed (e.g. text → thinking)
	if ob.buf.Len() > 0 && ob.evt != entry.Event {
		if err := ob.flushLocked(); err != nil {
			return err
		}
	}

	if ob.buf.Len() == 0 {
		ob.ts = entry.Timestamp
		ob.evt = entry.Event
	}
	ob.buf.WriteString(entry.Content)

	if ob.buf.Len() >= outputBufferMaxLen || strings.Contains(entry.Content, "\n") {
		return ob.flushLocked()
	}

	ob.resetTimer()
	return nil
}

// Close flushes remaining content and stops the timer.
func (ob *OutputBuffer) Close() {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	if ob.timer != nil {
		ob.timer.Stop()
		ob.timer = nil
	}
	_ = ob.flushLocked()
}

func (ob *OutputBuffer) flush() {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	_ = ob.flushLocked()
}

func (ob *OutputBuffer) flushLocked() error {
	if ob.timer != nil {
		ob.timer.Stop()
		ob.timer = nil
	}
	if ob.buf.Len() == 0 {
		return nil
	}
	content := ob.buf.String()
	event := ob.evt
	ob.buf.Reset()
	ob.evt = ""

	return ob.writer.Append(ob.crewID, ob.agentID, LogEntry{
		Timestamp: ob.ts,
		Level:     "info",
		Agent:     ob.agentID,
		Event:     event,
		Content:   content,
	})
}

func (ob *OutputBuffer) resetTimer() {
	if ob.timer != nil {
		ob.timer.Stop()
	}
	ob.timer = time.AfterFunc(outputBufferTimeout, func() {
		ob.mu.Lock()
		defer ob.mu.Unlock()
		_ = ob.flushLocked()
	})
}
