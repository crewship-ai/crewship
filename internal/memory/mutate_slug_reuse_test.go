package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestMutate_InFlightWriteAcrossSlugReuse is #4862: a write of an agent that
// is deleted, and whose slug is freed and reused, WHILE the write is between
// two of its steps. The slug release (RekeyAgentScopeTx, the exact code the
// API runs in freeDeletedAgentSlug) moves the old agent's anchors and pending
// intents to "agent:<slug>_deleted_<id>/..." without taking the old agent's
// file lock, so the paused write resumes against a moved ledger.
//
// Whatever the old write then does — confirm under the archived key, or be
// refused — it must never write under "agent:<slug>/...", which now belongs to
// the new agent: an anchor there that describes the old agent's file makes the
// new agent's every write fail the drift check, or overwrites the anchor of
// the new agent's own last write.
func TestMutate_InFlightWriteAcrossSlugReuse(t *testing.T) {
	const archived = "alice_deleted_ag1"
	archivedAudit := "agent:" + archived + "/AGENT.md"

	for _, step := range []string{"before_intent", "after_intent", "after_rename", "before_confirm"} {
		for _, newAgentWrites := range []bool{false, true} {
			name := step + "/new agent idle"
			if newAgentWrites {
				name = step + "/new agent writes"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				f := newMutateFixture(t) // the old agent: its crew's AGENT.md, key agent:alice/AGENT.md
				f.mustMutate(t, f.req("op_seed", OpReplace, "old seed\n"))

				// The new agent reuses the slug in another crew: same ledger
				// key, different canonical file, so a different file lock.
				newDir := t.TempDir()
				newPath := filepath.Join(newDir, "AGENT.md")
				t.Cleanup(func() { _ = os.Remove(newPath + ".lock") })
				newReq := func(opID string, op MutateOp, content string) MutateRequest {
					r := f.req(opID, op, content)
					r.ActorID = "alice2"
					r.Path = newPath
					return r
				}

				fired := false
				inflight := f.req("op_inflight", OpAppend, "old secret\n")
				inflight.testHook = func(s string) error {
					if s != step || fired {
						return nil
					}
					fired = true
					// Agent deleted, slug freed (anchors and pending intents
					// move to the tombstone key), new agent created.
					tx, err := f.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatalf("begin release: %v", err)
					}
					if err := RekeyAgentScopeTx(ctx, tx, "ws_test", "alice", archived); err != nil {
						_ = tx.Rollback()
						t.Fatalf("release slug: %v", err)
					}
					if err := tx.Commit(); err != nil {
						t.Fatalf("commit release: %v", err)
					}
					if newAgentWrites {
						if _, err := Mutate(ctx, f.db, newReq("op_new", OpReplace, "new fact\n")); err != nil {
							t.Fatalf("new agent's first write while the old one is in flight: %v", err)
						}
					}
					return nil
				}

				_, oldErr := f.mutate(t, inflight)
				if !fired {
					t.Fatalf("hook step %q never fired", step)
				}
				if oldErr != nil && !errors.Is(oldErr, ErrMemoryConflict) {
					t.Fatalf("old in-flight write: want success or ErrMemoryConflict, got %v", oldErr)
				}

				// 1. The new agent's key carries only the new agent's state.
				newKey, err := loadAnchor(ctx, f.db, "ws_test", f.auditPath)
				if err != nil {
					t.Fatal(err)
				}
				if newAgentWrites {
					if newKey == nil || newKey.revision != 1 || newKey.contentSHA != hashOf("new fact\n") {
						t.Fatalf("new agent's anchor under %s = %+v, want rev 1 of its own write (old write err=%v)",
							f.auditPath, newKey, oldErr)
					}
				} else if newKey != nil {
					t.Fatalf("the deleted agent's in-flight write recreated an anchor under the reused key %s: rev %d sha %s (old write err=%v)",
						f.auditPath, newKey.revision, short(newKey.contentSHA), oldErr)
				}
				var pending int
				if err := f.db.QueryRowContext(ctx,
					`SELECT COUNT(*) FROM memory_mutations WHERE workspace_id = ? AND path = ? AND state IN (?, ?)`,
					"ws_test", f.auditPath, mutationStateIntent, mutationStateRenamed).Scan(&pending); err != nil {
					t.Fatal(err)
				}
				if pending != 0 {
					t.Fatalf("%d unconfirmed old-agent rows left under the reused key", pending)
				}

				// 2. The old write landed under the archived key or not at all.
				arch, err := loadAnchor(ctx, f.db, "ws_test", archivedAudit)
				if err != nil {
					t.Fatal(err)
				}
				if oldErr == nil {
					if arch == nil || arch.revision != 2 || arch.contentSHA != hashOf("old seed\nold secret\n") {
						t.Fatalf("confirmed old write: archived anchor = %+v, want rev 2 of the old file", arch)
					}
					var state, path string
					if err := f.db.QueryRowContext(ctx,
						`SELECT state, path FROM memory_mutations WHERE operation_id = ?`, "op_inflight").Scan(&state, &path); err != nil {
						t.Fatal(err)
					}
					if state != mutationStateConfirmed || path != archivedAudit {
						t.Fatalf("old mutation row = (%s, %s), want confirmed under %s", state, path, archivedAudit)
					}
				} else if arch == nil || arch.revision != 1 || arch.contentSHA != hashOf("old seed\n") {
					t.Fatalf("refused old write: archived anchor = %+v, want the moved rev 1 untouched", arch)
				}

				// 3. The new agent keeps writing: no drift, no replay.
				res, err := Mutate(ctx, f.db, newReq("op_new_2", OpAppend, "more\n"))
				if err != nil {
					t.Fatalf("new agent's write after the old one settled: %v", err)
				}
				if res.Recovered != 0 {
					t.Fatalf("new agent's write replayed %d old-agent intents", res.Recovered)
				}
				want := "more\n"
				if newAgentWrites {
					want = "new fact\nmore\n"
				}
				if b, _ := os.ReadFile(newPath); string(b) != want {
					t.Fatalf("new agent's file = %q, want %q", b, want)
				}
			})
		}
	}
}
