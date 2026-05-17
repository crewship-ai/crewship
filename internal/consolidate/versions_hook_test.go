package consolidate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestConsolidate_AppendRules_RecordsVersion(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db) // already in approve_test.go's neighbour
	// Add v90 schema directly (sql.DB shim because the helper above
	// is intentionally minimal to keep test friction low).
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS memory_versions (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    path         TEXT NOT NULL,
    tier         TEXT NOT NULL CHECK (tier IN ('agent','crew','workspace','pins','learned')),
    sha256       TEXT NOT NULL,
    bytes        INTEGER NOT NULL,
    written_at   TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    written_by   TEXT,
    parent_sha   TEXT,
    payload_ref  TEXT NOT NULL
);`); err != nil {
		t.Fatalf("v90 schema: %v", err)
	}

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"version me","action":"act","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.8}]`
	tmp := t.TempDir()
	blobDir := filepath.Join(tmp, "versions")
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger()}
	cfg := Config{
		WorkspaceID: "ws_test",
		CrewID:      "crew_test",
		Since:       time.Hour,
		MinEntries:  10,
		OutputDir:   tmp,
		BlobRoot:    blobDir,
	}
	res, err := c.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RulesAppended != 1 {
		t.Fatalf("RulesAppended = %d, want 1", res.RulesAppended)
	}

	// memory_versions row landed with tier=learned and the right path shape.
	var sha, tier, path string
	var bytes int
	if err := db.QueryRow(`
SELECT sha256, tier, path, bytes FROM memory_versions
WHERE workspace_id = 'ws_test' AND tier = 'learned'
ORDER BY written_at DESC LIMIT 1`).Scan(&sha, &tier, &path, &bytes); err != nil {
		t.Fatalf("read memory_versions: %v", err)
	}
	if tier != "learned" {
		t.Errorf("tier = %q, want learned", tier)
	}
	if !strings.HasPrefix(path, "crew:crew_test/learned-") {
		t.Errorf("path = %q, want prefix crew:crew_test/learned-", path)
	}

	// Blob lives at {blobDir}/{sha[:2]}/{sha} with the canonical content.
	blobPath := filepath.Join(blobDir, sha[:2], sha)
	blob, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	// SHA matches the blob bytes (no off-by-one on what was recorded).
	got := sha256.Sum256(blob)
	if hex.EncodeToString(got[:]) != sha {
		t.Errorf("recorded sha %q != actual blob sha %x", sha, got)
	}
	if bytes != len(blob) {
		t.Errorf("bytes = %d, blob len = %d", bytes, len(blob))
	}
	if !strings.Contains(string(blob), "version me") {
		t.Errorf("blob missing rule body: %q", blob)
	}
}

func TestConsolidate_NoBlobRoot_SkipsVersioning(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS memory_versions (id TEXT PRIMARY KEY, workspace_id TEXT, path TEXT, tier TEXT, sha256 TEXT, bytes INTEGER, written_at TEXT, written_by TEXT, parent_sha TEXT, payload_ref TEXT NOT NULL)`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"x","action":"y","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.5}]`
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger()}
	cfg := Config{
		WorkspaceID: "ws_test", CrewID: "crew_test", Since: time.Hour,
		MinEntries: 10, OutputDir: t.TempDir(), // no BlobRoot
	}
	if _, err := c.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("without BlobRoot, no version rows should be recorded; got %d", count)
	}
}

func TestCanonicalAuditPath(t *testing.T) {
	cases := []struct {
		crew, file, want string
	}{
		{"crew_a", "learned-2026-05-17.md", "crew:crew_a/learned-2026-05-17.md"},
		{"crew_b", "pins.md", "crew:crew_b/pins.md"},
		{"", "orphan.md", "orphan.md"}, // missing crew degrades cleanly
	}
	for _, c := range cases {
		got := canonicalAuditPath(c.crew, c.file)
		if got != c.want {
			t.Errorf("canonicalAuditPath(%q,%q) = %q, want %q", c.crew, c.file, got, c.want)
		}
	}
}
