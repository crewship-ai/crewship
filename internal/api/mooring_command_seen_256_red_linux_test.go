//go:build linux && wordcraft_c1_integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Expected RED on8789ef07f: checkpoint.seen survives a real host/helper cold
// restart and still refuses a successor after256 operations. This isolates the
// native seen bound from the independent64MiB SQL retained-checkpoint quota.
// Confirm is the trusted test committer; no SQL/Saved/auth/browser claim here.
func TestC1Seen256CheckpointRemainsWritableAfterColdRestart(t *testing.T) {
	ctx := t.Context()
	cfg := actualCommandNativeConfig(t, "seen-capacity-native")
	p, err := startCommandNativePort(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	snapshot, err := p.InitialSnapshot(ctx, "seen-capacity-document", "seen-generation-one")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := p.Projection(ctx)
	if err != nil || len(projection.IDs) != 1 {
		t.Fatal("init", err)
	}
	a := commandHostAdmission{Participant: "human:capacity", Author: "capacity", TransportParticipant: "capacity-source", Epoch: 1, Writer: true, Time: "2001-01-01T00:00:00Z"}
	text := ""
	submit := func(port *commandNativePort, op, addition string) error {
		shown, e := port.Projection(ctx)
		if e != nil {
			return e
		}
		raw, e := json.Marshal(map[string]any{"op_id": shown.Generation + ":" + op, "generation": shown.Generation, "participant": a.Participant, "base_revision": snapshot.Revision, "command": "text.insert", "params": map[string]any{"text": addition}, "target": map[string]any{"block_id": shown.IDs[0], "unit": "utf8_bytes", "start": len(text), "end": len(text)}})
		if e != nil {
			return e
		}
		candidate, e := port.Stage(ctx, a, snapshot, raw)
		if e != nil {
			return e
		}
		if e = port.Confirm(ctx, candidate, a); e != nil {
			return e
		}
		snapshot = commandSQLSnapshot{Revision: candidate.Revision, Checkpoint: candidate.Checkpoint}
		text += addition
		return nil
	}
	for i := 1; i <= 256; i++ {
		if err = submit(p, fmt.Sprintf("seed-%03d", i), "A"); err != nil {
			t.Fatalf("control seed%d: %v (this is not the selected seen257 failure)", i, err)
		}
	}
	beforeErr := submit(p, "before-restart", "B")
	if beforeErr != nil && !errors.Is(beforeErr, errCommandRefused) {
		t.Fatalf("unresolved pre-restart outcome: %v; no successor submitted", beforeErr)
	}
	var header struct {
		Seen map[string]json.RawMessage `json:"seen"`
	}
	if err = json.Unmarshal(snapshot.Checkpoint, &header); err != nil {
		t.Fatal(err)
	}
	t.Logf("pre-restart revision=%d persisted_seen=%d next_error=%v", snapshot.Revision, len(header.Seen), beforeErr)
	path := filepath.Join(t.TempDir(), "confirmed-checkpoint.json")
	if err = os.WriteFile(path, snapshot.Checkpoint, 0600); err != nil {
		t.Fatal(err)
	}
	p.Close()
	q, err := startCommandNativePort(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.RestoreConfirmed(ctx, commandSQLSnapshot{Revision: snapshot.Revision, Checkpoint: restored}); err != nil {
		t.Fatal("cold restore", err)
	}
	if v, e := q.Projection(ctx); e != nil || actualNativeText(t, v) != text || v.Revision != snapshot.Revision {
		t.Fatal("cold restore lost confirmed P", e)
	}
	afterErr := submit(q, "new-intent-after-restart", "C")
	if afterErr != nil {
		t.Fatalf("seen limit remains blocked after cold restart: before=%v after=%v revision=%d seen=%d", beforeErr, afterErr, snapshot.Revision, len(header.Seen))
	}
}
