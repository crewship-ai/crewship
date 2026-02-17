package logging

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type LogRecord struct {
	Time    time.Time         `json:"time"`
	Level   string            `json:"level"`
	Message string            `json:"msg"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

type RingBuffer struct {
	mu      sync.RWMutex
	entries []LogRecord
	cap     int
	pos     int
	full    bool
}

func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		entries: make([]LogRecord, capacity),
		cap:     capacity,
	}
}

func (rb *RingBuffer) Append(record LogRecord) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.entries[rb.pos] = record
	rb.pos = (rb.pos + 1) % rb.cap
	if rb.pos == 0 {
		rb.full = true
	}
}

func (rb *RingBuffer) Entries(limit int) []LogRecord {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	var count int
	if rb.full {
		count = rb.cap
	} else {
		count = rb.pos
	}

	if limit > 0 && limit < count {
		count = limit
	}

	result := make([]LogRecord, 0, count)

	if rb.full {
		start := rb.pos
		total := rb.cap
		skip := total - count
		for i := skip; i < total; i++ {
			result = append(result, rb.entries[(start+i)%rb.cap])
		}
	} else {
		start := rb.pos - count
		if start < 0 {
			start = 0
		}
		result = append(result, rb.entries[start:rb.pos]...)
	}

	return result
}

// RingHandler is a slog.Handler that captures log records into a RingBuffer
// and forwards them to an inner handler.
type RingHandler struct {
	inner  slog.Handler
	buffer *RingBuffer
	attrs  []slog.Attr
	group  string
}

func NewRingHandler(inner slog.Handler, buffer *RingBuffer) *RingHandler {
	return &RingHandler{inner: inner, buffer: buffer}
}

func (h *RingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *RingHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make(map[string]string)
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		attrs[key] = a.Value.String()
		return true
	})

	record := LogRecord{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
	}
	if len(attrs) > 0 {
		record.Attrs = attrs
	}
	h.buffer.Append(record)

	return h.inner.Handle(ctx, r)
}

func (h *RingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs), len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	newAttrs = append(newAttrs, attrs...)
	return &RingHandler{
		inner:  h.inner.WithAttrs(attrs),
		buffer: h.buffer,
		attrs:  newAttrs,
		group:  h.group,
	}
}

func (h *RingHandler) WithGroup(name string) slog.Handler {
	g := h.group
	if g != "" {
		g += "." + name
	} else {
		g = name
	}
	return &RingHandler{
		inner:  h.inner.WithGroup(name),
		buffer: h.buffer,
		attrs:  h.attrs,
		group:  g,
	}
}
