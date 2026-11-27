package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

type realtimeTestEdit struct {
	Sheet  string
	Range  string
	Values [][]string
}

func applyRealtimeTestEdits(t *testing.T, sheets map[string]sitCells, edits []realtimeTestEdit) {
	t.Helper()
	for _, e := range edits {
		area, ok := nativeAgentEngineRange(json.RawMessage(strconv.Quote(e.Range)))
		if !ok || len(e.Values) != area.height() || sheets[e.Sheet] == nil {
			t.Fatalf("invalid edit in realtime fixture: %+v", e)
		}
		for y, row := range e.Values {
			if len(row) != area.width() {
				t.Fatalf("invalid edit width in realtime fixture: %+v", e)
			}
			for x, value := range row {
				sheets[e.Sheet][craftUndoA1(area.col1+x, area.row1+y)] = value
			}
		}
	}
}

func (s *sitScenario) realtimeAdmission() craftNativeSessionResponse {
	s.t.Helper()
	req := `{"chat_id":` + strconv.Quote(s.f.a.ChatID) + `}`
	w := s.humanRequest(http.MethodPost, req, "", nil, s.m.human.sessions)
	var admission craftNativeSessionResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &admission) != nil ||
		!admission.RealtimeSheet || !admission.CanWrite || admission.Generation != "1" || admission.SessionToken == "" {
		s.t.Fatalf("realtime admission: %d %s", w.Code, w.Body)
	}
	w = s.humanRequest(http.MethodGet, "", admission.SessionToken, nil, s.m.human.lease)
	var view craftNativeLeaseResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Holder != nil ||
		!view.RealtimeSheet || !view.CanEdit || !view.CanWrite || view.Generation == nil || *view.Generation != admission.Generation {
		s.t.Fatalf("realtime lease view: %d %s", w.Code, w.Body)
	}
	return admission
}

func (s *sitScenario) realtimeReconcile(admission craftNativeSessionResponse, base, attempt string, snapshot []byte) (int, []byte) {
	s.t.Helper()
	body, err := json.Marshal(craftNativeSnapshotBody{
		WorkspaceID: s.f.a.WorkspaceID, PageID: "sheet-own", ApplicationID: "gridcraft",
		EnginePin: mooring.NewNativeStore(nil, s.store).EnginePin(), SessionID: admission.SessionID, Generation: admission.Generation,
		BaseVersion: base, EditorRevision: "sit-doc:" + base, ContentHash: craftContentHash(snapshot), AttemptID: attempt,
	})
	if err != nil {
		s.t.Fatal(err)
	}
	w := s.humanRequest(http.MethodPost, string(body), admission.SessionToken, nil, s.m.documents.reconcileSnapshot)
	return w.Code, w.Body.Bytes()
}

func TestSheetITRealtimeOrdersAndExactReconcile(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/craft/sheet-realtime-scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Initial    map[string]sitCells
		Concurrent struct {
			Orders       [][]string
			Human, Agent []realtimeTestEdit
			Persisted    map[string]sitCells
		}
		After struct {
			LaterHuman [][]realtimeTestEdit           `json:"later_human"`
			Undo       map[string]map[string]sitCells `json:"undo_persisted_by_commit_order"`
		} `json:"after_visible_agent_write"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || len(fixture.Concurrent.Orders) != 2 {
		t.Fatalf("realtime scenarios fixture: %+v, %v", fixture, err)
	}
	for _, order := range fixture.Concurrent.Orders {
		t.Run(order[0]+"_first", func(t *testing.T) {
			initial := sitWorkbook(fixture.Initial["L"], fixture.Initial["Other"])
			s := newSheetITScenario(t, initial)
			s.send("Update the status cells", true)
			call := s.agent()
			agentGeneration := s.acquire(call)
			admission := s.realtimeAdmission()
			persisted := s.headSheets(admission.SessionToken)
			applyRealtimeTestEdits(t, persisted, fixture.Concurrent.Human)
			submission := sitWorkbook(persisted["L"], persisted["Other"])
			var first []byte
			var operation string
			for _, actor := range order {
				switch actor {
				case "human":
					w := s.humanSaveWith(admission.SessionToken, admission.Generation, "1", submission, "realtime-original", "", map[string]string{"X-Craft-Touched": `["'L'!B2:B4","'Other'!B3"]`})
					if w.Code != 200 {
						t.Fatalf("concurrent human save: %d %s", w.Code, w.Body)
					}
					first = w.Body.Bytes()
				case "agent":
					commands := make([]map[string]any, 0, len(fixture.Concurrent.Agent))
					for _, edit := range fixture.Concurrent.Agent {
						commands = append(commands, sitCmd("range.setValues", map[string]any{"sheet": edit.Sheet, "range": edit.Range, "values": edit.Values}))
					}
					applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": agentGeneration, "base_version": "1", "request_id": "realtime-agent", "commands": sitCommands(commands...)})
					var result struct {
						Undoable  bool
						Operation string `json:"operation_id"`
					}
					if refused || json.Unmarshal([]byte(applied), &result) != nil || !result.Undoable || result.Operation == "" {
						t.Fatalf("concurrent agent apply: %s", applied)
					}
					operation = result.Operation
				default:
					t.Fatalf("bad actor in fixture %q", actor)
				}
			}
			if got := s.headSheets(admission.SessionToken); s.head() != 3 || !reflect.DeepEqual(got, fixture.Concurrent.Persisted) {
				t.Fatalf("persisted head v%d: %v, want %v", s.head(), got, fixture.Concurrent.Persisted)
			}
			if order[0] == "agent" {
				var merged craftRealtimeMerged
				if err := json.Unmarshal(first, &merged); err != nil || merged.Kind != "merged" ||
					merged.Submitted.BaseVersion != "1" || merged.Submitted.AttemptID != "realtime-original" ||
					merged.Submitted.ContentHash != craftContentHash(submission) || merged.Receipt.BaseVersion != "2" ||
					merged.Receipt.Version != "3" || merged.Receipt.CandidateHash != craftContentHash(merged.Bytes) ||
					!bytes.Equal(merged.Bytes, s.stored(3)) || !reflect.DeepEqual(merged.AgentRanges, []craftSheetRange{{"L", "B3:B5"}}) ||
					!reflect.DeepEqual(merged.Overlap, []craftSheetRange{{"L", "B3:B4"}}) {
					t.Fatalf("invalid merge response: %+v (%v)", merged.Receipt, err)
				}
			}
			for i, edits := range fixture.After.LaterHuman {
				cells := s.headSheets(admission.SessionToken)
				applyRealtimeTestEdits(t, cells, edits)
				base := strconv.FormatInt(s.head(), 10)
				body := sitWorkbook(cells["L"], cells["Other"])
				ranges := make([]string, 0, len(edits))
				for _, edit := range edits {
					ranges = append(ranges, "'"+edit.Sheet+"'!"+edit.Range)
				}
				touched, _ := json.Marshal(ranges)
				w := s.humanSaveWith(admission.SessionToken, admission.Generation, base, body, fmt.Sprintf("realtime-later-%d", i), "", map[string]string{"X-Craft-Touched": string(touched)})
				if w.Code != 200 {
					t.Fatalf("later human save: %d %s", w.Code, w.Body)
				}
			}
			if status, raw := s.realtimeReconcile(admission, "1", "realtime-original", submission); status != 200 || !bytes.Equal(raw, first) {
				t.Fatalf("reconcile lost reply after later head: %d, original receipt preserved=%v", status, bytes.Equal(raw, first))
			}
			w := s.undo(admission.SessionToken, admission.Generation, operation)
			if w.Code != 201 || !strings.Contains(w.Body.String(), `"skipped":1`) {
				t.Fatalf("realtime undo: %d %s", w.Code, w.Body)
			}
			if got, want := s.headSheets(admission.SessionToken), fixture.After.Undo[order[0]+"_first"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("after Undo = %v, want %v", got, want)
			}
			if repeat := s.undo(admission.SessionToken, admission.Generation, operation); repeat.Code != 200 || repeat.Body.String() != w.Body.String() {
				t.Fatalf("realtime repeated undo: %d %s", repeat.Code, repeat.Body)
			}
		})
	}
}

func TestSheetITRealtimeWireFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/craft/sheet-realtime-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	parse := func(key string, dst any) {
		t.Helper()
		decoder := json.NewDecoder(bytes.NewReader(fixtures[key]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(dst); err != nil {
			t.Fatalf("fixture %s: %v", key, err)
		}
	}
	var session craftNativeSessionResponse
	var lease craftNativeLeaseResponse
	parse("session", &session)
	parse("lease", &lease)
	if !session.RealtimeSheet || !lease.RealtimeSheet || lease.Generation == nil || session.Generation != *lease.Generation || !lease.CanWrite || lease.Holder != nil {
		t.Fatal("realtime session and authority proof differ")
	}
	for _, key := range []string{"merged", "merged_unchanged"} {
		var merged craftRealtimeMerged
		parse(key, &merged)
		version, _ := craftNativeVersion(merged.Receipt.Version)
		base, _ := craftNativeVersion(merged.Receipt.BaseVersion)
		format, err := mooring.DetectWorkbook(merged.Bytes)
		hash := craftContentHash(merged.Bytes)
		if err != nil || format != "xlsx" || merged.Kind != "merged" ||
			merged.Receipt.Unchanged != (key == "merged_unchanged") || merged.Receipt.Unchanged != (version == base) ||
			!mooring.ValidNativeAttemptID(merged.Submitted.AttemptID) || merged.Submitted.ContentHash == hash ||
			merged.HeadContentHash != hash || merged.Receipt.ContentHash != hash || merged.Receipt.CandidateHash != hash ||
			merged.Receipt.SessionID != session.SessionID || merged.Receipt.Generation != session.Generation ||
			merged.Receipt.EditorRevision != merged.Submitted.EditorRevision || len(merged.AgentRanges) == 0 || len(merged.Overlap) == 0 || merged.HeadRanges == nil || (len(merged.HeadRanges) == 0) != (key == "merged_unchanged") {
			t.Fatalf("invalid %s fixture receipt: %+v", key, merged.Receipt)
		}
	}
	var snapshot craftSheetPresenceSnapshot
	var start, end craftSheetPresenceEvent
	parse("presence_snapshot", &snapshot)
	parse("presence_started", &start)
	parse("presence_ended", &end)
	if len(snapshot.Operations) != 1 || !reflect.DeepEqual(snapshot.Operations[0], start) ||
		start.OpID != end.OpID || start.RunID != end.RunID || start.Phase != "started" || start.TTL != craftSheetPresenceTTL.Milliseconds() ||
		end.Phase != "ended" || end.TTL != 0 || len(end.Ranges) != 0 {
		t.Fatalf("invalid presence fixture: %+v %+v", start, end)
	}
}

func TestSheetITRealtimeAnchoredWriteHasCompleteInverse(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Update the two status cells", true)
	call := s.agent()
	generation := s.acquire(call)
	admission := s.realtimeAdmission()
	applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "realtime-anchored-write", "commands": sitCommands(
		sitCmd("range.setValues", map[string]any{"sheet": "L", "range": "B3", "values": [][]string{{"first"}, {"second"}}}),
	)})
	var r struct {
		Operation string `json:"operation_id"`
		Undoable  bool   `json:"undoable"`
	}
	if refused || json.Unmarshal([]byte(applied), &r) != nil || !r.Undoable || r.Operation == "" {
		t.Fatalf("anchored apply: %s", applied)
	}
	cells := s.headSheets(admission.SessionToken)["L"]
	if cells["B3"] != "first" || cells["B4"] != "second" {
		t.Fatalf("anchored apply did not cover two cells: %v", cells)
	}
	ops := s.opsRows()
	if len(ops) != 1 || !strings.Contains(ops[0].Inverse, "'L'!B3:B4") || !strings.Contains(ops[0].Ops, "'L'!B3:B4") {
		t.Fatalf("anchored write has incomplete journal: %+v", ops)
	}
	w := s.undo(admission.SessionToken, admission.Generation, r.Operation)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"restored":2`) {
		t.Fatalf("anchored undo: %d %s", w.Code, w.Body)
	}
	cells = s.headSheets(admission.SessionToken)["L"]
	if cells["B3"] != "open" || cells["B4"] != "open" {
		t.Fatalf("anchored undo did not restore both cells: %v", cells)
	}
}

func TestSheetITRealtimeAllOverlapsAndRetiredAttempt(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Change B8", true)
	call := s.agent()
	generation := s.acquire(call)
	admission := s.realtimeAdmission()
	user := s.headSheets(admission.SessionToken)
	user["L"]["B8"] = "person"
	submitted := sitWorkbook(user["L"], user["Other"])
	if status, _ := s.realtimeReconcile(admission, "1", "realtime-retire", submitted); status != 410 {
		t.Fatalf("uncommitted attempt reconcile: %d", status)
	}
	touched := map[string]string{"X-Craft-Touched": `["'L'!B8"]`}
	if w := s.humanSaveWith(admission.SessionToken, admission.Generation, "1", submitted, "realtime-retire", "", touched); w.Code != 409 || s.head() != 1 {
		t.Fatalf("retired attempt wrote: %d %s", w.Code, w.Body)
	}
	applied, refused := s.toolCall(call, "craft_sheet_apply", sitApply(generation, "realtime-all-overlap"))
	if refused || s.head() != 2 {
		t.Fatalf("agent apply: %s", applied)
	}
	w := s.humanSaveWith(admission.SessionToken, admission.Generation, "1", submitted, "realtime-all-lost", "", touched)
	var merged craftRealtimeMerged
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &merged) != nil || merged.Kind != "merged" ||
		merged.Receipt.Version != "2" || merged.Receipt.BaseVersion != "2" || !merged.Receipt.Unchanged || s.head() != 2 ||
		!bytes.Equal(merged.Bytes, s.stored(2)) || !reflect.DeepEqual(merged.Overlap, []craftSheetRange{{"L", "B8"}}) {
		t.Fatalf("all overlaps: %d receipt %+v overlap %+v head %d", w.Code, merged.Receipt, merged.Overlap, s.head())
	}
	if status, raw := s.realtimeReconcile(admission, "1", "realtime-all-lost", submitted); status != 200 || !bytes.Equal(raw, w.Body.Bytes()) {
		t.Fatalf("no-op merge reconciliation: %d original response=%v", status, bytes.Equal(raw, w.Body.Bytes()))
	}
}

func TestSheetITRealtimePresenceAndChatDetachment(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Change B8", true)
	call := s.agent()
	generation := s.acquire(call)
	admission := s.realtimeAdmission()
	next := craftNativeOpenStreamHandler(t, s.f.rig, s.human, admission.SessionToken, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("slug", "sheet-own")
		s.m.human.events(w, r)
	})
	if event, data := next(); event != "sheet_agent_presence_snapshot" || !reflect.DeepEqual(data["operations"], []any{}) {
		t.Fatalf("initial presence: %s %v", event, data)
	}
	engineApply := s.m.config.AgentApply
	entered, release, applied := make(chan struct{}), make(chan struct{}), make(chan string, 1)
	var releaseOnce sync.Once
	stop := func() { releaseOnce.Do(func() { close(release) }) }
	defer stop()
	s.m.config.AgentApply = func(ctx context.Context, p CraftNativeDocumentProfile, workbook []byte, commands json.RawMessage) ([]byte, error) {
		close(entered)
		select {
		case <-release:
			return engineApply(ctx, p, workbook, commands)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	go func() {
		result, _ := s.toolCall(call, "craft_sheet_apply", sitApply(generation, "realtime-presence"))
		applied <- result
	}()
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("agent did not enter engine Apply")
	}
	event, started := next()
	if event != "sheet_agent_presence" || started["phase"] != "started" || started["op_id"] == "" ||
		!reflect.DeepEqual(started["ranges"], []any{map[string]any{"sheet": "L", "a1": "B8"}}) {
		t.Fatalf("start presence: %s %v", event, started)
	}
	stop()
	select {
	case result := <-applied:
		if !strings.Contains(result, `"version":2`) {
			t.Fatalf("agent apply did not commit: %s", result)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("agent did not finish")
	}
	event, ended := next()
	if event != "sheet_agent_presence" || ended["phase"] != "ended" || ended["op_id"] != started["op_id"] || !reflect.DeepEqual(ended["ranges"], []any{}) {
		t.Fatalf("end presence: %s %v", event, ended)
	}
	mustExec(t, s.f.rig.db, `DELETE FROM artifact_message_refs WHERE chat_id=? AND page_id='sheet-own'`, s.f.a.ChatID)
	if w := s.humanRequest(http.MethodGet, "", admission.SessionToken, nil, s.m.human.lease); w.Code != 403 {
		t.Fatalf("detached session read: %d %s", w.Code, w.Body)
	}
	if status, _ := s.realtimeReconcile(admission, "1", "realtime-detached", s.v1); status != 403 {
		t.Fatalf("detached session reconciliation: %d", status)
	}
	if event, data := next(); event != "session_retired" || data["draft"] != "retain" {
		t.Fatalf("detached stream: %s %v", event, data)
	}
}

func TestSheetITRealtimeSaveRetriesCASAgainstBothAgentWrites(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Change the status cells", true)
	call, admission := s.agent(), s.realtimeAdmission()
	generation := s.acquire(call)
	first, refused := s.toolCall(call, "craft_sheet_apply", sitApply(generation, "realtime-agent-first"))
	if refused || s.head() != 2 {
		t.Fatalf("first agent write: %s", first)
	}
	human := sitInitialL()
	human["B2"], human["B7"] = "human B2", "human B7"
	submitted := sitWorkbook(human, sitInitialOther())
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	stop := func() { releaseOnce.Do(func() { close(release) }) }
	defer stop()
	original := s.m.config.AgentApply
	var mergeCalls int
	s.m.config.AgentApply = func(ctx context.Context, p CraftNativeDocumentProfile, workbook []byte, commands json.RawMessage) ([]byte, error) {
		if craftNativeUndo(ctx) {
			mergeCalls++
			if mergeCalls == 1 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		return original(ctx, p, workbook, commands)
	}
	saved := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		saved <- s.humanSaveWith(admission.SessionToken, admission.Generation, "1", submitted, "realtime-racing-save", "", map[string]string{"X-Craft-Touched": `["'L'!B2","'L'!B7"]`})
	}()
	select {
	case <-entered:
	case w := <-saved:
		t.Fatalf("save returned before reaching replay: %d %s", w.Code, w.Body)
	case <-time.After(10 * time.Second):
		t.Fatal("save did not reach replay")
	}
	second, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{
		"generation": generation, "base_version": "2", "request_id": "realtime-agent-second",
		"commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B7", "input": "agent B7"})),
	})
	if refused || s.head() != 3 {
		t.Fatalf("second agent write: %s", second)
	}
	stop()
	select {
	case w := <-saved:
		var data craftRealtimeMerged
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &data) != nil || mergeCalls != 2 ||
			data.Receipt.BaseVersion != "3" || data.Receipt.Version != "4" ||
			!reflect.DeepEqual(data.Overlap, []craftSheetRange{{"L", "B7"}}) ||
			!reflect.DeepEqual(data.AgentRanges, []craftSheetRange{{"L", "B7:B8"}}) {
			t.Fatalf("CAS did not rebase against both changes: code=%d calls=%d receipt=%+v overlaps=%v ranges=%v",
				w.Code, mergeCalls, data.Receipt, data.Overlap, data.AgentRanges)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("save did not finish after the CAS race")
	}
	head := s.headSheets(admission.SessionToken)["L"]
	if head["B2"] != "human B2" || head["B7"] != "agent B7" || head["B8"] != "done" {
		t.Fatalf("unexpected merged worksheet: %v", head)
	}
}

func TestSheetITRealtimeRefusesUnknownEditsAndWriteAfterDetachment(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Change the status cells", true)
	call, admission := s.agent(), s.realtimeAdmission()
	generation := s.acquire(call)
	if got, refused := s.toolCall(call, "craft_sheet_apply", sitApply(generation, "realtime-then-revoked")); refused {
		t.Fatal(got)
	}
	human := sitInitialL()
	human["B2"], human["B3"] = "human B2", "human B3"
	submitted := sitWorkbook(human, sitInitialOther())
	for i, touched := range []string{"", "unknown", `["'L'!B2"]`} {
		headers := map[string]string{}
		if touched != "" {
			headers["X-Craft-Touched"] = touched
		}
		w := s.humanSaveWith(admission.SessionToken, admission.Generation, "1", submitted, fmt.Sprintf("realtime-unproved-%d", i), "", headers)
		if (w.Code != 409 && w.Code != 422) || s.head() != 2 {
			t.Fatalf("unknown/incomplete edits changed head: %d %s", w.Code, w.Body)
		}
	}
	original := s.m.config.AgentApply
	var computed bool
	s.m.config.AgentApply = func(ctx context.Context, p CraftNativeDocumentProfile, workbook []byte, commands json.RawMessage) ([]byte, error) {
		merged, err := original(ctx, p, workbook, commands)
		if err == nil && craftNativeUndo(ctx) {
			computed = true
			_, err = s.f.rig.db.ExecContext(ctx, `DELETE FROM artifact_message_refs WHERE chat_id=? AND page_id='sheet-own'`, s.f.a.ChatID)
		}
		return merged, err
	}
	w := s.humanSaveWith(admission.SessionToken, admission.Generation, "1", submitted, "realtime-detached-save", "", map[string]string{"X-Craft-Touched": `["'L'!B2:B3"]`})
	if !computed || w.Code != 403 || s.head() != 2 {
		t.Fatalf("detachment did not fence computed save: computed=%v head=%d code=%d response=%s", computed, s.head(), w.Code, w.Body)
	}
}

func TestSheetITRealtimeReconcilesPlainUnchangedAndCanSaveAgain(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Look at this sheet", true)
	editor := s.realtimeAdmission()
	const attempt = "realtime-noop-unchanged"
	w := s.humanSaveWith(editor.SessionToken, editor.Generation, "1", s.v1, attempt, "", nil)
	var first craftWorkbookSaveReceipt
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || !first.Unchanged ||
		first.BaseVersion != "1" || first.Version != "1" || first.CandidateHash != craftContentHash(s.v1) {
		t.Fatalf("plain unchanged save: %d %s", w.Code, w.Body)
	}
	originalReply := bytes.Clone(w.Body.Bytes())
	cells := s.headSheets(editor.SessionToken)
	cells["L"]["B2"] = "changed after recovering confirmation"
	next := sitWorkbook(cells["L"], cells["Other"])
	if status, _ := s.realtimeReconcile(editor, "1", attempt, next); status != 409 {
		t.Fatalf("same attempt with a different hash: %d", status)
	}
	if status, _ := s.realtimeReconcile(editor, "1", "realtime-different-attempt", s.v1); status != 410 {
		t.Fatalf("different attempt with identical original bytes: %d", status)
	}
	if status, got := s.realtimeReconcile(editor, "1", attempt, s.v1); status != 200 || !bytes.Equal(got, originalReply) {
		t.Fatalf("lost unchanged reply: status %d exact %v", status, bytes.Equal(got, originalReply))
	}
	w = s.humanSaveWith(editor.SessionToken, editor.Generation, "1", next, "realtime-next-save", "", map[string]string{"X-Craft-Touched": `["'L'!B2"]`})
	if w.Code != 200 || s.head() != 2 || !bytes.Equal(s.stored(2), next) {
		t.Fatalf("next save after recovered no-op: %d %s", w.Code, w.Body)
	}
	if status, got := s.realtimeReconcile(editor, "1", attempt, s.v1); status != 200 || !bytes.Equal(got, originalReply) {
		t.Fatalf("original no-op after head advanced: status %d exact %v", status, bytes.Equal(got, originalReply))
	}
}

func TestSheetITRealtimeRestoreRejectsOldAttempts(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Look at this sheet", true)
	editor := s.realtimeAdmission()
	cells := s.headSheets(editor.SessionToken)
	cells["L"]["B2"] = "committed before the backup"
	committed := sitWorkbook(cells["L"], cells["Other"])
	const committedID = "realtime-restore-committed"
	w := s.humanSaveWith(editor.SessionToken, editor.Generation, "1", committed, committedID, "", map[string]string{"X-Craft-Touched": `["'L'!B2"]`})
	wantCode(t, w, http.StatusOK, "commit with a lost reply")
	if s.head() != 2 {
		t.Fatal("save did not publish version 2")
	}

	cells["L"]["B2"] = "retired before the backup"
	retired := sitWorkbook(cells["L"], cells["Other"])
	const retiredID = "realtime-restore-retired"
	if status, body := s.realtimeReconcile(editor, "2", retiredID, retired); status != http.StatusGone {
		t.Fatalf("retire pending attempt: %d %s", status, body)
	}
	ctx, source := t.Context(), s.f.rig.db
	workspace := s.f.a.WorkspaceID
	var committedRows, retiredRows int
	if err := source.QueryRow(`SELECT count(version), count(*)-count(version) FROM craft_realtime_attempts`).Scan(&committedRows, &retiredRows); err != nil || committedRows != 1 || retiredRows != 1 {
		t.Fatalf("source attempt rows: committed=%d retired=%d err=%v", committedRows, retiredRows, err)
	}
	originalOps := len(s.opsRows())
	if originalOps == 0 {
		t.Fatal("save did not record a workbook operation")
	}
	dump, err := backup.DumpWorkspace(ctx, source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["craft_realtime_attempts"]) != 0 || len(dump.Tables["craft_version_ops"]) != originalOps {
		t.Fatal("workspace backup must carry the saved operations and exclude live attempts")
	}

	s.sitRunEnds()
	const passphrase = "synthetic-sheet-recovery-passphrase"
	bundle, err := backup.CreateInstanceBackup(ctx, source, backup.InstanceOptions{
		OutputDir: t.TempDir(), Passphrase: passphrase, Quiesce: quiesce.New(),
		Actor:    backup.Actor{UserID: s.f.a.HumanUserID, Role: "ADMIN"},
		BusyWait: 3 * time.Second, Poll: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err = backup.RecoverInstance(ctx, backup.RecoverOptions{BundlePath: bundle.Path, Passphrase: passphrase, DataDir: dir, Actor: s.f.a.HumanUserID}); err != nil {
		t.Fatal(err)
	}
	target, err := database.Open("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	// A new server process on the same host boot must require a fresh admission.
	human := newCraftNativeHTTP(NewPageHandler(target.DB, nil, newTestLogger()), true, s.m.human.clock)
	docs := newCraftNativeDocumentsHTTP(human, map[string]craftNativeApplication{"gridcraft": {Store: s.store}})
	for _, attempt := range []struct {
		id, base string
		content  []byte
	}{
		{committedID, "1", committed},
		{retiredID, "2", retired},
	} {
		t.Run(attempt.id, func(t *testing.T) {
			body := craftNativeSnapshotBody{
				WorkspaceID: workspace, PageID: "sheet-own", ApplicationID: "gridcraft",
				EnginePin: mooring.NewNativeStore(nil, s.store).EnginePin(), SessionID: editor.SessionID,
				Generation: editor.Generation, BaseVersion: attempt.base, EditorRevision: "sit-doc:" + attempt.base,
				ContentHash: craftContentHash(attempt.content), AttemptID: attempt.id,
			}
			reconcile, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			w := s.humanRequest(http.MethodPost, string(reconcile), editor.SessionToken, nil, docs.reconcileSnapshot)
			wantCode(t, w, http.StatusForbidden, "reconcile from the source admission")
			body.Bytes = attempt.content
			r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/sheet-own/craft/document/save", workbookSnapshotReader(body)).WithContext(s.human)
			r.SetPathValue("slug", "sheet-own")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Craft-Session", editor.SessionToken)
			r.Header.Set("X-Craft-Touched", `["'L'!B2"]`)
			w = httptest.NewRecorder()
			docs.saveSnapshot(w, r)
			wantCode(t, w, http.StatusForbidden, "save replay from the source admission")
		})
	}
	var attempts, versions, ops int
	if err = target.QueryRow(`SELECT
		(SELECT count(*) FROM craft_realtime_attempts),
		(SELECT count(*) FROM artifact_versions WHERE page_id='sheet-own'),
		(SELECT count(*) FROM craft_version_ops WHERE page_id='sheet-own')`).Scan(&attempts, &versions, &ops); err != nil ||
		attempts != 0 || versions != 2 || ops != originalOps {
		t.Fatalf("recovered workbook state: attempts=%d versions=%d operations=%d err=%v", attempts, versions, ops, err)
	}
	store := artifacts.NewVersionStore(target.DB, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	key := artifacts.VersionKey{WorkspaceID: workspace, PageID: "sheet-own"}
	manifest, err := store.ReadVersion(ctx, key, 2, func(context.Context, *sql.Tx) (string, error) {
		return "user:" + s.f.a.HumanUserID, nil
	})
	if err != nil || !bytes.Equal(manifest.Files[manifest.EntryPoint].Content, committed) {
		t.Fatalf("committed workbook version was lost during recovery: %v", err)
	}
}

func TestSheetITRealtimeCanMergeAcrossUndoInTheSameChat(t *testing.T) {
	initial := sitInitialL()
	initial["C2"] = "original C2"
	s := newSheetITScenario(t, sitWorkbook(initial, sitInitialOther()))
	s.send("Change C2", true)
	call := s.agent()
	generation := s.acquire(call)
	editor := s.realtimeAdmission()
	got, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{
		"generation": generation, "base_version": "1", "request_id": "realtime-C2",
		"commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "C2", "input": "agent C2"})),
	})
	var receipt struct {
		Operation string `json:"operation_id"`
	}
	if refused || json.Unmarshal([]byte(got), &receipt) != nil || receipt.Operation == "" || s.head() != 2 {
		t.Fatalf("agent write: %s", got)
	}
	cells := s.headSheets(editor.SessionToken)
	cells["L"]["B2"] = "independent human edit"
	submitted := sitWorkbook(cells["L"], cells["Other"])
	if undo := s.undo(editor.SessionToken, editor.Generation, receipt.Operation); undo.Code != 201 || s.head() != 3 {
		t.Fatalf("undo: %d %s", undo.Code, undo.Body)
	}
	w := s.humanSaveWith(editor.SessionToken, editor.Generation, "2", submitted, "realtime-after-undo", "", map[string]string{"X-Craft-Touched": `["'L'!B2"]`})
	var merged craftRealtimeMerged
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &merged) != nil || merged.Kind != "merged" ||
		merged.Receipt.Version != "4" || len(merged.AgentRanges) != 0 || len(merged.Overlap) != 0 ||
		!reflect.DeepEqual(merged.HeadRanges, []craftSheetRange{{"L", "C2"}}) {
		t.Fatalf("merge over Undo in the same chat: %d %s", w.Code, w.Body)
	}
	head := s.headSheets(editor.SessionToken)["L"]
	if head["B2"] != "independent human edit" || head["C2"] != "original C2" {
		t.Fatalf("saved cell values B2=%q C2=%q", head["B2"], head["C2"])
	}
}

func TestSheetITRealtimePresenceRangeLimit(t *testing.T) {
	const wireLimit = 32
	columns := make([]map[string]string, 0, wireLimit+1)
	for i := 1; i <= wireLimit+1; i++ {
		columns = append(columns, map[string]string{"sheet": "L", "range": "A" + strconv.Itoa(i)})
	}
	raw, err := json.Marshal(map[string]any{"result": map[string]any{"columns": columns}})
	if err != nil {
		t.Fatal(err)
	}
	ranges, truncated := craftClipSheetPresenceRanges(craftSheetPresenceResolved(raw))
	if len(ranges) != wireLimit || !truncated || ranges[wireLimit-1] != (craftSheetRange{"L", "A32"}) {
		t.Fatalf("presence ranges at the wire limit: %d, truncated=%v", len(ranges), truncated)
	}
}

func TestSheetITRealtimeCompacts257OverlapsAndReopens(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Fill a column of status cells", true)
	call, editor := s.agent(), s.realtimeAdmission()
	generation := s.acquire(call)
	const count = 257
	values := make([][]string, count)
	cells := sitInitialL()
	for i := range count {
		values[i] = []string{"agent value"}
		cells["B"+strconv.Itoa(100+i)] = "human value"
	}
	touched := map[string]string{"X-Craft-Touched": `["'L'!B100:B356"]`}
	submitted := sitWorkbook(cells, sitInitialOther())
	if got, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{
		"base_version": "1", "generation": generation, "request_id": "realtime-many-overlaps",
		"commands": sitCommands(sitCmd("range.setValues", map[string]any{"sheet": "L", "range": "B100:B356", "values": values})),
	}); refused {
		t.Fatal(got)
	}
	w := s.humanSaveWith(editor.SessionToken, editor.Generation, "1", submitted, "realtime-contiguous-submit", "", touched)
	var merged craftRealtimeMerged
	want := []craftSheetRange{{"L", "B100:B356"}}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &merged) != nil || merged.Kind != "merged" ||
		!merged.Receipt.Unchanged || !reflect.DeepEqual(merged.Overlap, want) || !reflect.DeepEqual(merged.AgentRanges, want) {
		t.Fatalf("257 contiguous overlaps: HTTP %d, receipt %+v and %d overlap ranges", w.Code, merged.Receipt, len(merged.Overlap))
	}
	reopened := s.realtimeAdmission()
	actual := s.headSheets(reopened.SessionToken)["L"]
	for i := range count {
		a := "B" + strconv.Itoa(100+i)
		if actual[a] != "agent value" {
			t.Fatalf("reopened %s = %q", a, actual[a])
		}
	}
}

func TestSheetITRealtimeRejects257SeparateAgentRangesBeforeSaving(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Fill non-adjacent status cells", true)
	call, editor := s.agent(), s.realtimeAdmission()
	generation := s.acquire(call)
	const total = 257
	for offset := 0; offset < total; offset += 64 {
		commands := make([]map[string]any, 0, min(64, total-offset))
		for i := offset; i < min(offset+64, total); i++ {
			commands = append(commands, sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B" + strconv.Itoa(100+2*i), "input": "agent"}))
		}
		if got, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{
			"base_version": strconv.FormatInt(s.head(), 10), "generation": generation,
			"request_id": fmt.Sprintf("realtime-separated-%d", offset), "commands": sitCommands(commands...),
		}); refused {
			t.Fatalf("agent batch %d: %s", offset, got)
		}
	}
	before, count := s.head(), len(s.opsRows())
	cells := sitInitialL()
	cells["B2"] = "independent edit"
	body := sitWorkbook(cells, sitInitialOther())
	const attempt = "realtime-separated-save"
	w := s.humanSaveWith(editor.SessionToken, editor.Generation, "1", body, attempt, "", map[string]string{"X-Craft-Touched": `["'L'!B2"]`})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"reason":"too_many_ranges"`) || s.head() != before || len(s.opsRows()) != count {
		t.Fatalf("257 separate ranges: HTTP %d %s, head %d, journal %d", w.Code, w.Body, s.head(), len(s.opsRows()))
	}
	var published int
	if err := s.f.rig.db.QueryRow(`SELECT count(*) FROM craft_realtime_attempts WHERE attempt_id=?`, attempt).Scan(&published); err != nil || published != 0 {
		t.Fatalf("failed save left a receipt: %d %v", published, err)
	}
}

func TestSheetITRealtimeCompactionKeepsHolesAndSheetBoundaries(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Change three cells in L and one in Other", true)
	call, editor := s.agent(), s.realtimeAdmission()
	generation := s.acquire(call)
	if got, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{
		"base_version": "1", "generation": generation, "request_id": "realtime-L-shape",
		"commands": sitCommands(
			sitCmd("range.setValues", map[string]any{"sheet": "L", "range": "A1:A2", "values": [][]string{{"agent A1"}, {"agent A2"}}}),
			sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B1", "input": "agent B1"}),
			sitCmd("cell.set", map[string]any{"sheet": "Other", "cell": "A1", "input": "agent Other A1"}),
		),
	}); refused {
		t.Fatal(got)
	}
	cells := sitInitialL()
	cells["B2"] = "keep this"
	w := s.humanSaveWith(editor.SessionToken, editor.Generation, "1", sitWorkbook(cells, sitInitialOther()), "realtime-human-in-hole", "", map[string]string{"X-Craft-Touched": `["'L'!B2"]`})
	var merged craftRealtimeMerged
	want := []craftSheetRange{{"L", "A1:B1"}, {"L", "A2"}, {"Other", "A1"}}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &merged) != nil ||
		len(merged.Overlap) != 0 || !reflect.DeepEqual(merged.AgentRanges, want) {
		t.Fatalf("exact union: HTTP %d, ranges=%v overlap=%v", w.Code, merged.AgentRanges, merged.Overlap)
	}
	reopened := s.realtimeAdmission()
	if got := s.headSheets(reopened.SessionToken)["L"]["B2"]; got != "keep this" {
		t.Fatalf("B2 was swallowed by the L shape: %q", got)
	}
}

func TestSheetITRealtimeAgentThenUndoHasNoNetRange(t *testing.T) {
	l := sitInitialL()
	l["C2"] = "original"
	s := newSheetITScenario(t, sitWorkbook(l, sitInitialOther()))
	s.send("Change C2", true)
	call, editor := s.agent(), s.realtimeAdmission()
	generation := s.acquire(call)
	got, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{
		"generation": generation, "base_version": "1", "request_id": "realtime-agent-then-undo",
		"commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "C2", "input": "agent"})),
	})
	var receipt struct {
		ID string `json:"operation_id"`
	}
	if refused || json.Unmarshal([]byte(got), &receipt) != nil || receipt.ID == "" {
		t.Fatalf("agent change: %s", got)
	}
	if undo := s.undo(editor.SessionToken, editor.Generation, receipt.ID); undo.Code != 201 {
		t.Fatalf("undo: %d %s", undo.Code, undo.Body)
	}
	l["C2"] = "new human input"
	submission := sitWorkbook(l, sitInitialOther())
	w := s.humanSaveWith(editor.SessionToken, editor.Generation, "1", submission, "realtime-net-zero-agent", "", map[string]string{"X-Craft-Touched": `["'L'!C2"]`})
	var merged craftRealtimeMerged
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &merged) != nil ||
		merged.Kind != "merged" || merged.HeadRanges == nil || len(merged.HeadRanges) != 0 ||
		merged.AgentRanges == nil || len(merged.AgentRanges) != 0 || len(merged.Overlap) != 0 {
		t.Fatalf("a reverted agent write leaked into the net ranges: HTTP %d, %v %v %v", w.Code, merged.HeadRanges, merged.AgentRanges, merged.Overlap)
	}
	if current := s.headSheets(editor.SessionToken)["L"]["C2"]; current != "new human input" {
		t.Fatalf("reverted agent work won against the human: C2=%q", current)
	}
}

func TestSheetITRealtimeHeadRangesUseOtherTabsActualChangesAndReconcile(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Read the status cells", true)
	editor, other := s.realtimeAdmission(), s.realtimeAdmission()
	cells := sitInitialL()
	cells["C2"] = "other tab"
	w := s.humanSaveWith(other.SessionToken, other.Generation, "1", sitWorkbook(cells, sitInitialOther()), "other-tab-head", "", map[string]string{"X-Craft-Touched": `["'L'!C2:D2"]`})
	if w.Code != 200 || s.head() != 2 {
		t.Fatalf("other tab: %d %s", w.Code, w.Body)
	}
	pending := sitInitialL()
	pending["B2"] = "first tab"
	submitted := sitWorkbook(pending, sitInitialOther())
	w = s.humanSaveWith(editor.SessionToken, editor.Generation, "1", submitted, "first-tab-merge", "", map[string]string{"X-Craft-Touched": `["'L'!B2"]`})
	var merged craftRealtimeMerged
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &merged) != nil || merged.Kind != "merged" ||
		!reflect.DeepEqual(merged.HeadRanges, []craftSheetRange{{"L", "C2"}}) || len(merged.AgentRanges) != 0 {
		t.Fatalf("other tab's proof contains a hint or our own write: HTTP %d %v", w.Code, merged.HeadRanges)
	}
	reply := bytes.Clone(w.Body.Bytes())
	cells = s.headSheets(editor.SessionToken)["L"]
	if cells["B2"] != "first tab" || cells["C2"] != "other tab" {
		t.Fatal(cells)
	}
	cells["D2"] = "another version"
	w = s.humanSaveWith(other.SessionToken, other.Generation, "3", sitWorkbook(cells, sitInitialOther()), "other-tab-later", "", map[string]string{"X-Craft-Touched": `["'L'!D2"]`})
	if w.Code != 200 || s.head() != 4 {
		t.Fatalf("later head: %d %s", w.Code, w.Body)
	}
	if status, actual := s.realtimeReconcile(editor, "1", "first-tab-merge", submitted); status != 200 || !bytes.Equal(actual, reply) {
		t.Fatalf("reconcile after another version: HTTP %d, exact reply %v", status, bytes.Equal(actual, reply))
	}
}

func TestSheetITRealtimePartialUndoRangesAreOnlyTheRestoredCells(t *testing.T) {
	l := sitInitialL()
	l["C2"], l["C3"], l["C4"] = "original", "original", "original"
	s := newSheetITScenario(t, sitWorkbook(l, sitInitialOther()))
	s.send("Change C2:C4", true)
	call, editor := s.agent(), s.realtimeAdmission()
	generation := s.acquire(call)
	got, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{
		"generation": generation, "base_version": "1", "request_id": "realtime-partial-undo",
		"commands": sitCommands(sitCmd("range.setValues", map[string]any{"sheet": "L", "range": "C2:C4", "values": [][]string{{"agent"}, {"agent"}, {"agent"}}})),
	})
	var receipt struct {
		ID string `json:"operation_id"`
	}
	if refused || json.Unmarshal([]byte(got), &receipt) != nil || receipt.ID == "" {
		t.Fatalf("agent change: %s", got)
	}
	cells := s.headSheets(editor.SessionToken)["L"]
	cells["C3"] = "keep C3"
	w := s.humanSaveWith(editor.SessionToken, editor.Generation, "2", sitWorkbook(cells, sitInitialOther()), "human-before-partial-undo", "", map[string]string{"X-Craft-Touched": `["'L'!C3"]`})
	if w.Code != 200 {
		t.Fatalf("human change: %d %s", w.Code, w.Body)
	}
	cells["A2"] = "keep A2"
	pending := sitWorkbook(cells, sitInitialOther())
	if w := s.undo(editor.SessionToken, editor.Generation, receipt.ID); w.Code != 201 ||
		!strings.Contains(w.Body.String(), `"restored":2`) || !strings.Contains(w.Body.String(), `"skipped":1`) {
		t.Fatalf("partial Undo: %d %s", w.Code, w.Body)
	}
	w = s.humanSaveWith(editor.SessionToken, editor.Generation, "3", pending, "human-after-partial-undo", "", map[string]string{"X-Craft-Touched": `["'L'!A2"]`})
	var merged craftRealtimeMerged
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &merged) != nil || len(merged.AgentRanges) != 0 ||
		!reflect.DeepEqual(merged.HeadRanges, []craftSheetRange{{"L", "C2"}, {"L", "C4"}}) {
		t.Fatalf("partial Undo effects: %d %v", w.Code, merged.HeadRanges)
	}
	final := s.headSheets(editor.SessionToken)["L"]
	if final["A2"] != "keep A2" || final["C2"] != "original" || final["C3"] != "keep C3" || final["C4"] != "original" {
		t.Fatalf("head after partial Undo and concurrent input: %v", final)
	}
}
