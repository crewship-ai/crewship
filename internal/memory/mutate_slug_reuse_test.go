package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMutate_InFlightWriteAcrossSlugReuse is #4862: a write of an agent that
// is deleted, and whose slug is freed and reused, WHILE the write is between
// two of its steps. The file lock is per canonical file and does not exclude
// the agent delete or the slug release, so the paused write resumes against a
// ledger whose "agent:<slug>/..." key now belongs to another agent.
//
// The release mirrors freeDeletedAgentSlug (internal/api): the same
// RekeyAgentScopeTx and the tombstone rename, in one transaction.
//
// Whatever the old write then does — confirm under the archived key, or be
// refused — it must never write under "agent:<slug>/...": an anchor there that
// describes the old agent's file makes the new agent's writes fail the drift
// check, or overwrites the anchor of the new agent's own last write. That
// holds whether or not the old agent had an anchor to move, and whether or not
// the new agent's state happens to equal the old one.
func TestMutate_InFlightWriteAcrossSlugReuse(t *testing.T) {
	const (
		oldID    = "ag-old"
		newID    = "ag-new"
		archived = "alice_deleted_" + oldID
	)
	archivedAudit := "agent:" + archived + "/AGENT.md"

	type tc struct {
		name     string
		seedOld  string // old agent's prior write; "" = this is its first write
		step     string
		newCrew  string // crew of the agent that reuses the slug
		newWrite string // the new agent's first write during the pause; "" = idle
	}
	var cases []tc
	for _, step := range []string{"before_intent", "after_intent", "after_rename", "before_confirm"} {
		cases = append(cases,
			tc{name: step + "/old anchor/new agent idle", seedOld: "old seed\n", step: step, newCrew: "crew-b"},
			tc{name: step + "/old anchor/new agent writes", seedOld: "old seed\n", step: step, newCrew: "crew-b", newWrite: "new fact\n"},
			tc{name: step + "/first write/new agent idle in another crew", step: step, newCrew: "crew-b"},
			// Same crew: the slug reservation refuses the new agent, but the
			// key is released all the same and nobody may hold it yet.
			tc{name: step + "/first write/slug released, same crew reserved", step: step, newCrew: "crew-a"},
		)
	}
	// The new agent reaches exactly the revision and hash the old write read.
	cases = append(cases, tc{name: "before_intent/new agent recreates the same revision and hash",
		seedOld: "old seed\n", step: "before_intent", newCrew: "crew-b", newWrite: "old seed\n"})

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			f := newMutateFixture(t)
			for _, q := range []string{
				`INSERT INTO workspaces(id,name,slug) VALUES ('ws_test','WS','ws-test')`,
				`INSERT INTO crews(id,workspace_id,name,slug) VALUES ('crew-a','ws_test','A','a'),('crew-b','ws_test','B','b')`,
				`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES ('` + oldID + `','ws_test','crew-a','Alice','alice')`,
			} {
				if _, err := f.db.ExecContext(ctx, q); err != nil {
					t.Fatalf("seed %q: %v", q, err)
				}
			}
			agentFile := func(crew string) string {
				p := filepath.Join(f.dir, crew, "agents", "alice", "AGENT.md")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				return p
			}
			oldPath, newPath := agentFile("crew-a"), agentFile(c.newCrew)
			agentReq := func(agentID, path, opID string, op MutateOp, content string) MutateRequest {
				r := f.req(opID, op, content)
				r.ActorID = agentID
				r.ScopeAgentID = agentID
				r.Path = path
				return r
			}
			read := func(p string) string {
				b, err := os.ReadFile(p)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				return string(b)
			}

			if c.seedOld != "" {
				if _, err := Mutate(ctx, f.db, agentReq(oldID, oldPath, "op_seed", OpReplace, c.seedOld)); err != nil {
					t.Fatalf("seed the old agent: %v", err)
				}
			}
			oldBefore, _ := loadAnchor(ctx, f.db, "ws_test", f.auditPath)
			oldFileBefore := read(oldPath)

			sameCrew := c.newCrew == "crew-a"
			fired := false
			inflight := agentReq(oldID, oldPath, "op_inflight", OpAppend, "old secret\n")
			inflight.testHook = func(s string) error {
				if s != c.step || fired {
					return nil
				}
				fired = true
				// Delete the agent, free its slug (as freeDeletedAgentSlug),
				// create the new agent with the same slug.
				if _, err := f.db.ExecContext(ctx, `UPDATE agents SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, oldID); err != nil {
					t.Fatalf("delete agent: %v", err)
				}
				tx, err := f.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatalf("begin release: %v", err)
				}
				if err := RekeyAgentScopeTx(ctx, tx, "ws_test", "alice", archived); err != nil {
					_ = tx.Rollback()
					t.Fatalf("release slug: %v", err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE agents SET slug = ? WHERE id = ?`, archived, oldID); err != nil {
					_ = tx.Rollback()
					t.Fatalf("rename tombstone: %v", err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatalf("commit release: %v", err)
				}
				_, err = f.db.ExecContext(ctx,
					`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES (?,?,?,?,?)`,
					newID, "ws_test", c.newCrew, "Alice 2", "alice")
				if sameCrew {
					// agent_slug_reservations keeps (crew, slug) for the
					// deleted agent: the slug is free in the workspace but
					// cannot be taken in its crew. The key is still released.
					if err == nil || !strings.Contains(err.Error(), "reserved") {
						t.Fatalf("same-crew reuse: want the slug reservation to refuse it, got %v", err)
					}
				} else if err != nil {
					t.Fatalf("create new agent: %v", err)
				}
				if c.newWrite != "" {
					if _, err := Mutate(ctx, f.db, agentReq(newID, newPath, "op_new", OpReplace, c.newWrite)); err != nil {
						t.Fatalf("new agent's first write while the old one is in flight: %v", err)
					}
				}
				return nil
			}

			_, oldErr := Mutate(ctx, f.db, inflight)
			if !fired {
				t.Fatalf("hook step %q never fired", c.step)
			}
			if oldErr != nil && !errors.Is(oldErr, ErrMemoryConflict) {
				t.Fatalf("old in-flight write: want success or ErrMemoryConflict, got %v", oldErr)
			}

			// 1. The new agent's key carries only the new agent's state.
			newKey, err := loadAnchor(ctx, f.db, "ws_test", f.auditPath)
			if err != nil {
				t.Fatal(err)
			}
			if c.newWrite != "" {
				if newKey == nil || newKey.revision != 1 || newKey.contentSHA != hashOf(c.newWrite) {
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
			if c.step == "before_intent" {
				// The agent was gone before its intent: refused with
				// nothing recorded and nothing on disk.
				if !errors.Is(oldErr, ErrMemoryConflict) {
					t.Fatalf("intent of a deleted agent: want ErrMemoryConflict, got %v", oldErr)
				}
				var rows int
				if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_mutations WHERE operation_id = ?`, "op_inflight").Scan(&rows); err != nil {
					t.Fatal(err)
				}
				if rows != 0 {
					t.Fatalf("refused intent left %d mutation rows", rows)
				}
				if got := read(oldPath); got != oldFileBefore {
					t.Fatalf("refused write changed the old agent's file: %q, was %q", got, oldFileBefore)
				}
				if (oldBefore == nil) != (arch == nil) ||
					(arch != nil && (arch.revision != oldBefore.revision || arch.contentSHA != oldBefore.contentSHA)) {
					t.Fatalf("archived anchor = %+v, want the moved %+v untouched", arch, oldBefore)
				}
			} else {
				if oldErr != nil {
					t.Fatalf("an intent recorded before the release must confirm under the archived key, got %v", oldErr)
				}
				wantRev := int64(1)
				if oldBefore != nil {
					wantRev = oldBefore.revision + 1
				}
				if arch == nil || arch.revision != wantRev || arch.contentSHA != hashOf(oldFileBefore+"old secret\n") {
					t.Fatalf("confirmed old write: archived anchor = %+v, want rev %d of the old file", arch, wantRev)
				}
				var state, path string
				if err := f.db.QueryRowContext(ctx,
					`SELECT state, path FROM memory_mutations WHERE operation_id = ?`, "op_inflight").Scan(&state, &path); err != nil {
					t.Fatal(err)
				}
				if state != mutationStateConfirmed || path != archivedAudit {
					t.Fatalf("old mutation row = (%s, %s), want confirmed under %s", state, path, archivedAudit)
				}
			}

			// 3. The new agent keeps writing: no drift, no replay.
			if sameCrew {
				return // no new agent could be created; 1 and 2 are the assertions
			}
			before := read(newPath)
			res, err := Mutate(ctx, f.db, agentReq(newID, newPath, "op_new_2", OpAppend, "more\n"))
			if err != nil {
				t.Fatalf("new agent's write after the old one settled: %v", err)
			}
			if res.Recovered != 0 {
				t.Fatalf("new agent's write replayed %d old-agent intents", res.Recovered)
			}
			if got := read(newPath); got != before+"more\n" {
				t.Fatalf("new agent's file = %q, want %q", got, before+"more\n")
			}

			// 4. The deleted agent can no longer write under any key.
			if _, err := Mutate(ctx, f.db, agentReq(oldID, oldPath, "op_after", OpAppend, "late\n")); !errors.Is(err, ErrMemoryConflict) {
				t.Fatalf("write of the deleted agent after the release: want ErrMemoryConflict, got %v", err)
			}
		})
	}
}
