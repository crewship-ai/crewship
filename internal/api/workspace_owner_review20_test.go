package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestOwnerCreationDedicatedEvent(t *testing.T) {
	for _, door := range []string{"create", "signup", "bootstrap"} {
		t.Run(door, func(t *testing.T) {
			f := newOwnerCreationFixture(t, door)
			rr := f.mutate()
			want := 201
			if door == "signup" {
				want = 202
			}
			if rr.Code != want {
				t.Fatalf("SETUP: actual %s mutation status %d want %d", door, rr.Code, want)
			}
			var owners int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE role='OWNER'`).Scan(&owners); err != nil || owners != 1 {
				t.Fatalf("SETUP: actual OWNER not committed: %d %v", owners, err)
			}
			var dedicated, generic int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='member.owner_created'`).Scan(&dedicated); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='member.access_changed' AND json_extract(payload,'$.action')='owner_created'`).Scan(&generic); err != nil {
				t.Fatal(err)
			}
			if dedicated != 1 || generic != 0 {
				t.Errorf("committed owner creation audit types: dedicated=%d generic=%d, want 1/0", dedicated, generic)
			}
		})
	}
}

func TestOwnerCreationGuestWorkspaceForbidden(t *testing.T) {
	f := newOwnerCreationFixture(t, "create")
	if _, err := f.db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('review20-guest','owner-audit-calibration','owner-create-actor','VIEWER','restricted')`); err != nil {
		t.Fatal(err)
	}
	before := ownerCreationCounts(t, f.db)
	rr := f.mutate()
	if rr.Code != 403 {
		t.Errorf("actual Guest POST/workspaces status=%d want403", rr.Code)
	}
	if !reflect.DeepEqual(before, ownerCreationCounts(t, f.db)) || f.published.Load() != 0 {
		t.Fatal("denied Guest changed state or notified")
	}
}

type semanticOwnerEmitter struct{ *journal.Writer }

func (w semanticOwnerEmitter) AppendTx(ctx context.Context, tx *sql.Tx, e journal.Entry) (func(), error) {
	// A native writer may encode the same snapshot from a decoded object rather
	// than the producer's struct field order. Its own keyed ACK remains enforced.
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		return nil, err
	}
	e.Payload = nil // Decode into an independent map, leaving the producer snapshot intact.
	if err := json.Unmarshal(raw, &e.Payload); err != nil {
		return nil, err
	}
	return w.Writer.AppendTx(ctx, tx, e)
}

func TestOwnerCreationAuditAcknowledgesSemanticSnapshot(t *testing.T) {
	f := newOwnerCreationFixture(t, "create")
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{ChainKey: f.key})
	t.Cleanup(func() { _ = writer.Close() })
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('semantic-owner','Semantic','semantic-owner')`); err != nil {
		t.Fatal(err)
	}
	inserted, err := tx.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('semantic-owner-member','semantic-owner','owner-create-actor','OWNER')`)
	if err != nil {
		t.Fatal(err)
	}
	notify, err := appendWorkspaceOwnerAudit(t.Context(), tx, semanticOwnerEmitter{writer}, inserted, "semantic-owner", "semantic-owner-member", "owner-create-actor", "workspace_create")
	if err != nil {
		t.Fatalf("equivalent native audit snapshot rejected: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if notify != nil {
		notify()
	}
	proof, err := journal.VerifyChainWithKey(t.Context(), f.db, "semantic-owner", f.key)
	if err != nil || !proof.OK {
		t.Fatalf("actual configured chain invalid: %v", err)
	}
}
