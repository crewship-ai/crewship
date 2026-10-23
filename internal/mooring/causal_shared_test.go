package mooring

import (
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type causalGoldenEffect struct {
	OperationID      string            `json:"op_id"`
	TargetID         string            `json:"target_id"`
	Before           *BatchCausalEntry `json:"before"`
	After            BatchCausalEntry  `json:"after"`
	ContentChanged   bool              `json:"content_changed"`
	StructureChanged bool              `json:"structure_changed"`
	Created          bool              `json:"created"`
	Deleted          bool              `json:"deleted"`
}
type causalGoldenOperation struct {
	ID           string               `json:"op_id"`
	Revision     string               `json:"revision"`
	Dependencies []string             `json:"dependencies"`
	Complete     bool                 `json:"effects_complete"`
	Effects      []causalGoldenEffect `json:"effects"`
	Status       string               `json:"status"`
}
type causalGoldenCase struct {
	ID             string                  `json:"id"`
	Head           string                  `json:"document_revision"`
	Targets        []BatchTargetVersion    `json:"targets"`
	Ledger         []causalGoldenOperation `json:"ledger"`
	Operations     []BatchOperation        `json:"operations"`
	QualifiedReads []BatchQualifiedRead    `json:"qualified_reads"`
	Expected       struct {
		Host string `json:"host"`
	} `json:"expected"`
}

func causalSeedGolden(t *testing.T, s *BatchStore, tx *sql.Tx, key SessionKey, c causalGoldenCase) {
	t.Helper()
	for _, table := range []string{"mooring_collab_target_effects", "mooring_collab_operations", "mooring_collab_changes", "mooring_collab_target_versions"} {
		if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`UPDATE mooring_collab_documents SET revision=? WHERE page_id=?`, c.Head, key.ArtifactID); err != nil {
		t.Fatal(err)
	}
	for _, target := range c.Targets {
		if _, err := tx.Exec(`INSERT INTO mooring_collab_target_versions(page_id,workspace_id,target_id,revision,structure_revision,deleted) VALUES(?,?,?,?,?,?)`, key.ArtifactID, key.WorkspaceID, target.TargetID, target.Revision, target.StructureRevision, target.Deleted); err != nil {
			t.Fatal(err)
		}
	}
	for _, ledger := range c.Ledger {
		op := testBatchOp(ledger.ID)
		op.Dependencies = ledger.Dependencies
		change := testBatchChange(t, key, "fixture-"+ledger.ID, "", op)
		result := BatchChangeResult{ID: change.ID, Status: "saved", FirstRevision: ledger.Revision, LastRevision: ledger.Revision, Operations: []BatchOperationResult{{ID: op.ID, Status: "saved", Revision: ledger.Revision, Issuer: "crewship", Durability: "node"}}}
		if err := s.saveChange(t.Context(), tx, key, change, result); err != nil {
			t.Fatal(err)
		}
		if !ledger.Complete {
			if _, err := tx.Exec(`UPDATE mooring_collab_operations SET target_effects_complete=0 WHERE page_id=? AND op_id=?`, key.ArtifactID, ledger.ID); err != nil {
				t.Fatal(err)
			}
		}
		rows := []preparedTargetRow{}
		for _, effect := range ledger.Effects {
			row := preparedTargetRow{OperationID: ledger.ID, Revision: ledger.Revision, Effect: BatchTargetEffect{TargetID: effect.TargetID, ContentChanged: effect.ContentChanged, StructureChanged: effect.StructureChanged, Created: effect.Created, Deleted: effect.Deleted}, After: BatchTargetVersion{TargetID: effect.TargetID, Revision: effect.After.Revision, StructureRevision: effect.After.StructureRevision, Deleted: effect.After.Deleted}}
			if effect.Before != nil {
				row.Before = &BatchTargetVersion{TargetID: effect.TargetID, Revision: effect.Before.Revision, StructureRevision: effect.Before.StructureRevision, Deleted: effect.Before.Deleted}
			}
			rows = append(rows, row)
		}
		if err := persistTargetRows(t.Context(), tx, key, rows); err != nil {
			t.Fatal(err)
		}
	}
	// Desired entry state is independent of any deliberately malformed history.
	for _, target := range c.Targets {
		if _, err := tx.Exec(`UPDATE mooring_collab_target_versions SET revision=?,structure_revision=?,deleted=? WHERE page_id=? AND target_id=?`, target.Revision, target.StructureRevision, target.Deleted, key.ArtifactID, target.TargetID); err != nil {
			t.Fatal(err)
		}
	}
}
func TestCausalSQLSharedCompactQualificationVectors(t *testing.T) {
	raw, err := os.ReadFile("../../mooring/protocol/causal-read-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []causalGoldenCase `json:"cases"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) != 29 {
		t.Fatal("shared fixture", err)
	}
	s, _, lease := targetFixture(t)
	consumed := 0
	for _, c := range fixture.Cases {
		if c.Expected.Host == "dependency_rejected" {
			continue
		} // Existing host planner suite qualifies original rejected roots before preparation.
		consumed++
		t.Run(c.ID, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err = ownerFence(t.Context(), tx); err != nil {
				t.Fatal(err)
			}
			causalSeedGolden(t, s, tx, lease.Key, c)
			inventory := make([]BatchTargetIdentity, 0, len(c.Targets))
			for _, target := range c.Targets {
				inventory = append(inventory, BatchTargetIdentity{TargetID: target.TargetID, Deleted: target.Deleted})
			}
			for i := range c.Operations {
				c.Operations[i].Payload = json.RawMessage(`{}`)
				c.Operations[i].Participant = "person"
			}
			change := testBatchChange(t, lease.Key, "probe-change", "", c.Operations...)
			result, err := s.qualifyCausalReads(t.Context(), tx, lease.Key, change, inventory, DefaultBatchHistoryBudget())
			if c.Expected.Host == "nonterminal" {
				if err == nil || len(result.QualifiedReads) != 0 {
					t.Fatal("incomplete history classified", result, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(result.QualifiedReads, c.QualifiedReads) {
				t.Fatalf("shared compact result mismatch: got%+v expected%+v err%v", result.QualifiedReads, c.QualifiedReads, err)
			}
		})
	}
	if consumed != 28 {
		t.Fatal("unconsumed shared cases", consumed)
	}
}
