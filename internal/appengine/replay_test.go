package appengine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// mapEngine is a deterministic engine whose snapshot is a JSON map from
// "sheet!cell" to input, so a replay result depends on every command.
type mapEngine struct{}

func (mapEngine) Identity() string { return "map-engine@1+sha256:test" }
func (mapEngine) Prepare(_ context.Context, b Binding, s State, c Command) (Candidate, error) {
	if c.Kind == "import" {
		return NewCandidate(b, c.Snapshot, []string{"*"}), nil
	}
	cells := map[string]string{}
	if err := json.Unmarshal(s.Snapshot, &cells); err != nil {
		return Candidate{}, err
	}
	written := c.Cells
	if c.Kind == "cell.set" {
		written = []CellInput{{c.Sheet, c.Cell, c.Input}}
	}
	for _, cell := range written {
		cells[cell.Sheet+"!"+cell.Cell] = cell.Input
	}
	data, err := json.Marshal(cells)
	if err != nil {
		return Candidate{}, err
	}
	return NewCandidate(b, data, commandTargets(c)), nil
}

// replayRevision rebuilds revision target from the nearest checkpoint at or
// below it plus the recorded commands, checking the checkpoint and every
// replayed revision against its recorded result hash.
func replayRevision(t *testing.T, db *sql.DB, e Engine, k Key, target int64) json.RawMessage {
	t.Helper()
	var checkpoint int64
	var snapshot []byte
	if err := db.QueryRow(`SELECT rev,snapshot FROM gridcraft_revisions WHERE page_id=? AND rev<=? ORDER BY rev DESC LIMIT 1`, k.PageID, target).Scan(&checkpoint, &snapshot); err != nil {
		t.Fatalf("checkpoint for %d: %v", target, err)
	}
	var recorded sql.NullString
	if err := db.QueryRow(`SELECT result_hash FROM gridcraft_audit WHERE page_id=? AND rev=?`, k.PageID, checkpoint).Scan(&recorded); err != nil || recorded.String != hash(snapshot) {
		t.Fatalf("checkpoint %d hash %q: %v", checkpoint, recorded.String, err)
	}
	rows, err := db.Query(`SELECT rev,kind,command,result_hash FROM gridcraft_audit WHERE page_id=? AND rev>? AND rev<=? ORDER BY rev`, k.PageID, checkpoint, target)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	state, rev := json.RawMessage(snapshot), checkpoint
	for rows.Next() {
		var kind string
		var command []byte
		if err = rows.Scan(&rev, &kind, &command, &recorded); err != nil {
			t.Fatal(err)
		}
		var record commandRecord
		if kind != "cells" || json.Unmarshal(command, &record) != nil {
			t.Fatalf("revision %d is not replayable: %s %s", rev, kind, command)
		}
		c, err := e.Prepare(t.Context(), Binding{Key: k, BaseRev: rev - 1, Generation: 1, Engine: e.Identity()}, State{SchemaVersion: SchemaVersion, Rev: rev - 1, Generation: 1, Snapshot: state}, Command{Kind: "cells.set", Cells: record.Cells})
		if err != nil {
			t.Fatalf("replay %d: %v", rev, err)
		}
		if !recorded.Valid || c.ResultHash != recorded.String {
			t.Fatalf("revision %d diverged: replayed %s recorded %q", rev, c.ResultHash, recorded.String)
		}
		state = c.Snapshot
	}
	if err = rows.Err(); err != nil || rev != target {
		t.Fatalf("replayed to %d of %d: %v", rev, target, err)
	}
	return state
}

func replayCheck(t *testing.T, s *Store, db *sql.DB, a Authorize, e Engine, k Key, targets ...int64) {
	t.Helper()
	head, err := s.Read(t.Context(), k, a)
	if err != nil {
		t.Fatal(err)
	}
	var recorded string
	if err = db.QueryRow(`SELECT result_hash FROM gridcraft_audit WHERE page_id=? AND rev=?`, k.PageID, head.Rev).Scan(&recorded); err != nil || recorded != hash(head.Snapshot) {
		t.Fatalf("head hash %q: %v", recorded, err)
	}
	for _, target := range targets {
		got := replayRevision(t, db, e, k, target)
		if target == head.Rev && !bytes.Equal(got, head.Snapshot) {
			t.Fatalf("replayed head differs:\n%s\n%s", got, head.Snapshot)
		}
	}
}

// Every revision records the hash of its resulting snapshot, so a head or
// any intermediate revision rebuilt from a checkpoint is verified, not assumed.
func TestReplayFromNearestCheckpointMatchesRecordedResultHashes(t *testing.T) {
	s, db, a, k := fixture(t)
	e := mapEngine{}
	if _, err := s.Execute(t.Context(), k, Request{MutationID: "import", MandateID: "human-import", Command: Command{Kind: "import", Snapshot: json.RawMessage(`{"Sheet1!A1":"seed"}`)}}, a, e); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 120; i++ {
		session := fmt.Sprintf("s%d", i%3)
		r := Request{MutationID: fmt.Sprintf("%s:%d", session, i), MandateID: "human-import", SessionID: session, BaseRev: int64(i), Generation: 1, Command: Command{Kind: "cells.set", Cells: []CellInput{{"Sheet1", fmt.Sprintf("A%d", i%17+1), fmt.Sprintf("v%d<&>", i)}, {"Sheet1", "B1", fmt.Sprint(i)}}}}
		if _, err := s.Execute(t.Context(), k, r, a, e); err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
	}
	replayCheck(t, s, db.DB, a, e, k, 1, 37, 50, 99, 121)
}

// The pinned engine reproduces the recorded result of every revision from the
// nearest checkpoint, including formulas and text that JSON would escape.
func TestRealGridCraftReplayFromNearestCheckpointMatchesResultHashes(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		// SKIP-WAIVER(#3967): requires the pinned Linux engine; worker CI verifies all ten real contracts without skips.
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY for the pinned engine replay contract")
	}
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	s, db, a, k := fixture(t)
	engine := GridCraft{Runner: localFixtureRunner{binary, t.TempDir()}}
	if _, err = s.Execute(t.Context(), k, Request{MutationID: "import", MandateID: "human-import", Command: Command{Kind: "import", Snapshot: json.RawMessage(raw)}}, a, engine); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 60; i++ {
		var cs []CellInput
		switch {
		case i == 1:
			cs = []CellInput{{"Sheet1", "B1", "=SUM(A1:A60)"}, {"Sheet1", "C1", "=B1/A3"}}
		case i%7 == 0:
			cs = []CellInput{{"Sheet1", fmt.Sprintf("A%d", i), fmt.Sprintf("text<&>%d", i)}}
		default:
			cs = []CellInput{{"Sheet1", fmt.Sprintf("A%d", i), fmt.Sprint(i * 3)}}
		}
		r := Request{MutationID: fmt.Sprintf("s1:%d", i), MandateID: "human-import", SessionID: "s1", BaseRev: int64(i), Generation: 1, Command: Command{Kind: "cells.set", Cells: cs}}
		if _, err = s.Execute(t.Context(), k, r, a, engine); err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
	}
	replayCheck(t, s, db.DB, a, engine, k, 37, 61)
}
