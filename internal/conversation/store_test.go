package conversation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
)

var ctx = context.Background()

func TestStoreAppendAndRead(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New("error", "json", nil)
	store := NewStore(dir, logger)
	defer store.Close()

	msg1 := Message{
		ID:        "msg_1",
		Role:      RoleUser,
		Content:   "Hello agent",
		Timestamp: time.Now().UTC(),
	}
	msg2 := Message{
		ID:        "msg_2",
		Role:      RoleAssistant,
		Content:   "Hello user",
		Timestamp: time.Now().UTC(),
	}

	if err := store.Append(ctx, "session-1", msg1); err != nil {
		t.Fatalf("append msg1: %v", err)
	}
	if err := store.Append(ctx, "session-1", msg2); err != nil {
		t.Fatalf("append msg2: %v", err)
	}

	messages, err := store.Read(ctx, "session-1", 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != RoleUser {
		t.Errorf("expected user role, got %s", messages[0].Role)
	}
	if messages[1].Content != "Hello user" {
		t.Errorf("expected 'Hello user', got %q", messages[1].Content)
	}
}

func TestStoreReadOffset(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New("error", "json", nil)
	store := NewStore(dir, logger)
	defer store.Close()

	for i := 0; i < 5; i++ {
		if err := store.Append(ctx, "session-2", Message{
			ID:      "msg",
			Role:    RoleUser,
			Content: "message",
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	messages, err := store.Read(ctx, "session-2", 3, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(messages) != 2 {
		t.Errorf("expected 2 messages (offset 3 from 5), got %d", len(messages))
	}
}

func TestStoreReadLimit(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New("error", "json", nil)
	store := NewStore(dir, logger)
	defer store.Close()

	for i := 0; i < 10; i++ {
		if err := store.Append(ctx, "session-3", Message{ID: "m", Role: RoleUser, Content: "x"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	messages, err := store.Read(ctx, "session-3", 0, 3)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(messages))
	}
}

func TestStoreReadNonExistent(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New("error", "json", nil)
	store := NewStore(dir, logger)
	defer store.Close()

	messages, err := store.Read(ctx, "nonexistent", 0, 0)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if messages != nil {
		t.Errorf("expected nil messages, got %v", messages)
	}
}

func TestStoreInvalidID(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New("error", "json", nil)
	store := NewStore(dir, logger)
	defer store.Close()

	err := store.Append(ctx, "../escape", Message{ID: "m", Role: RoleUser, Content: "x"})
	if err == nil {
		t.Error("expected error for path traversal ID")
	}

	_, err = store.Read(ctx, "foo/bar", 0, 0)
	if err == nil {
		t.Error("expected error for slash in ID")
	}
}

func TestStoreFileCreated(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New("error", "json", nil)
	store := NewStore(dir, logger)

	if err := store.Append(ctx, "test-session", Message{ID: "m", Role: RoleUser, Content: "hi"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	store.Close()

	path := filepath.Join(dir, "conversations", "test-session.jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected conversation file to exist")
	}
}

func TestStoreCancelledContext(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New("error", "json", nil)
	store := NewStore(dir, logger)
	defer store.Close()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.Append(cancelled, "session-x", Message{ID: "m", Role: RoleUser, Content: "x"})
	if err == nil {
		t.Error("expected error for cancelled context")
	}

	_, err = store.Read(cancelled, "session-x", 0, 0)
	if err == nil {
		t.Error("expected error for cancelled context on read")
	}
}
