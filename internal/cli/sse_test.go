package cli

import (
	"strings"
	"testing"
)

func TestParseSSE_SingleEvent(t *testing.T) {
	stream := "id: 42\nevent: entry\ndata: {\"hello\":\"world\"}\n\n"
	var got []SSEEvent
	err := parseSSE(strings.NewReader(stream), func(e SSEEvent) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].ID != "42" || got[0].Event != "entry" || got[0].Data != `{"hello":"world"}` {
		t.Errorf("bad event: %+v", got[0])
	}
}

func TestParseSSE_MultipleEvents(t *testing.T) {
	stream := "id: 1\ndata: a\n\nid: 2\ndata: b\n\nid: 3\ndata: c\n\n"
	var got []SSEEvent
	_ = parseSSE(strings.NewReader(stream), func(e SSEEvent) error {
		got = append(got, e)
		return nil
	})
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	for i, want := range []string{"1", "2", "3"} {
		if got[i].ID != want {
			t.Errorf("event %d ID = %q, want %q", i, got[i].ID, want)
		}
	}
}

func TestParseSSE_MultilineData(t *testing.T) {
	stream := "data: first\ndata: second\ndata: third\n\n"
	var got []SSEEvent
	_ = parseSSE(strings.NewReader(stream), func(e SSEEvent) error {
		got = append(got, e)
		return nil
	})
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].Data != "first\nsecond\nthird" {
		t.Errorf("multiline data: %q", got[0].Data)
	}
}

func TestParseSSE_Comment(t *testing.T) {
	stream := ": heartbeat\n\nid: 1\ndata: real\n\n"
	var got []SSEEvent
	_ = parseSSE(strings.NewReader(stream), func(e SSEEvent) error {
		got = append(got, e)
		return nil
	})
	// Two events: the comment-only and the real one.
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Comment != "heartbeat" {
		t.Errorf("comment: %q", got[0].Comment)
	}
	if got[1].Data != "real" {
		t.Errorf("data: %q", got[1].Data)
	}
}

func TestParseSSE_StopsOnError(t *testing.T) {
	stream := "data: a\n\ndata: b\n\ndata: c\n\n"
	count := 0
	err := parseSSE(strings.NewReader(stream), func(e SSEEvent) error {
		count++
		if count == 2 {
			return errStop
		}
		return nil
	})
	if err != errStop {
		t.Errorf("expected errStop, got %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 calls before stop, got %d", count)
	}
}

func TestParseSSE_TrailingNoBlankLine(t *testing.T) {
	// Server closes without sending the terminating blank line.
	// We should still dispatch the buffered event.
	stream := "id: 9\ndata: tail"
	var got []SSEEvent
	err := parseSSE(strings.NewReader(stream), func(e SSEEvent) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0].ID != "9" || got[0].Data != "tail" {
		t.Errorf("trailing event lost: %+v", got)
	}
}

func TestParseSSE_CRLFLineEndings(t *testing.T) {
	stream := "id: 1\r\ndata: hi\r\n\r\n"
	var got []SSEEvent
	_ = parseSSE(strings.NewReader(stream), func(e SSEEvent) error {
		got = append(got, e)
		return nil
	})
	if len(got) != 1 {
		t.Fatalf("CRLF not handled: %d events", len(got))
	}
	if got[0].Data != "hi" {
		t.Errorf("data: %q", got[0].Data)
	}
}

func TestParseSSE_RetryFieldIgnored(t *testing.T) {
	stream := "retry: 5000\nid: 1\ndata: x\n\n"
	var got []SSEEvent
	_ = parseSSE(strings.NewReader(stream), func(e SSEEvent) error {
		got = append(got, e)
		return nil
	})
	if len(got) != 1 || got[0].Data != "x" {
		t.Errorf("retry should be ignored: %+v", got)
	}
}

func TestParseSSE_FieldWithoutSpaceAfterColon(t *testing.T) {
	// SSE spec: leading single space after ":" is optional.
	stream := "id:42\ndata:no-space\n\n"
	var got []SSEEvent
	_ = parseSSE(strings.NewReader(stream), func(e SSEEvent) error {
		got = append(got, e)
		return nil
	})
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].ID != "42" || got[0].Data != "no-space" {
		t.Errorf("colon-no-space parse: %+v", got[0])
	}
}

// errStop sentinel for TestParseSSE_StopsOnError.
var errStop = stopErr{}

type stopErr struct{}

func (stopErr) Error() string { return "stop" }
