package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// gridcraftCellsEngine imports the given snapshot and confirms cell edits with
// exactly their authored targets as effects.
type gridcraftCellsEngine struct{}

func (gridcraftCellsEngine) Identity() string { return "cells-fixture@1" }
func (gridcraftCellsEngine) Prepare(_ context.Context, b appengine.Binding, s appengine.State, c appengine.Command) (appengine.Candidate, error) {
	if c.Kind == "import" {
		return appengine.NewCandidate(b, c.Snapshot, []string{"*"}), nil
	}
	var effects []string
	for _, cell := range c.Cells {
		effects = append(effects, cell.Sheet+"!"+cell.Cell)
	}
	return appengine.NewCandidate(b, s.Snapshot, effects), nil
}

func TestGridCraftHumanCellEditsUseCellLevelConcurrency(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	wmSeedPage(t, f.db, "ws-old", "workbook-edit", "edit-workbook", "Workbook", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='workbook-edit'`)
	mustExec(t, f.db, `UPDATE users SET full_name='Boss Person' WHERE id='boss'`)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftCellsEngine{})
	base := "/api/v1/pages/edit-workbook/workbook"
	q := "?workspace_id=ws-old"
	wantCode(t, f.do(f.boss, "POST", base+"/operations"+q, `{"mutation_id":"import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`), 200, "import")
	edit := func(session string, seq, base int, cells ...string) string {
		var ops []string
		for _, c := range cells {
			ops = append(ops, fmt.Sprintf(`{"kind":"cell.set","sheet":"Sheet1","cell":%q,"input":"%s-%s"}`, c, session, c))
		}
		return fmt.Sprintf(`{"session_id":%q,"mutation_id":"%s:%d","base_rev":%d,"generation":1,"ops":[%s]}`, session, session, seq, base, strings.Join(ops, ","))
	}
	first := f.do(f.boss, "POST", base+"/edits"+q, edit("s1", 1, 1, "B2", "B3"))
	wantCode(t, first, 200, "human cell edit")
	var receipt appengine.Receipt
	if json.Unmarshal(first.Body.Bytes(), &receipt) != nil || receipt.Rev != 2 || receipt.Durability != "node" || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("receipt %s", first.Body.String())
	}
	retry := f.do(f.boss, "POST", base+"/edits"+q, edit("s1", 1, 1, "B2", "B3"))
	if retry.Code != 200 || retry.Body.String() != first.Body.String() {
		t.Fatalf("retry %d %s", retry.Code, retry.Body.String())
	}
	conflict := f.do(f.boss, "POST", base+"/edits"+q, edit("s2", 1, 1, "B3"))
	wantCode(t, conflict, 409, "same cell from another window")
	var body struct {
		Error            string `json:"error"`
		HeadRev          int64  `json:"head_rev"`
		SnapshotRequired bool   `json:"snapshot_required"`
		Conflicts        []struct {
			Sheet, Cell, Input string
			Rev                int64
			ActorLabel         string `json:"actor_label"`
		} `json:"conflicts"`
	}
	if err := json.Unmarshal(conflict.Body.Bytes(), &body); err != nil || body.Error != "precondition_failed" || body.HeadRev != 2 || body.SnapshotRequired || len(body.Conflicts) != 1 {
		t.Fatalf("conflict body %s", conflict.Body.String())
	}
	if c := body.Conflicts[0]; c.Sheet != "Sheet1" || c.Cell != "B3" || c.Input != "s1-B3" || c.Rev != 2 || c.ActorLabel != "Boss Person" {
		t.Fatalf("conflict %+v", c)
	}
	if strings.Contains(conflict.Body.String(), "user:boss") {
		t.Fatalf("internal authority leaked: %s", conflict.Body.String())
	}
	wantCode(t, f.do(f.boss, "POST", base+"/edits"+q, edit("s2", 2, 1, "C4")), 200, "independent cell rebased")
	tooMany := make([]string, appengine.MaxEditCells+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("A%d", i+1)
	}
	for name, body := range map[string]string{
		"too many ops":        edit("s1", 9, 3, tooMany...),
		"foreign mutation":    strings.Replace(edit("s1", 9, 3, "D5"), `"mutation_id":"s1:9"`, `"mutation_id":"s2:9"`, 1),
		"unsupported op":      strings.Replace(edit("s1", 9, 3, "D5"), `"cell.set"`, `"range.fill"`, 1),
		"unknown field":       strings.Replace(edit("s1", 9, 3, "D5"), `"generation":1`, `"generation":1,"mandate_id":"human-import"`, 1),
		"missing session":     strings.Replace(edit("s1", 9, 3, "D5"), `"session_id":"s1",`, ``, 1),
		"import through edit": `{"session_id":"s1","mutation_id":"s1:9","base_rev":3,"generation":1,"ops":[{"kind":"import"}]}`,
	} {
		wantCode(t, f.do(f.boss, "POST", base+"/edits"+q, body), 400, name)
	}
	wantCode(t, f.do(f.wsAdmin, "POST", base+"/edits"+q, edit("s3", 1, 3, "D5")), 404, "not the owner")
	var head int
	if err := f.db.QueryRow(`SELECT head_rev FROM gridcraft_heads WHERE page_id='workbook-edit'`).Scan(&head); err != nil || head != 3 {
		t.Fatalf("head=%d err=%v", head, err)
	}
}
