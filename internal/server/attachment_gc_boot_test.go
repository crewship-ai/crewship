package server

// Boot-wiring test for the attachment blob collector (#1768 item 7, #1791
// review finding 1).
//
// The collector itself is unit-tested in internal/api/attachments_gc_test.go.
// What this pins is the production wiring: Server.Start must run it, bound to
// the run context. That distinction is the whole finding — the sweep existed,
// was correct, and was reachable only from one handler's error arm, so on a
// healthy instance it never executed and every blob orphaned by an FK cascade
// stayed on disk for the life of the deployment.
//
// The observable is end-to-end: a blob on disk that no row names disappears
// after boot, with no HTTP call ever made.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
)

func TestStart_AttachmentBlobGC_ReclaimsAnOrphanedBlob(t *testing.T) {
	db := openTestDB(t)

	// A workspace with no attachment rows at all — the state a wiped crew or a
	// restored-without-rows tenant leaves behind.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_attgc', 'Attachment GC', 'attachment-gc')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	storage := t.TempDir()
	content := []byte("orphaned by a cascade nobody watched\n")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	blob := filepath.Join(storage, "attachments", "ws_attgc", sha[:2], sha)
	if err := os.MkdirAll(filepath.Dir(blob), 0o750); err != nil {
		t.Fatalf("mkdir blob dir: %v", err)
	}
	if err := os.WriteFile(blob, content, 0o640); err != nil {
		t.Fatalf("write blob: %v", err)
	}

	// Unix sockets have a tight path-length limit; shortSocketPath keeps this
	// under it and asserts it, rather than trusting a hand-rolled short name
	// (see testMaxSocketPath in socket_test.go).
	sockPath := shortSocketPath(t, "i.sock")

	cfg := silentCfg()
	cfg.IPC.SocketPath = sockPath
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 0 // ephemeral — the HTTP surface is irrelevant here
	cfg.Storage.BasePath = storage

	s := New(cfg, logging.New("error", "json", nil), &Deps{DB: db})
	t.Cleanup(s.StopBackground)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// The collector's first pass runs as soon as it starts, but the clock here
	// begins at go s.Start(ctx) — migrations and every other background worker
	// come first, and on a loaded machine that is seconds. The loop breaks the
	// instant the blob goes, so the wide window only costs wall-clock when the
	// collector is genuinely not wired.
	deadline := time.Now().Add(30 * time.Second)
	collected := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(blob); os.IsNotExist(err) {
			collected = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("server did not shut down within 10s after ctx cancel")
	}

	if !collected {
		t.Fatalf("an unreferenced attachment blob survived server boot — StartAttachmentBlobGC "+
			"is not wired into Server.Start, so nothing on this instance ever reclaims blobs "+
			"whose rows a cascade removed (%s)", blob)
	}
}
