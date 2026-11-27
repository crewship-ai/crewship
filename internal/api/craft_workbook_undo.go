package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Undo of an agent change of a workbook Sheet. With the change the server keeps, per operation,
// the values its range held before (the inverse, read through the engine). Undo reads the same
// ranges again and puts back only the cells that still hold what the agent wrote; a cell the
// person changed since is kept and counted skipped. A formula comes back as its text, written with
// formulas allowed for the undo only (craftNativeUndoContext).

const (
	craftUndoMaxCells = 10000 // one range read (nativeAgentReadCells)
	craftUndoMaxRows  = 1000  // one range.setValues
	craftUndoMaxCols  = 200
)

type craftUndoRead struct{ sheet, rng, address string }

type craftUndoOp struct {
	Command string                     `json:"command"`
	Params  map[string]json.RawMessage `json:"params"`
}

type craftInverseEntry struct {
	Range  string  `json:"range"`
	Values [][]any `json:"values"`
}

// craftUndoReads names the range each resolved operation wrote, its sheet apart.
func craftUndoReads(ops json.RawMessage) ([]craftUndoRead, bool) {
	var list []craftUndoOp
	if json.Unmarshal(ops, &list) != nil || len(list) == 0 {
		return nil, false
	}
	out := make([]craftUndoRead, 0, len(list))
	for _, op := range list {
		key := "range"
		if op.Command == "cell.set" {
			key = "cell"
		}
		var address string
		if json.Unmarshal(op.Params[key], &address) != nil {
			return nil, false
		}
		cut := strings.LastIndex(address, "!")
		if cut < 1 {
			return nil, false
		}
		sheet, rng := address[:cut], address[cut+1:]
		if len(sheet) >= 2 && sheet[0] == '\'' && sheet[len(sheet)-1] == '\'' {
			sheet = strings.ReplaceAll(sheet[1:len(sheet)-1], "''", "'")
		}
		area, ok := nativeAgentEngineRange(json.RawMessage(strconv.Quote(rng)))
		if !ok || area.cells() > craftUndoMaxCells || area.width() > craftUndoMaxCols {
			return nil, false
		}
		out = append(out, craftUndoRead{sheet: sheet, rng: rng, address: address})
	}
	return out, true
}

// craftInverseFrom keeps what each range held before the change, formulas as their text.
func craftInverseFrom(ops json.RawMessage, old [][][]any) (json.RawMessage, bool) {
	reads, ok := craftUndoReads(ops)
	if !ok || len(reads) != len(old) {
		return nil, false
	}
	out := make([]craftInverseEntry, len(reads))
	for i, rd := range reads {
		// Every cell of the range, as the engine read it: an answer that does not cover the whole
		// rectangle (fewer rows, a shorter row) leaves the change without an undo, never with a
		// partial one that would report cells it did not put back.
		area, ok := nativeAgentEngineRange(json.RawMessage(strconv.Quote(rd.rng)))
		if !ok || len(old[i]) != area.height() {
			return nil, false
		}
		for _, row := range old[i] {
			if len(row) != area.width() {
				return nil, false
			}
		}
		out[i] = craftInverseEntry{Range: rd.address, Values: old[i]}
	}
	raw, err := json.Marshal(out)
	return raw, err == nil
}

// craftUndoTouched names the cells touched since the agent's version by a logged operation of a
// later version; all reports that a later log said it does not know.
type craftUndoTouched struct {
	all   bool
	areas map[string][]nativeAgentEngineArea
}

func (t craftUndoTouched) has(sheet string, col, row int) bool {
	if t.all {
		return true
	}
	for _, a := range t.areas[sheet] {
		if col >= a.col1 && col <= a.col2 && row >= a.row1 && row <= a.row2 {
			return true
		}
	}
	return false
}

// add reads one later version's logged operations: every range they name, or all when one names
// "*" (the editor did not know what it changed) or a range it cannot read.
func (t *craftUndoTouched) add(ops json.RawMessage) {
	var list []craftUndoOp
	if json.Unmarshal(ops, &list) != nil {
		return
	}
	for _, op := range list {
		var address string
		if json.Unmarshal(op.Params["range"], &address) != nil && json.Unmarshal(op.Params["cell"], &address) != nil {
			continue
		}
		sheet, area, ok := craftSheetAddress(address)
		if address == "*" || !ok {
			// Not knowing which cells, or an address it cannot read, counts as every cell.
			t.all = true
			return
		}
		if t.areas == nil {
			t.areas = map[string][]nativeAgentEngineArea{}
		}
		t.areas[sheet] = append(t.areas[sheet], area)
	}
}

// craftUndoCommands are the writes that put back the untouched cells, latest operation first,
// with how many cells come back and how many are kept. A cell comes back only when no later logged
// operation names it and its raw input in the head is exactly the one in the agent's version.
func craftUndoCommands(ops, inverse json.RawMessage, atAgent, current [][][]any, touched craftUndoTouched) ([]json.RawMessage, int, int, bool) {
	reads, ok := craftUndoReads(ops)
	var list []craftUndoOp
	var inv []craftInverseEntry
	if !ok || json.Unmarshal(ops, &list) != nil || json.Unmarshal(inverse, &inv) != nil || len(inv) != len(reads) || len(current) != len(reads) || len(atAgent) != len(reads) {
		return nil, 0, 0, false
	}
	var commands []json.RawMessage
	skipped, restored := 0, 0
	for i := len(reads) - 1; i >= 0; i-- {
		area, _ := nativeAgentEngineRange(json.RawMessage(strconv.Quote(reads[i].rng)))
		h, w := area.height(), area.width()
		col0, row0, _ := nativeAgentEngineAddress(strings.SplitN(reads[i].rng, ":", 2)[0])
		restore := make([][]bool, h)
		all := true
		for r := 0; r < h; r++ {
			restore[r] = make([]bool, w)
			for c := 0; c < w; c++ {
				restore[r][c] = !touched.has(reads[i].sheet, col0+c, row0+r) && craftUndoSameInput(craftUndoCell(current[i], r, c), craftUndoCell(atAgent[i], r, c))
				if restore[r][c] {
					restored++
				} else {
					skipped++
					all = false
				}
			}
		}
		emit := func(r, c0, rows, cols int) {
			values := make([][]any, rows)
			empty := true
			for y := 0; y < rows; y++ {
				values[y] = make([]any, cols)
				for x := 0; x < cols; x++ {
					values[y][x] = craftUndoCell(inv[i].Values, r+y, c0+x)
					if values[y][x] != nil && values[y][x] != "" {
						empty = false
					}
				}
			}
			rng := craftUndoA1(col0+c0, row0+r)
			if rows > 1 || cols > 1 {
				rng += ":" + craftUndoA1(col0+c0+cols-1, row0+r+rows-1)
			}
			params := map[string]any{"range": rng, "sheet": reads[i].sheet}
			command := "range.clear"
			if !empty {
				command, params["values"] = "range.setValues", values
			}
			raw, _ := json.Marshal(map[string]any{"command": command, "params": params})
			commands = append(commands, raw)
		}
		if all {
			for r := 0; r < h; r += craftUndoMaxRows {
				emit(r, 0, min(craftUndoMaxRows, h-r), w)
			}
			continue
		}
		for r := 0; r < h; r++ {
			for c := 0; c < w; {
				if !restore[r][c] {
					c++
					continue
				}
				end := c
				for end < w && restore[r][end] {
					end++
				}
				emit(r, c, 1, end-c)
				c = end
			}
		}
	}
	return commands, restored, skipped, true
}

func craftUndoCell(grid [][]any, r, c int) any {
	if r < len(grid) && c < len(grid[r]) {
		return grid[r][c]
	}
	return nil
}

// craftUndoSameInput: the cell's raw input (a formula as its text, a constant with its type) is
// the same in both reads, without normalizing: "001" is not 1. An empty cell reads as nil or "".
func craftUndoSameInput(a, b any) bool {
	blank := func(v any) bool { return v == nil || v == "" }
	if blank(a) || blank(b) {
		return blank(a) && blank(b)
	}
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}

func craftUndoA1(col, row int) string {
	name := ""
	for col > 0 {
		col--
		name = string(rune('A'+col%26)) + name
		col /= 26
	}
	return name + strconv.Itoa(row)
}

// craftUndoReadValues reads, with formulas, every range the operations wrote, in raw.
func craftUndoReadValues(ctx context.Context, inspect CraftNativeAgentInspect, p CraftNativeDocumentProfile, raw []byte, ops json.RawMessage) ([][][]any, bool) {
	reads, ok := craftUndoReads(ops)
	if !ok || inspect == nil {
		return nil, false
	}
	out := make([][][]any, len(reads))
	for i, rd := range reads {
		query, _ := json.Marshal(map[string]any{"command": "sheet.read", "params": map[string]any{"range": rd.rng, "sheet": rd.sheet, "formulas": true}})
		answer, err := inspect(ctx, p, raw, query)
		var view struct {
			Result struct {
				Values [][]any `json:"values"`
			} `json:"result"`
		}
		if err != nil || json.Unmarshal(answer, &view) != nil {
			return nil, false
		}
		out[i] = view.Result.Values
	}
	return out, true
}

// craftUndoReceipt answers an undo.
type craftUndoReceipt struct {
	Version string `json:"version"`
	// Restored and Skipped: the cells that got their old value back and the cells kept because
	// they were touched since the agent's change.
	Restored int64 `json:"restored"`
	Skipped  int64 `json:"skipped"`
	// SignatureInvalidated: the workbook's XML package signature was kept but no longer matches.
	SignatureInvalidated bool `json:"signature_invalidated,omitempty"`
}

// craftUndoBatch is the most commands one engine apply takes.
const craftUndoBatch = 64

// undoOperation answers POST craft/document/undo {operation_id}: the person, holding the edit lease
// (X-Craft-Session, X-Craft-Generation), undoes one agent change of a workbook Sheet. The cells
// that still hold what the agent wrote get their old values back through the engine; the result is
// the person's new version (audit source undo). 201 {version, skipped}; a repeat 200 with the same.
func (d *craftNativeDocumentsHTTP) undoOperation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r, entry, s, app, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if !s.Workbook() {
		replyCode(w, 404, "not_workbook", "undo is kept for workbook Sheets only")
		return
	}
	generation, err := craftNativeGeneration(r.Header.Get("X-Craft-Generation"))
	var body struct {
		OperationID string `json:"operation_id"`
	}
	if err != nil || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body) != nil || body.OperationID == "" || len(body.OperationID) > 300 {
		replyError(w, 400, "undo needs X-Craft-Generation and operation_id")
		return
	}
	ctx, db, scope := r.Context(), d.human.pages.db, entry.scope
	// A fenced write: the caller's live lease of this generation, checked before the engine runs
	// (the save checks it again in its transaction).
	if held, e := d.human.leases.Read(ctx, scope, craftNativePrincipal(entry)); e != nil || held.Generation != generation || held.Holder != craftNativePrincipal(entry) {
		craftNativeDocumentError(w, mooring.ErrEditLeaseStale)
		return
	}
	var doneVersion, doneSkipped, doneRestored int64
	switch err = db.QueryRowContext(ctx, `SELECT version, skipped, restored FROM craft_version_undos WHERE workspace_id=? AND page_id=? AND operation_id=?`, scope.WorkspaceID, scope.ArtifactID, body.OperationID).Scan(&doneVersion, &doneSkipped, &doneRestored); {
	case err == nil:
		writeJSON(w, 200, craftUndoReceipt{Version: strconv.FormatInt(doneVersion, 10), Restored: doneRestored, Skipped: doneSkipped})
		return
	case !errors.Is(err, sql.ErrNoRows):
		craftNativeDocumentError(w, err)
		return
	}
	var ops, inverse string
	var agentVersion int64
	err = db.QueryRowContext(ctx, `SELECT ops, inverse, version FROM craft_version_ops WHERE workspace_id=? AND page_id=? AND operation_id=? AND actor_kind='agent'`, scope.WorkspaceID, scope.ArtifactID, body.OperationID).Scan(&ops, &inverse, &agentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		replyCode(w, 404, "not_found", "no agent change with this operation_id on this Sheet")
		return
	}
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if inverse == "" || inverse == "[]" {
		replyCode(w, 409, "not_undoable", "the server did not keep what this change replaced")
		return
	}
	profile, ok := d.profile(app.Store.Application)
	agents := d.human.agents
	if !ok || agents == nil || agents.config.AgentApply == nil || agents.config.AgentInspect == nil {
		replyError(w, 503, "the Sheet engine is unavailable")
		return
	}
	state, err := s.Read(ctx, scope)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	f, err := s.DocumentFile(state.Manifest)
	if err != nil {
		if !craftWorkbookError(w, err) {
			craftNativeDocumentError(w, err)
		}
		return
	}
	current, ok := craftUndoReadValues(ctx, agents.config.AgentInspect, profile, f.Content, json.RawMessage(ops))
	if !ok {
		replyCode(w, 422, "engine_failed", "the Sheet engine could not read the changed ranges")
		return
	}
	atAgent := current
	if agentVersion != state.LatestVersion {
		manifest, _, err := s.ReadVersion(ctx, scope, agentVersion)
		if err != nil {
			craftNativeDocumentError(w, err)
			return
		}
		then, err := s.DocumentFile(manifest)
		if err != nil {
			if !craftWorkbookError(w, err) {
				craftNativeDocumentError(w, err)
			}
			return
		}
		if atAgent, ok = craftUndoReadValues(ctx, agents.config.AgentInspect, profile, then.Content, json.RawMessage(ops)); !ok {
			replyCode(w, 422, "engine_failed", "the Sheet engine could not read the agent's version")
			return
		}
	}
	touched, err := craftUndoTouchedSince(ctx, db, scope, agentVersion)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	commands, restored, skipped, ok := craftUndoCommands(json.RawMessage(ops), json.RawMessage(inverse), atAgent, current, touched)
	if !ok {
		replyCode(w, 409, "not_undoable", "the change cannot be undone")
		return
	}
	if len(commands) == 0 {
		writeJSON(w, 200, craftUndoReceipt{Version: strconv.FormatInt(state.LatestVersion, 10), Restored: 0, Skipped: int64(skipped)})
		return
	}
	candidate := f.Content
	for start := 0; start < len(commands); start += craftUndoBatch {
		batch, _ := json.Marshal(commands[start:min(start+craftUndoBatch, len(commands))])
		if candidate, err = agents.config.AgentApply(craftNativeUndoContext(ctx), profile, candidate, batch); err != nil {
			// Names the refusal, never a cell's content.
			var first struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(commands[start], &first)
			slog.Warn("craft workbook undo: engine refused the change", "error", err, "commands", len(commands),
				"batch", start/craftUndoBatch, "first_command", first.Command)
			if !nativeAgentChangeRefused(w, err) {
				replyCode(w, 422, "engine_failed", "the Sheet engine could not undo the change")
			}
			return
		}
	}
	upload, err := s.ReserveUpload(ctx, scope, craftNativeReservation(int64(len(candidate))))
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	defer upload.Close()
	lease := mooring.EditLease{Scope: scope, Holder: craftNativePrincipal(entry), Generation: generation}
	user, operation := entry.source.UserID, body.OperationID
	sheetRecord := craftSheetRecord(scope, app.Store.Application, user, craftSheetSavedByUndo)
	sent, _ := json.Marshal(commands)
	undoOps, replayable := craftNativeAgentResolvedOps(sent)
	signed := craftWorkbookSigned(f.Content)
	record := func(ctx context.Context, tx *sql.Tx, rc artifacts.VersionReceipt, native []byte) error {
		if err := sheetRecord(ctx, tx, rc, native); err != nil {
			return err
		}
		if signed {
			if err := recordCraftSignatureBreak(ctx, tx, scope.WorkspaceID, scope.ArtifactID, rc.Version); err != nil {
				return err
			}
		}
		// The editor holding the lease replays the undo like an agent change: the commands it sent
		// to the engine, in the editor's execute names. No inverse: an undo is not undone again.
		var name string
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE(full_name,'') FROM users WHERE id=?`, user).Scan(&name)
		if err := recordCraftVersionOps(ctx, tx, craftVersionOps{WorkspaceID: scope.WorkspaceID, PageID: scope.ArtifactID, Version: rc.Version, BaseVersion: state.LatestVersion,
			ActorKind: "human", ActorID: user, ActorName: name, OperationID: "undo:" + operation, EnginePin: s.EnginePin(), Calculate: "calculated", Replayable: replayable, Ops: undoOps}); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO craft_version_undos(workspace_id,page_id,operation_id,version,skipped,restored,user_id,created_at) VALUES(?,?,?,?,?,?,?,?)`,
			scope.WorkspaceID, scope.ArtifactID, operation, rc.Version, skipped, restored, user, tsformat.Format(time.Now().UTC()))
		return err
	}
	receipt, err := s.SaveAttemptRecorded(upload.Context(ctx), lease, state.LatestVersion, "undo:"+operation, craftContentHash(candidate), candidate, "", record)
	if err != nil {
		if errors.Is(err, artifacts.ErrVersionConflict) && d.craftWorkbookMoved(w, r, s, entry, state.LatestVersion) {
			return
		}
		if !craftWorkbookError(w, err) {
			craftNativeDocumentError(w, err)
		}
		return
	}
	writeJSON(w, 201, craftUndoReceipt{Version: receipt.Version, Restored: int64(restored), Skipped: int64(skipped), SignatureInvalidated: signed})
}

type craftNativeUndoKey struct{}

// craftNativeUndoContext marks an engine apply as a person's undo: it may put back a formula.
func craftNativeUndoContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, craftNativeUndoKey{}, true)
}

func craftNativeUndo(ctx context.Context) bool {
	v, _ := ctx.Value(craftNativeUndoKey{}).(bool)
	return v
}

// craftUndoTouchedSince reads the logged operations of every version after the agent's.
func craftUndoTouchedSince(ctx context.Context, db *sql.DB, scope mooring.EditLeaseScope, version int64) (craftUndoTouched, error) {
	var t craftUndoTouched
	rows, err := db.QueryContext(ctx, `SELECT ops FROM craft_version_ops WHERE workspace_id=? AND page_id=? AND version>? ORDER BY version`, scope.WorkspaceID, scope.ArtifactID, version)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var ops string
		if err := rows.Scan(&ops); err != nil {
			return t, err
		}
		t.add(json.RawMessage(ops))
	}
	return t, rows.Err()
}

// craftSheetAddress reads a sheet-qualified address ("'L'!B2", "'L'!A1:C9"), of any size.
func craftSheetAddress(address string) (string, nativeAgentEngineArea, bool) {
	cut := strings.LastIndex(address, "!")
	if cut < 3 || address[0] != '\'' || address[cut-1] != '\'' {
		return "", nativeAgentEngineArea{}, false
	}
	sheet := strings.ReplaceAll(address[1:cut-1], "''", "'")
	area, ok := nativeAgentEngineRange(json.RawMessage(strconv.Quote(address[cut+1:])))
	return sheet, area, ok && sheet != ""
}
