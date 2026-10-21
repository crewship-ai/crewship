package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
)

type blockingTranscriptSink struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	bytes.Buffer
}

func (s *blockingTranscriptSink) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.Buffer.Write(p)
}

func TestPinnedTranscriptAllowsAppendDuringCompressionAndExcludesLaterBytes(t *testing.T) {
	storage := t.TempDir()
	store := conversation.NewStore(storage, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer store.Close()
	noise := make([]byte, 1<<20)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(t.Context(), "pinned-chat", conversation.Message{ID: "original", Role: conversation.RoleUser, Content: hex.EncodeToString(noise)}); err != nil {
		t.Fatal(err)
	}
	dump := &DBDump{Tables: map[string][]map[string]any{"chats": {{"id": "pinned-chat", "workspace_id": "ws", "message_count": 1}}}}
	pinned, err := pinAgentChatTranscripts(t.Context(), storage, dump, store.LockTranscripts)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	sink := &blockingTranscriptSink{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(sink.release) }) }
	defer unblock()
	writer, err := NewTarZstWriter(sink)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := pinned.write(t.Context(), writer, dump, nonZeroTime())
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		finished <- err
	}()
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("compression did not start")
	}
	appended := make(chan error, 1)
	go func() {
		appended <- store.Append(t.Context(), "pinned-chat", conversation.Message{ID: "late-message", Role: conversation.RoleAssistant, Content: "LATE_APPEND_MUST_NOT_ENTER_SNAPSHOT"})
	}()
	select {
	case err := <-appended:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Append blocked by compression")
	}
	unblock()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	archive, err := NewTarZstReader(bytes.NewReader(sink.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	header, err := archive.Next()
	if err != nil || header.Name != "agent-chats/pinned-chat.jsonl" {
		t.Fatalf("header %+v %v", header, err)
	}
	content, err := io.ReadAll(archive)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "late-message") || !strings.Contains(string(content), "original") {
		t.Fatal("snapshot did not respect pinned byte length")
	}
	// Closing the snapshot releases all pinned descriptors, even if callers
	// already closed it on a successful path.
	fd := pinned.files[0].file
	pinned.Close()
	pinned.Close()
	if _, err := fd.Stat(); err == nil {
		t.Fatal("snapshot leaked descriptor")
	}
}

func TestTranscriptCollectionSkipsCorruptionPreservesValidRawFields(t *testing.T) {
	storage := t.TempDir()
	dir := filepath.Join(storage, "conversations")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	validFirst := "{\"session_id\":\"allowed\",\"id\":\"first\",\"unknown_field\":{\"n\":9007199254740993},\"run_id\":\"run-1\"}\n"
	validLast := "{\"id\":\"last\", \"session_id\":\"allowed\",\"parts\":[{\"type\":\"text\",\"content\":\"kept\"}]}\n"
	source := validFirst + "not json\n" + "{\"session_id\":\"foreign\",\"content\":\"FOREIGN_SECRET\"}\n" + validLast + "{\"session_id\":\"allowed\",\"content\":"
	if err := os.WriteFile(filepath.Join(dir, "allowed.jsonl"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	dump := &DBDump{Tables: map[string][]map[string]any{"chats": {{"id": "allowed", "workspace_id": "ws", "message_count": 5}}}}
	pinned, err := pinAgentChatTranscripts(t.Context(), storage, dump, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	var sink bytes.Buffer
	writer, err := NewTarZstWriter(&sink)
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := pinned.write(t.Context(), writer, dump, nonZeroTime())
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if len(incomplete) != 1 || incomplete[0].Count != 3 || incomplete[0].Workspace != "ws" || !strings.Contains(incomplete[0].Detail, "allowed") {
		t.Fatalf("incomplete %+v", incomplete)
	}
	archive, err := NewTarZstReader(bytes.NewReader(sink.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err = archive.Next(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(archive)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != validFirst+validLast {
		t.Fatalf("valid fields changed or foreign content leaked: %s", raw)
	}
}

func TestTranscriptPinFailureReleasesFence(t *testing.T) {
	storage := t.TempDir()
	dir := filepath.Join(storage, "conversations")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "first.jsonl"), []byte("{\"session_id\":\"first\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("first.jsonl", filepath.Join(dir, "unsafe.jsonl")); err != nil {
		t.Fatal(err)
	}
	dump := &DBDump{Tables: map[string][]map[string]any{"chats": {{"id": "first"}, {"id": "unsafe"}}}}
	releases := 0
	pinned, err := pinAgentChatTranscripts(t.Context(), storage, dump, func(_ context.Context) (func(), error) { return func() { releases++ }, nil })
	if err == nil || pinned != nil || releases != 1 {
		t.Fatalf("failed pin %+v %v releases=%d", pinned, err, releases)
	}
}
