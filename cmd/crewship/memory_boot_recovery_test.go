//go:build !clionly

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if err := recoverMemoryBeforeServe(t.Context(), db, root, root, slog.Default()); err != nil {
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
	err := recoverMemoryBeforeServe(t.Context(), db, root, root, slog.Default())
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

func TestStart_RefusesUnrecoverableMemoryBeforeServing(t *testing.T) {
	binary := buildCrewshipBinary(t)
	for _, tc := range []struct {
		name    string
		outside bool
	}{
		{name: "missing durable blob"},
		{name: "restored path outside current storage", outside: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			storageRoot := filepath.Join(root, "storage")
			if err := os.MkdirAll(storageRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "crewship.db")
			db := testutil.MigratedDBAt(t, path).DB
			content := []byte("missing\n")
			target := filepath.Join(storageRoot, "AGENT.md")
			if tc.outside {
				content = []byte("already on disk\n")
				target = filepath.Join(t.TempDir(), "AGENT.md")
				if err := os.WriteFile(target, content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			pendingBootMutation(t, db, target, hashBootContent(content), len(content))

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			configPath := filepath.Join(root, "server.yaml")
			config := fmt.Sprintf("server:\n  host: 127.0.0.1\n  port: %d\nstorage:\n  base_path: %s\n  memory_root: %s\n", port, storageRoot, filepath.Join(root, "memory"))
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "start", "--no-docker", "--config", configPath, "--db", path)
			child.Dir = root
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if strings.HasPrefix(key, "CREWSHIP_") || key == "DATABASE_URL" || key == "NEXTAUTH_SECRET" || strings.HasPrefix(key, "ENCRYPTION_KEY") {
					continue
				}
				child.Env = append(child.Env, entry)
			}
			child.Env = append(child.Env, "CREWSHIP_DATA_DIR="+root, "CREWSHIP_SKIP_SIDECAR=1")
			output, err := child.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("server did not refuse the pending mutation before serving: %v", ctx.Err())
			}
			if err == nil || !strings.Contains(string(output), "recover pending memory mutations before serving") {
				t.Fatalf("boot error = %v; expected memory recovery failure, output: %s", err, output)
			}
		})
	}
}
