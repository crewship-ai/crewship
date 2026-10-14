package conversation

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/logging"
)

func TestAppendReleasesTranscriptLockWhileMirrorWaitsForDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE conversation_messages(id TEXT,session_id TEXT,agent_id TEXT,role TEXT,content TEXT,tool_summary TEXT,ts TEXT,author_user_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir(), logging.New("error", "json", nil), WithDB(db))
	defer store.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	held, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	done := make(chan error, 1)
	go func() {
		done <- store.Append(ctx, "lock-session", Message{ID: "blocked-mirror", Role: RoleUser, Content: "Written before the mirror"})
	}()
	// WaitCount proves Append reached the exhausted pool, after its file write.
	for db.Stats().WaitCount == 0 {
		select {
		case err := <-done:
			t.Fatalf("append did not wait for DB: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	locked := make(chan func(), 1)
	go func() {
		release, err := store.LockTranscripts(ctx)
		if err != nil {
			locked <- nil
			return
		}
		locked <- release
	}()
	var release func()
	select {
	case release = <-locked:
		if release == nil {
			t.Fatal("transcript fence failed")
		}
	case <-time.After(time.Second):
		cancel()
		held.Close()
		if cleanup := <-locked; cleanup != nil {
			cleanup()
		}
		t.Fatal("mirror database wait held transcript mutex")
	}
	defer release()
	messages, err := store.Read(ctx, "lock-session", 0, 0)
	if err != nil || len(messages) != 1 {
		t.Fatalf("JSONL write missing: %+v %v", messages, err)
	}
	// The delayed SQL mirror can complete even while restore owns the file fence.
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("mirror completion waited for transcript mutex")
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_messages WHERE id='blocked-mirror'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("mirror not completed: %d %v", count, err)
	}
}

func TestSnapshotTranscriptsKeepsCachedDescriptorAndAppendResumes(t *testing.T) {
	store := NewStore(t.TempDir(), logging.New("error", "json", nil))
	defer store.Close()
	if err := store.Append(t.Context(), "snapshot-session", Message{ID: "before", Role: RoleUser}); err != nil {
		t.Fatal(err)
	}
	cached := store.files["snapshot-session"]
	release, err := store.SnapshotTranscripts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if store.files["snapshot-session"] != cached {
		release()
		t.Fatal("snapshot replaced cached writer")
	}
	if _, err := cached.Stat(); err != nil {
		release()
		t.Fatalf("snapshot closed cached writer: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- store.Append(t.Context(), "snapshot-session", Message{ID: "after", Role: RoleUser}) }()
	select {
	case err := <-done:
		release()
		t.Fatalf("append crossed snapshot fence: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("append did not resume")
	}
	if store.files["snapshot-session"] != cached {
		t.Fatal("append reopened a snapshot-closed writer")
	}
	messages, err := store.Read(t.Context(), "snapshot-session", 0, 0)
	if err != nil || len(messages) != 2 {
		t.Fatalf("snapshot append lost messages: %+v %v", messages, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if release, err := store.SnapshotTranscripts(cancelled); err == nil {
		release()
		t.Fatal("cancelled snapshot admitted")
	}
	if err := store.Append(t.Context(), "snapshot-session", Message{ID: "after-cancel", Role: RoleUser}); err != nil {
		t.Fatal(err)
	}
}
