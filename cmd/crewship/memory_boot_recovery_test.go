//go:build !clionly

package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func pendingBootMutation(t *testing.T, db *sql.DB, canonical, targetSHA string, targetBytes int) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `INSERT INTO memory_mutations
		(id,workspace_id,path,canonical_path,scope,tier,operation_id,request_sha256,op,
		 base_revision,base_sha256,new_revision,target_sha256,target_bytes,target_blob_ref,state,recorded_at)
		VALUES ('m1','ws1','agent:a/AGENT.md',?,'agent:a','AGENT','op1','request','replace',
		0,?,1,?,?,'blob','intent','2026-09-23T00:00:00Z')`,
		canonical, hashBootContent(nil), targetSHA, targetBytes)
	if err != nil {
		t.Fatal(err)
	}
}

func hashBootContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestRecoverMemoryBeforeServe_ConfirmsExistingTarget(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	root := t.TempDir()
	target := filepath.Join(root, "AGENT.md")
	content := []byte("recovered after restart\n")
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	pendingBootMutation(t, db, target, hashBootContent(content), len(content))
	if err := recoverMemoryBeforeServe(t.Context(), db, root, slog.Default()); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRowContext(t.Context(), `SELECT state FROM memory_mutations WHERE id='m1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "confirmed" {
		t.Fatalf("startup left mutation %q, want confirmed", state)
	}
}

func TestRecoverMemoryBeforeServe_RefusesMissingDurableBlob(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	root := t.TempDir()
	target := filepath.Join(root, "AGENT.md")
	pendingBootMutation(t, db, target, hashBootContent([]byte("missing blob\n")), len("missing blob\n"))
	err := recoverMemoryBeforeServe(t.Context(), db, root, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "recover pending memory mutations before serving") {
		t.Fatalf("startup accepted an unrecoverable intent: %v", err)
	}
	var state string
	if err := db.QueryRowContext(t.Context(), `SELECT state FROM memory_mutations WHERE id='m1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "intent" {
		t.Fatalf("failed recovery changed mutation state to %q", state)
	}
}
