package logging

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"
)

// TestNewRingBuffer_PanicsOnBadCapacity is a regression for the CodeRabbit
// finding that capacity<=0 would cause a later modulo-by-zero panic inside
// Append; the constructor should fail fast instead.
func TestNewRingBuffer_PanicsOnBadCapacity(t *testing.T) {
	for _, cap := range []int{0, -1, -100} {
		cap := cap
		t.Run("cap="+itoa(cap), func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for capacity=%d", cap)
				}
			}()
			_ = NewRingBuffer(cap)
		})
	}
}

// TestNewRingHandler_PanicsOnNilArgs is a regression for the CodeRabbit
// finding that NewRingHandler accepted nil inner/buffer and then panicked
// later inside Enabled / Handle. Fail fast at construction.
func TestNewRingHandler_PanicsOnNilArgs(t *testing.T) {
	rb := NewRingBuffer(4)
	inner := slog.NewTextHandler(bytes.NewBuffer(nil), nil)

	t.Run("nil inner", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic on nil inner")
			}
		}()
		_ = NewRingHandler(nil, rb)
	})

	t.Run("nil buffer", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic on nil buffer")
			}
		}()
		_ = NewRingHandler(inner, nil)
	})

	// Sanity: valid args must NOT panic.
	t.Run("happy path", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("unexpected panic: %v", r)
			}
		}()
		h := NewRingHandler(inner, rb)
		if h == nil {
			t.Error("got nil handler")
		}
	})
}

// itoa is a tiny local helper to avoid pulling in strconv just for subtest
// names; keeps the test file's imports minimal.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestRingBufferAppendAndEntries(t *testing.T) {
	rb := NewRingBuffer(5)

	for i := 0; i < 3; i++ {
		rb.Append(LogRecord{Time: time.Now(), Level: "INFO", Message: "msg"})
	}

	entries := rb.Entries(0)
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

func TestRingBufferWraparound(t *testing.T) {
	rb := NewRingBuffer(3)

	for i := 0; i < 5; i++ {
		rb.Append(LogRecord{Time: time.Now(), Level: "INFO", Message: "msg" + string(rune('A'+i))})
	}

	entries := rb.Entries(0)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (capacity), got %d", len(entries))
	}
	if entries[0].Message != "msgC" {
		t.Errorf("expected oldest entry 'msgC', got %q", entries[0].Message)
	}
	if entries[2].Message != "msgE" {
		t.Errorf("expected newest entry 'msgE', got %q", entries[2].Message)
	}
}

func TestRingBufferLimit(t *testing.T) {
	rb := NewRingBuffer(10)

	for i := 0; i < 8; i++ {
		rb.Append(LogRecord{Time: time.Now(), Level: "INFO", Message: "msg"})
	}

	entries := rb.Entries(3)
	if len(entries) != 3 {
		t.Errorf("expected 3 entries (limited), got %d", len(entries))
	}
}

func TestRingBufferLimitWithWraparound(t *testing.T) {
	rb := NewRingBuffer(5)

	for i := 0; i < 10; i++ {
		rb.Append(LogRecord{Time: time.Now(), Level: "INFO", Message: "msg" + string(rune('A'+i))})
	}

	entries := rb.Entries(2)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Should be last 2: msgI, msgJ
	if entries[0].Message != "msgI" {
		t.Errorf("expected 'msgI', got %q", entries[0].Message)
	}
	if entries[1].Message != "msgJ" {
		t.Errorf("expected 'msgJ', got %q", entries[1].Message)
	}
}

func TestRingBufferEmpty(t *testing.T) {
	rb := NewRingBuffer(5)
	entries := rb.Entries(0)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestRingHandlerForwardsToInner(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	rb := NewRingBuffer(10)
	handler := NewRingHandler(inner, rb)
	logger := slog.New(handler)

	logger.Info("test message", "key", "value")

	if buf.Len() == 0 {
		t.Error("expected inner handler to receive log")
	}

	entries := rb.Entries(0)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry in ring buffer, got %d", len(entries))
	}
	if entries[0].Message != "test message" {
		t.Errorf("expected 'test message', got %q", entries[0].Message)
	}
	if entries[0].Attrs["key"] != "value" {
		t.Errorf("expected attr key=value, got %v", entries[0].Attrs)
	}
}

func TestRingHandlerEnabled(t *testing.T) {
	inner := slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})
	rb := NewRingBuffer(10)
	handler := NewRingHandler(inner, rb)

	if handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected INFO to be disabled when inner handler is WARN level")
	}
	if !handler.Enabled(context.Background(), slog.LevelError) {
		t.Error("expected ERROR to be enabled")
	}
}

func TestRingHandlerWithAttrsNoMutation(t *testing.T) {
	inner := slog.NewJSONHandler(&bytes.Buffer{}, nil)
	rb := NewRingBuffer(10)
	handler := NewRingHandler(inner, rb)

	// Create a child handler with attrs
	child1 := handler.WithAttrs([]slog.Attr{slog.String("k1", "v1")})

	// Create another child -- must not see k1 from the first child
	child2 := handler.WithAttrs([]slog.Attr{slog.String("k2", "v2")})

	// Log via child1
	logger1 := slog.New(child1)
	logger1.Info("from child1")

	// Log via child2
	logger2 := slog.New(child2)
	logger2.Info("from child2")

	entries := rb.Entries(0)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// child1's entry should have k1 but NOT k2
	if entries[0].Attrs["k1"] != "v1" {
		t.Errorf("child1 entry missing k1=v1, got %v", entries[0].Attrs)
	}
	if _, hasK2 := entries[0].Attrs["k2"]; hasK2 {
		t.Error("child1 entry must NOT have k2 (slice aliasing bug)")
	}

	// child2's entry should have k2 but NOT k1
	if entries[1].Attrs["k2"] != "v2" {
		t.Errorf("child2 entry missing k2=v2, got %v", entries[1].Attrs)
	}
	if _, hasK1 := entries[1].Attrs["k1"]; hasK1 {
		t.Error("child2 entry must NOT have k1 (slice aliasing bug)")
	}
}
