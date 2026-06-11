package conversation

import (
	"strconv"
	"testing"

	"github.com/crewship-ai/crewship/internal/logging"
)

func TestReadTailReturnsNewestInOrder(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, logging.New("error", "json", nil))
	defer store.Close()

	for i := 0; i < 10; i++ {
		if err := store.Append(ctx, "s", Message{ID: strconv.Itoa(i), Role: RoleUser, Content: "m" + strconv.Itoa(i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// Tail of 3 must be the last 3 appended, oldest-first within the window.
	msgs, err := store.ReadTail(ctx, "s", 3)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	want := []string{"7", "8", "9"}
	for i, m := range msgs {
		if m.ID != want[i] {
			t.Errorf("pos %d: ID = %q, want %q", i, m.ID, want[i])
		}
	}
}

func TestReadTailReturnsAllWhenFewerThanMax(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, logging.New("error", "json", nil))
	defer store.Close()

	for i := 0; i < 4; i++ {
		if err := store.Append(ctx, "s", Message{ID: strconv.Itoa(i), Role: RoleUser, Content: "x"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	msgs, err := store.ReadTail(ctx, "s", 100)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected all 4 messages, got %d", len(msgs))
	}
	if msgs[0].ID != "0" || msgs[3].ID != "3" {
		t.Errorf("ordering wrong: %q..%q", msgs[0].ID, msgs[3].ID)
	}
}

func TestReadTailZeroOrNegativeMaxReadsAll(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, logging.New("error", "json", nil))
	defer store.Close()

	for i := 0; i < 6; i++ {
		if err := store.Append(ctx, "s", Message{ID: strconv.Itoa(i), Role: RoleUser, Content: "x"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// maxMessages <= 0 means "no cap" — equivalent to a full read.
	msgs, err := store.ReadTail(ctx, "s", 0)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if len(msgs) != 6 {
		t.Fatalf("expected all 6 messages with no cap, got %d", len(msgs))
	}
}

func TestReadTailMissingSessionReturnsNil(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, logging.New("error", "json", nil))
	defer store.Close()

	msgs, err := store.ReadTail(ctx, "nope", 5)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil for missing session, got %d messages", len(msgs))
	}
}
