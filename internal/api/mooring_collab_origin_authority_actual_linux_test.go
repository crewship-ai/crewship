//go:build linux && mooring_rust_integration

package api

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/work"
)

// Actual native inventory/FULL SQL/BOOTTIME with the original API cell authority.
// HTTP credentials and confirmed attempts are synthetic host fixtures, not a
// signed HTTP ingress or a qualified agent environment. No origin is issued.
func TestCollaborationOriginInventoryKeepsActualOriginalAuthor(t *testing.T) {
	withActualAgentReadSession(t, func(x *collaborationAgentTestFixture, cell *collaborationSessionStore, humans *collaborationHumanRegistry, owner mooring.OwnerLease) {
		var pending *collaborationAgentPending
		x.tx(t, func(tx *sql.Tx) {
			var receipt work.Receipt
			var err error
			pending, receipt, err = x.store.StartTask(x.human, tx, x.key, "read", x.f.a.MessageID, x.f.receipt)
			if err != nil {
				t.Fatal(err)
			}
			x.workID = receipt.WorkID
		})
		handle := x.attach(t, pending)
		request := x.request()
		cp, err := cell.agentCheckpoint(request, x.store, handle, owner)
		if err != nil {
			t.Fatal(err)
		}
		basis := mooring.FullCheckpointBasis{Revision: string(cp.Checkpoint.Revision), Digest: string(cp.Checkpoint.Digest)}
		_, admission, err := humans.issue(x.human, owner, false)
		if err != nil {
			t.Fatal(err)
		}
		humanRequest := httptest.NewRequest("POST", "/host-only-origin-inspection", nil).WithContext(x.human)
		runHuman := func(call func(context.Context) error) error {
			return humans.runHumanOperation(humanRequest, admission.participant, call)
		}
		runAgent := func(call func(context.Context) error) error {
			return cell.runAgentRead(request, x.store, handle, owner, call)
		}
		var expected int
		for _, source := range []struct {
			name string
			run  func(func(context.Context) error) error
		}{{"human", runHuman}, {"agent", runAgent}} {
			count := 0
			err = source.run(func(ctx context.Context) error {
				return cell.store.WithSessionOrigins(ctx, cell.activation, basis, func(scoped context.Context, tx *sql.Tx, inventory *mooring.SessionOriginInventory) error {
					return inventory.Visit(scoped, tx, cell.store, owner, basis, func(uint64, uint32) error { count++; return nil })
				})
			})
			if err != nil || count == 0 {
				t.Fatalf("%s actual inventory denied: count=%d err=%v", source.name, count, err)
			}
			if expected != 0 && count != expected {
				t.Fatal("original readers observed different inventories")
			}
			expected = count
		}
		// A real policy/source ABA inside the SAME writer must be detected after
		// visiting actual native inventory, with all provisional SQL rolled back.
		for _, source := range []struct {
			name   string
			run    func(func(context.Context) error) error
			mutate func(*sql.Tx) error
		}{
			{"human", runHuman, func(tx *sql.Tx) error {
				if _, e := tx.Exec(`UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id='original-session'`); e != nil {
					return e
				}
				_, e := tx.Exec(`UPDATE user_sessions SET revoked_at=NULL WHERE id='original-session'`)
				return e
			}},
			{"agent", runAgent, func(tx *sql.Tx) error {
				if _, e := tx.Exec(`DELETE FROM page_grants WHERE page_id=? AND subject_type='agent' AND subject_id=?`, x.key.ArtifactID, x.f.a.AgentID); e != nil {
					return e
				}
				_, e := tx.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'read',?)`, x.key.ArtifactID, x.f.a.AgentID, x.f.a.HumanUserID)
				return e
			}},
		} {
			visited, mutated := false, false
			err = source.run(func(ctx context.Context) error {
				return cell.store.WithSessionOrigins(ctx, cell.activation, basis, func(scoped context.Context, tx *sql.Tx, inventory *mooring.SessionOriginInventory) error {
					return inventory.Visit(scoped, tx, cell.store, owner, basis, func(uint64, uint32) error {
						visited = true
						if mutated {
							return nil
						}
						if _, e := tx.Exec(`UPDATE pages SET name='uncommitted-origin-side-effect' WHERE id=?`, x.key.ArtifactID); e != nil {
							return e
						}
						if e := source.mutate(tx); e != nil {
							return e
						}
						mutated = true
						return nil
					})
				})
			})
			if err == nil || !visited || !mutated {
				t.Fatalf("%s source ABA did not reach the final authority fence: visited=%v mutated=%v err=%v", source.name, visited, mutated, err)
			}
			var name string
			if e := x.f.rig.db.QueryRow(`SELECT name FROM pages WHERE id=?`, x.key.ArtifactID).Scan(&name); e != nil || name != "Shared" {
				t.Fatal("failed original-author proof published SQL", source.name, name, e)
			}
			// Rollback restores the original metadata; no refreshed admission is made.
			if e := source.run(func(ctx context.Context) error { _, e := cell.store.ReadFullCheckpoint(ctx, owner); return e }); e != nil {
				t.Fatal("rollback damaged original source", source.name, e)
			}
		}
		// Committed grant revocation still blocks work BEFORE reading any inventory.
		mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent' AND subject_id=?`, x.key.ArtifactID, x.f.a.AgentID)
		called := false
		if e := runAgent(func(context.Context) error { called = true; return nil }); e == nil || called {
			t.Fatal("revoked agent invoked inventory work")
		}
		if e := runHuman(func(ctx context.Context) error { _, e := cell.store.ReadFullCheckpoint(ctx, owner); return e }); e != nil {
			t.Fatal("agent revocation denied independent human", e)
		}
	}, true)
}

// Read-only D3 remains readable but the app explicitly does not qualify its
// origin inventory. A host reader must not reinterpret that refusal as an empty
// available namespace, mint an origin, or invoke a consumer with partial data.
func TestCollaborationReadonlyOriginInventoryRejectsUnsupportedDocument(t *testing.T) {
	for _, source := range []string{"human", "agent"} {
		t.Run(source, func(t *testing.T) {
			withActualAgentReadSession(t, func(x *collaborationAgentTestFixture, cell *collaborationSessionStore, humans *collaborationHumanRegistry, owner mooring.OwnerLease) {
				var run func(func(context.Context) error) error
				if source == "human" {
					_, admission, err := humans.issue(x.human, owner, false)
					if err != nil {
						t.Fatal(err)
					}
					request := httptest.NewRequest("POST", "/host-only-readonly-inventory", nil).WithContext(x.human)
					run = func(call func(context.Context) error) error {
						return humans.runHumanOperation(request, admission.participant, call)
					}
				} else {
					var pending *collaborationAgentPending
					x.tx(t, func(tx *sql.Tx) {
						var receipt work.Receipt
						var err error
						pending, receipt, err = x.store.StartTask(x.human, tx, x.key, "read", x.f.a.MessageID, x.f.receipt)
						if err != nil {
							t.Fatal(err)
						}
						x.workID = receipt.WorkID
					})
					handle := x.attach(t, pending)
					request := x.request()
					run = func(call func(context.Context) error) error {
						return cell.runAgentRead(request, x.store, handle, owner, call)
					}
				}
				var basis mooring.FullCheckpointBasis
				if err := run(func(ctx context.Context) error {
					snapshot, e := cell.store.ReadFullCheckpoint(ctx, owner)
					if e == nil {
						basis = mooring.FullCheckpointBasis{Revision: string(snapshot.Checkpoint.Revision), Digest: string(snapshot.Checkpoint.Digest)}
					}
					return e
				}); err != nil {
					t.Fatal("original read-only checkpoint access", err)
				}
				var entered, consumed bool
				err := run(func(ctx context.Context) error {
					entered = true
					return cell.store.WithSessionOrigins(ctx, cell.activation, basis, func(context.Context, *sql.Tx, *mooring.SessionOriginInventory) error { consumed = true; return nil })
				})
				if err == nil || !entered || consumed {
					t.Fatalf("unsupported inventory was substituted: entered=%v consumed=%v err=%v", entered, consumed, err)
				}
			}, true)
		})
	}
}
