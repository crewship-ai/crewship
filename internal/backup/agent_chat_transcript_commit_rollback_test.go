package backup

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/quota"
)

type transcriptRollbackRuntime struct {
	snapshotProbe
	beforeImport func()
}

func (r *transcriptRollbackRuntime) ImportQuotaVolume(ctx context.Context, key quota.Key, size int64, src io.Reader) error {
	if r.beforeImport != nil {
		r.beforeImport()
	}
	return r.snapshotProbe.ImportQuotaVolume(ctx, key, size, src)
}

func (*transcriptRollbackRuntime) RemoveQuotaVolume(context.Context, quota.Key) error { return nil }

func TestRestoreBackupTranscriptRollbackAfterActualCommitFailure(t *testing.T) {
	source := openMigratedDBCov(t)
	workspace, crew := seedCovWorkspace(t, source, "transcript-commit-rollback")
	const agent = "a_cov_transcript-commit-rollback"
	const chat = "commit-rollback-chat"
	body := `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`
	if _, err := source.Exec(`UPDATE crews SET services_json=? WHERE id=?`, body, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version,updated_at) VALUES('transcript-snapshot-intent',?,'database','stopped',1,'2026-10-14T10:00:00Z')`, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO chats(id,workspace_id,agent_id,title) VALUES(?,?,?,'Source title')`, chat, workspace, agent); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sourceRoot := t.TempDir()
	sourceStore := conversation.NewStore(sourceRoot, logger)
	if err := sourceStore.Append(t.Context(), chat, conversation.Message{ID: "source-message", Role: conversation.RoleAssistant, Content: "Restored source"}); err != nil {
		t.Fatal(err)
	}
	sourceStore.Close()
	runtime := &transcriptRollbackRuntime{}
	bundle, err := CreateBackup(t.Context(), source, CreateOptions{Scope: ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Passphrase: "synthetic-commit-rollback-passphrase", Actor: covAdminActor(), ConversationRoot: sourceRoot, ServiceSnapshots: runtime})
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDBCov(t)
	seedCovWorkspace(t, target, "transcript-commit-rollback")
	if _, err := target.Exec(`INSERT INTO chats(id,workspace_id,agent_id,title) VALUES(?,?,?,'Original target title')`, chat, workspace, agent); err != nil {
		t.Fatal(err)
	}
	targetRoot := t.TempDir()
	targetStore := conversation.NewStore(targetRoot, logger)
	defer targetStore.Close()
	if err := targetStore.Append(t.Context(), chat, conversation.Message{ID: "old-message", Role: conversation.RoleUser, Content: "Original target"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(targetRoot, "conversations", chat+".jsonl")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The final fence renewal is after RestoreDumpTxHooks' explicit FK check.
	// This violation therefore reaches COMMIT, after PreCommit has published
	// transcript replacements, rather than failing an earlier restore phase.
	if _, err := target.Exec(`CREATE TABLE transcript_commit_poison(id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED);
 CREATE TRIGGER transcript_commit_failure AFTER UPDATE OF producer_until ON service_backup_fences
 WHEN NEW.operation='restore'
 BEGIN INSERT INTO transcript_commit_poison VALUES('missing-commit-user'); END;`); err != nil {
		t.Fatal(err)
	}
	released := false
	fenced := false
	runtime.beforeImport = func() {
		if fenced {
			t.Error("slow quota import ran while chats were globally fenced")
		}
	}
	lock := func(ctx context.Context) (func(), error) {
		fenced = true
		release, err := targetStore.LockTranscripts(ctx)
		if err != nil {
			return nil, err
		}
		return func() {
			released = true
			fenced = false
			current, readErr := os.ReadFile(path)
			info, statErr := os.Stat(path)
			if readErr != nil || statErr != nil || !bytes.Equal(current, original) || !os.SameFile(originalInfo, info) {
				t.Errorf("release exposed unrolled-back transcript: read=%v stat=%v body=%s", readErr, statErr, current)
			}
			release()
		}, nil
	}
	_, err = RestoreBackup(t.Context(), target, RestoreOptions{Path: bundle.Path, Passphrase: "synthetic-commit-rollback-passphrase", Actor: covAdminActor(), ConversationRoot: targetRoot, ServiceSnapshots: runtime, Replace: true, LockTranscripts: lock})
	if err == nil || !strings.Contains(err.Error(), "commit restore tx") {
		t.Fatalf("did not reach actual failed COMMIT after publication: %v", err)
	}
	if !released {
		t.Fatal("transcript lock not released")
	}
	var title string
	if err := target.QueryRow(`SELECT title FROM chats WHERE id=?`, chat).Scan(&title); err != nil || title != "Original target title" {
		t.Fatalf("DB restore not rolled back: %s %v", title, err)
	}
	for _, table := range []string{"service_backup_fences", "transcript_commit_poison"} {
		var count int
		if err := target.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("uncommitted %s survived: %d %v", table, count, err)
		}
	}
	if err := targetStore.Append(t.Context(), chat, conversation.Message{ID: "after-rollback", Role: conversation.RoleUser, Content: "Append after rollback"}); err != nil {
		t.Fatal(err)
	}
	messages, err := targetStore.Read(t.Context(), chat, 0, 0)
	if err != nil || len(messages) != 2 || messages[0].ID != "old-message" || messages[1].ID != "after-rollback" {
		t.Fatalf("append cache not recovered: %+v %v", messages, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(targetRoot, "conversations", ".restore-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("restore stages leaked: %v %v", leftovers, err)
	}
}
