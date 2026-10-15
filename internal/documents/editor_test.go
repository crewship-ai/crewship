package documents

import (
	"errors"
	"testing"
)

func TestDocumentEngineRecoveryGenerationAndExactRetry(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	old := acquire(t, s)
	if e := s.RecoverEngine(ctx, "node-boot-identity-first"); e != nil {
		t.Fatal(e)
	}
	current, e := s.Read(ctx, key, allow)
	if e != nil || current.Generation != 1 {
		t.Fatal(current, e)
	}
	if _, e = s.Renew(ctx, key, old, allow); !errors.Is(e, ErrFence) {
		t.Fatalf("oldlease=%v", e)
	}
	live := acquire(t, s)
	if e = s.RecoverEngine(ctx, "node-boot-identity-first"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Renew(ctx, key, live, allow); e != nil {
		t.Fatalf("recovery retry fenced new writer: %v", e)
	}
	zero := int64(0)
	stale := commit(live, "stale-generation", 0)
	stale.IfGeneration = &zero
	if _, e = s.Commit(ctx, key, stale, allow); !errors.Is(e, ErrConflict) {
		t.Fatalf("same-revision stalegeneration=%v", e)
	}
	if _, e = s.CheckpointRequestAtGeneration(ctx, key, live, "stale-checkpoint", "fingerprint", []byte("baseline"), 0, &zero, allow); !errors.Is(e, ErrConflict) {
		t.Fatalf("stalecheckpoint=%v", e)
	}
	if _, e = s.Comment(ctx, key, "stale-comment", "comment-fp", "text", []byte(`{"start":"old"}`), 0, &zero, allow); !errors.Is(e, ErrConflict) {
		t.Fatalf("stalecomment=%v", e)
	}
	one := int64(1)
	if _, e = s.Comment(ctx, key, "comment", "comment-fp", "text", []byte(`{"start":"preserved-relative-position"}`), 0, &one, allow); e != nil {
		t.Fatal(e)
	}
	if e = s.RecoverEngine(ctx, "node-boot-identity-second"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ResolveComment(ctx, key, "resolve", "resolve-fp", "comment", 1, 0, true, &one, allow); !errors.Is(e, ErrConflict) {
		t.Fatalf("staleresolution=%v", e)
	}
	comments, e := s.Comments(ctx, key, allow)
	if e != nil || len(comments) != 1 || comments[0].Resolved {
		t.Fatal(comments, e)
	}
	// An exact already-durable comment replay still succeeds with current authority.
	if _, e = s.Comment(ctx, key, "comment", "comment-fp", "text", []byte(`{"start":"preserved-relative-position"}`), 0, &one, allow); e != nil {
		t.Fatal(e)
	}
}

func TestDocumentSchemaUpgradeKeepsStableGeneration(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	lease := acquire(t, s)
	c := commit(lease, "upgrade", 0)
	c.Schema = 2
	zero := int64(0)
	c.IfGeneration = &zero
	if _, e := s.Commit(ctx, key, c, allow); e != nil {
		t.Fatal(e)
	}
	current, e := s.Read(ctx, key, allow)
	if e != nil || current.Schema != 2 || current.Generation != 0 {
		t.Fatal(current, e)
	}
	if e = s.Release(ctx, key, lease, allow); e != nil {
		t.Fatal(e)
	}
	replacement := acquire(t, s)
	current, e = s.Read(ctx, key, allow)
	if e != nil || current.Generation != 0 || replacement.Writer == lease.Writer {
		t.Fatal(current, e)
	}
	c = commit(replacement, "downgrade", 1)
	if _, e = s.Commit(ctx, key, c, allow); !errors.Is(e, ErrSchema) {
		t.Fatalf("silentdowngrade=%v", e)
	}
}

func TestDocumentInitializeCannotOverwriteExistingPage(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	if _, e := s.InitRequest(ctx, key, "different-init", "new-payload", 2, []byte("replacement"), projection, allow); !errors.Is(e, ErrConflict) {
		t.Fatalf("unexpected replacement initialization: %v", e)
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || string(v.Snapshot) != "baseline" || v.HeadRev != 0 {
		t.Fatal(v, e)
	}
}

func TestDocumentSchemaTwoCheckpointExactRetryAfterEngineRecovery(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	k := Key{WorkspaceID: key.WorkspaceID, PageID: "page-c"}
	if _, err := s.InitRequest(ctx, k, "schema-two-init", "init-fingerprint", 2, []byte("schema-two-snapshot"), projection, allow); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Acquire(ctx, k, "first-engine", allow)
	if err != nil {
		t.Fatal(err)
	}
	generation := int64(0)
	original, err := s.CheckpointRequestAtGeneration(ctx, k, lease, "schema-two-checkpoint", "checkpoint-fingerprint", []byte("schema-two-snapshot"), 0, &generation, allow)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverEngine(ctx, "replacement-engine-boot"); err != nil {
		t.Fatal(err)
	}
	// Call the store directly: the Node service's receipt lookup must not mask
	// a schema-two rejection at the durable checkpoint retry boundary.
	retried, err := s.CheckpointRequestAtGeneration(ctx, k, lease, "schema-two-checkpoint", "checkpoint-fingerprint", []byte("schema-two-snapshot"), 0, &generation, allow)
	if err != nil {
		t.Fatalf("exact confirmed checkpoint retry rejected after recovery: %v", err)
	}
	if retried.ID != original.ID || retried.Rev != original.Rev || retried.Schema != 2 || string(retried.Snapshot) != string(original.Snapshot) {
		t.Fatalf("checkpoint identity or payload changed: original=%+v retry=%+v", original, retried)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE workspace_id=? AND page_id=? AND kind='checkpoint'`, k.WorkspaceID, k.PageID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("checkpoint retry changed durable receipt count: %d, %v", count, err)
	}
	if _, err := s.CheckpointRequestAtGeneration(ctx, k, lease, "schema-two-checkpoint", "changed-fingerprint", []byte("schema-two-snapshot"), 0, &generation, allow); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("changed request reused confirmed checkpoint ID: %v", err)
	}
}
