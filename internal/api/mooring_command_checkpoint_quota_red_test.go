package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Expected RED on8789ef07f: retained snapshots reach64MiB around mutation50.
// This is the existing SQL/current-policy port fixture, NOT native/renderer
// qualification. Real SQL quota/transaction/candidate validation is exercised.
func TestC1CheckpointRetentionKeepsWritingBeyondFiftyLargeChanges(t *testing.T) {
	r := newCommandSQLRig(t, true)
	payload := strings.Repeat("A", 1280<<10)
	r.p.mutateCandidate = func(c *commandSQLCandidate) {
		var cp map[string]any
		if err := json.Unmarshal(c.Checkpoint, &cp); err != nil {
			t.Fatal(err)
		}
		cp["p"].(map[string]any)["native"] = map[string]any{"body": []any{map[string]any{"kind": "para", "text": payload, "runs": []any{map[string]any{"len": len(payload)}}}}}
		var err error
		c.Checkpoint, err = json.Marshal(cp)
		if err != nil {
			t.Fatal(err)
		}
		c.Digest = commandDigest(c.Checkpoint)
	}
	for i := 1; i <= 64; i++ {
		_, err := r.s.Submit(r.request(), r.a, r.actor(), r.input(fmt.Sprintf("large-%03d", i), uint64(i-1), "A"))
		if err != nil {
			var count, total int64
			if e := r.f.r.db.QueryRow(`SELECT count(*),coalesce(sum(decoded_bytes),0) FROM mooring_collab_checkpoints WHERE page_id='admission-page'`).Scan(&count, &total); e != nil {
				t.Fatal(e)
			}
			t.Fatalf("checkpoint retention blocked mutation%d/64: %v; retained_checkpoints=%d retained_bytes=%d; expected bounded retention to keep accepting writes", i, err, count, total)
		}
	}
}
