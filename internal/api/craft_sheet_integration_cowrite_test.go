package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"
)

type sitLeaseRow struct {
	Actor, Session string
	Generation     int64
}

func (s *sitScenario) sitLease() sitLeaseRow {
	s.t.Helper()
	var l sitLeaseRow
	if err := s.f.rig.db.QueryRow(`SELECT actor_id,session_id,generation FROM mooring_edit_leases WHERE page_id='sheet-own'`).Scan(&l.Actor, &l.Session, &l.Generation); err != nil {
		s.t.Fatalf("lease row: %v", err)
	}
	return l
}

// sitAgentWithPersonsLease: the person holds the edit lease in their editor (Editing) and sends a
// message with the Sheet chip; the agent's run starts.
func (s *sitScenario) sitAgentWithPersonsLease() (call func(string, any) (string, bool, json.RawMessage), human, generationH string) {
	s.t.Helper()
	human = s.humanSession()
	generationH = s.humanLease(human)
	s.send("Mark item 7 done", true)
	return s.agent(), human, generationH
}

func sitApply(generation, request string) map[string]any {
	return map[string]any{"generation": generation, "base_version": "1", "request_id": request, "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))}
}

// Co-write, co-write. The person whose message consents holds the edit lease in their editor: the
// agent still takes a co-write grant and its change becomes the version over the head, the person's
// lease unchanged (same actor, session, generation). The person's next save over their old base is
// told moved, and the operations since it carry the agent's change for the editor's replay.
func TestSheetITCoWriteOverTheConsentingPersonsLease(t *testing.T) {
	s := newSheetITScenario(t)
	call, human, generationH := s.sitAgentWithPersonsLease()
	before := s.sitLease()
	lease, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{})
	var got struct {
		Generation string `json:"generation"`
	}
	if refused || json.Unmarshal([]byte(lease), &got) != nil || got.Generation == "" {
		t.Fatalf("no co-write grant while the consenting person's editor holds the Sheet: %.200s", lease)
	}
	applied := s.tool(call, "craft_sheet_apply", sitApply(got.Generation, "sit-cowrite"))
	if !strings.Contains(applied, `"version":2`) || s.head() != 2 {
		t.Fatalf("the agent's co-write is not the version over the head: head %d %.200s", s.head(), applied)
	}
	if after := s.sitLease(); after != before {
		t.Errorf("the person's lease changed under the co-write: %+v → %+v", before, after)
	}
	w := s.humanSave(human, generationH, "1", sitWorkbook(sitInitialL(), map[string]string{"A1": "Code", "A2": "mine"}), "sit-cowrite-person", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"moved"`) || !strings.Contains(w.Body.String(), `"head":"2"`) {
		t.Fatalf("the person's save over base 1 after the co-write: %d %s", w.Code, w.Body)
	}
	code, ops, raw := s.operations(human, "1")
	if code != 200 || !ops.Replayable || len(ops.Versions) != 1 || ops.Versions[0].ActorKind != "agent" || !strings.Contains(string(ops.Versions[0].Ops), "B8") {
		t.Fatalf("operations from 1 after the co-write: %d %s", code, raw)
	}
	if rows := s.opsRows(); len(rows) != 1 || rows[0].Kind != "agent" || !strings.HasSuffix(rows[0].OperationID, ":sit-cowrite") {
		t.Errorf("craft_version_ops after the co-write: %+v", rows)
	}
}

// A co-write never writes over another person: their lease is holder_human, and a lease another person
// took between the agent's grant and its apply stores nothing.
func TestSheetITCoWriteNeverOverAnotherPerson(t *testing.T) {
	t.Run("another person's lease", func(t *testing.T) {
		s := newSheetITScenario(t)
		call, _, _ := s.sitAgentWithPersonsLease()
		mustExec(t, s.f.rig.db, `UPDATE mooring_edit_leases SET actor_id='user:sit-other-person',session_id='sit-other-session',generation=generation+1 WHERE page_id='sheet-own'`)
		lease, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{})
		if !refused || !strings.Contains(lease, "holder_human") {
			t.Errorf("acquire over another person's lease: refused=%v %.200s", refused, lease)
		}
		applied, refused := s.toolCall(call, "craft_sheet_apply", sitApply("1", "sit-other"))
		if !refused || s.head() != 1 || len(s.opsRows()) != 0 {
			t.Errorf("apply over another person's lease: refused=%v head %d %.200s", refused, s.head(), applied)
		}
	})
	t.Run("taken over between the grant and the apply", func(t *testing.T) {
		s := newSheetITScenario(t)
		call, _, _ := s.sitAgentWithPersonsLease()
		lease, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{})
		var got struct {
			Generation string `json:"generation"`
		}
		if refused || json.Unmarshal([]byte(lease), &got) != nil || got.Generation == "" {
			t.Fatalf("no co-write grant to start from: %.200s", lease)
		}
		mustExec(t, s.f.rig.db, `UPDATE mooring_edit_leases SET actor_id='user:sit-other-person',session_id='sit-other-session',generation=generation+1 WHERE page_id='sheet-own'`)
		applied, refused := s.toolCall(call, "craft_sheet_apply", sitApply(got.Generation, "sit-taken"))
		if !refused || s.head() != 1 || len(s.opsRows()) != 0 {
			t.Errorf("apply after another person took the Sheet: refused=%v head %d %.200s", refused, s.head(), applied)
		}
	})
}

// Co-write, publication contract (CAS and idempotence). The person saves between the agent's grant
// and its publication: the agent's change over the old base stores nothing (moved/stale) and the
// person's version stays the head. A repeat of a published change after a lost confirmation answers
// the same version and stores no second one.
func TestSheetITCoWritePublicationIsCASAndIdempotent(t *testing.T) {
	t.Run("the person saves between the grant and the publication", func(t *testing.T) {
		s := newSheetITScenario(t)
		call, human, generationH := s.sitAgentWithPersonsLease()
		lease, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{})
		var got struct {
			Generation string `json:"generation"`
		}
		if refused || json.Unmarshal([]byte(lease), &got) != nil || got.Generation == "" {
			t.Fatalf("no co-write grant to start from: %.200s", lease)
		}
		mine := sitWorkbook(sitInitialL(), map[string]string{"A1": "Code", "A2": "mine", "A3": "X2"})
		if w := s.humanSave(human, generationH, "1", mine, "sit-between", ""); w.Code != 200 || s.head() != 2 {
			t.Fatalf("the person's save between: %d %s", w.Code, w.Body)
		}
		applied, refused := s.toolCall(call, "craft_sheet_apply", sitApply(got.Generation, "sit-late"))
		if !refused || s.head() != 2 || len(s.opsRows()) != 0 {
			t.Errorf("the agent's change over the old base after the person's save: refused=%v head %d ops %d %.200s", refused, s.head(), len(s.opsRows()), applied)
		}
		if !bytes.Equal(s.stored(2), mine) {
			t.Error("the person's version is not the head as saved")
		}
	})
	t.Run("the person saves during calculation", func(t *testing.T) {
		s := newSheetITScenario(t)
		call, human, generationH := s.sitAgentWithPersonsLease()
		generation := s.acquire(call)
		mine := sitWorkbook(sitInitialL(), sitCells{"A1": "Code", "A2": "mine", "A3": "X2"})
		apply := s.m.config.AgentApply
		var humanStatus atomic.Int32
		s.m.config.AgentApply = func(ctx context.Context, profile CraftNativeDocumentProfile, base []byte, commands json.RawMessage) ([]byte, error) {
			saved := s.humanSave(human, generationH, "1", mine, "sit-during-calculation", "")
			humanStatus.Store(int32(saved.Code))
			if saved.Code != 200 {
				return nil, fmt.Errorf("concurrent human save: %d %s", saved.Code, saved.Body)
			}
			return apply(ctx, profile, base, commands)
		}
		applied, refused := s.toolCall(call, "craft_sheet_apply", sitApply(generation, "sit-consented-base"))
		if status := humanStatus.Load(); status != 200 || !refused || !strings.Contains(applied, "stale") || s.head() != 2 || !bytes.Equal(s.stored(2), mine) {
			t.Fatalf("concurrent save: human status=%d, agent refused=%v, head=%d: %.300s", status, refused, s.head(), applied)
		}
		for _, row := range s.opsRows() {
			if row.Kind == "agent" {
				t.Fatalf("rejected agent write left an operations row for version %d", row.Version)
			}
		}
	})
	t.Run("a repeat after a lost confirmation", func(t *testing.T) {
		s := newSheetITScenario(t)
		call, _, _ := s.sitAgentWithPersonsLease()
		lease, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{})
		var got struct {
			Generation string `json:"generation"`
		}
		if refused || json.Unmarshal([]byte(lease), &got) != nil || got.Generation == "" {
			t.Fatalf("no co-write grant to start from: %.200s", lease)
		}
		first, refused := s.toolCall(call, "craft_sheet_apply", sitApply(got.Generation, "sit-once"))
		if refused || !strings.Contains(first, `"version":2`) {
			t.Fatalf("the co-write: %.200s", first)
		}
		again, _ := s.toolCall(call, "craft_sheet_apply", sitApply(got.Generation, "sit-once"))
		if !strings.Contains(again, `"version":2`) || s.head() != 2 || len(s.opsRows()) != 1 {
			t.Errorf("a repeat of the co-write: head %d ops %d %.200s", s.head(), len(s.opsRows()), again)
		}
	})
}

// Co-write, only replayable commands beside the person's lease (allowlist: cell.set, range.setValues,
// range.clear as edit.clearContents, formulas.calculateNow). A structural command (insert a row,
// delete a sheet, rename) or an unknown one is refused with holder_human beside the person's lease
// and stores nothing. The agent's toolset has no structural command at all, so without the person's
// lease (Own) it is refused too, as an invalid request, and nothing is stored. Every command the
// agent may send is replayable: all of them together are published beside the person's lease.
func TestSheetITCoWriteOnlyReplayableCommands(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command map[string]any
	}{
		{"insert a row", sitCmd("rows.insert", map[string]any{"sheet": "L", "range": "A5:A5"})},
		{"delete a sheet", sitCmd("sheet.delete", map[string]any{"sheet": "Other"})},
		{"rename a sheet", sitCmd("sheet.rename", map[string]any{"sheet": "Other", "name": "Codes"})},
		{"format a range", sitCmd("range.setFormat", map[string]any{"sheet": "L", "range": "B2:B9", "format": "0.00"})},
		{"unknown", sitCmd("sit.unknown", map[string]any{"sheet": "L", "range": "B2"})},
	} {
		t.Run("co-write, "+tc.name, func(t *testing.T) {
			s := newSheetITScenario(t)
			call, _, _ := s.sitAgentWithPersonsLease()
			before := s.sitLease()
			lease, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{})
			var got struct {
				Generation string `json:"generation"`
			}
			if refused || json.Unmarshal([]byte(lease), &got) != nil || got.Generation == "" {
				t.Fatalf("no co-write grant: %.200s", lease)
			}
			applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": got.Generation, "base_version": "1", "request_id": "sit-struct", "commands": sitCommands(tc.command)})
			if !refused || s.head() != 1 || len(s.opsRows()) != 0 {
				t.Fatalf("%s beside the person's lease: refused=%v head %d ops %d %.200s", tc.name, refused, s.head(), len(s.opsRows()), applied)
			}
			if !strings.Contains(applied, "holder_human") {
				t.Errorf("%s beside the person's lease is refused, but not as holder_human: %.200s", tc.name, applied)
			}
			if after := s.sitLease(); after != before {
				t.Errorf("the person's lease changed: %+v → %+v", before, after)
			}
		})
		t.Run("own lease, "+tc.name, func(t *testing.T) {
			s := newSheetITScenario(t)
			s.send("Change the Sheet", true)
			call := s.agent()
			generation := s.acquire(call)
			applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "sit-struct-own", "commands": sitCommands(tc.command)})
			if !refused || s.head() != 1 || len(s.opsRows()) != 0 {
				t.Errorf("%s with the agent's own lease: refused=%v head %d %.200s", tc.name, refused, s.head(), applied)
			}
			t.Logf("own lease, %s: %.160s", tc.name, applied)
		})
	}
	t.Run("co-write, every command the agent may send", func(t *testing.T) {
		s := newSheetITScenario(t)
		call, _, _ := s.sitAgentWithPersonsLease()
		lease, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{})
		var got struct {
			Generation string `json:"generation"`
		}
		if refused || json.Unmarshal([]byte(lease), &got) != nil || got.Generation == "" {
			t.Fatalf("no co-write grant: %.200s", lease)
		}
		applied := s.tool(call, "craft_sheet_apply", map[string]any{"generation": got.Generation, "base_version": "1", "request_id": "sit-all", "commands": sitCommands(
			sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}),
			sitCmd("range.setValues", map[string]any{"sheet": "L", "range": "B2:B3", "values": [][]any{{"a"}, {"b"}}}),
			sitCmd("range.clear", map[string]any{"sheet": "L", "range": "B4:B4"}),
			sitCmd("formulas.calculateNow", map[string]any{}),
		)})
		if !strings.Contains(applied, `"version":2`) {
			t.Fatalf("the replayable commands beside the person's lease: %.300s", applied)
		}
		if rows := s.opsRows(); len(rows) != 1 || !rows[0].Replayable || !strings.Contains(string(rows[0].Ops), "edit.clearContents") {
			t.Errorf("ops of the co-write: %+v", rows)
		}
	})
}

// sitLargeCells fills the columns cols of L!2:last with 32 000 synthetic characters a cell, text that
// does not compress past the import's ratio limit.
func sitLargeCells(cols []int, last int) sitCells {
	l := sitInitialL()
	rng := rand.New(rand.NewPCG(26, 11))
	for _, col := range cols {
		for i := 2; i <= last; i++ {
			var b strings.Builder
			fmt.Fprintf(&b, "%d-%02d", col, i)
			for b.Len() < 32000 {
				fmt.Fprintf(&b, "%016x", rng.Uint64())
			}
			l[sitAddress(col, i)] = b.String()[:32000]
		}
	}
	return l
}

// sitLargeAgent: the person saves version 2 with l, then (holding their lease, or not when own) sends
// the message; the agent's run takes a grant.
func (s *sitScenario) sitLargeAgent(l sitCells, own bool) (call func(string, any) (string, bool, json.RawMessage), human, generationH, generation string) {
	s.t.Helper()
	human = s.humanSession()
	generationH = s.humanLease(human)
	if w := s.humanSave(human, generationH, "1", sitWorkbook(l, sitInitialOther()), "sit-large", ""); w.Code != 200 || s.head() != 2 {
		s.t.Fatalf("the person's large save: %d %.200s", w.Code, w.Body)
	}
	if own {
		mustExec(s.t, s.f.rig.db, `DELETE FROM mooring_edit_leases WHERE page_id='sheet-own'`)
	}
	s.send("Clear the large cells", true)
	call = s.agent()
	lease, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{})
	var got struct {
		Generation string `json:"generation"`
	}
	if refused || json.Unmarshal([]byte(lease), &got) != nil || got.Generation == "" {
		s.t.Fatalf("no grant: %.200s", lease)
	}
	return call, human, generationH, got.Generation
}

// Co-write, a large change of replayable commands: three range.clear over L!C2:E28, 81 cells of
// 32 000 characters, each range read for its inverse under the read's 1 MiB answer, together more
// than 2 MiB of operations and inverse, so the change is not replayable. Beside the person's lease it
// is refused as holder_human (head, ops unchanged); with the agent's own lease it is stored, its
// operations row not replayable.
func TestSheetITCoWriteRefusesALargeChange(t *testing.T) {
	clears := sitCommands(
		sitCmd("range.clear", map[string]any{"sheet": "L", "range": "C2:C28"}),
		sitCmd("range.clear", map[string]any{"sheet": "L", "range": "D2:D28"}),
		sitCmd("range.clear", map[string]any{"sheet": "L", "range": "E2:E28"}),
	)
	for _, own := range []bool{false, true} {
		name := map[bool]string{false: "beside the person's lease", true: "own lease"}[own]
		t.Run(name, func(t *testing.T) {
			s := newSheetITScenario(t)
			call, _, _, generation := s.sitLargeAgent(sitLargeCells([]int{3, 4, 5}, 28), own)
			rowsBefore := len(s.opsRows())
			applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "2", "request_id": "sit-large-clear", "commands": clears})
			if !own {
				if !refused || !strings.Contains(applied, "holder_human") || s.head() != 2 || len(s.opsRows()) != rowsBefore {
					t.Errorf("a large change beside the person's lease: refused=%v head %d ops %d→%d %.200s", refused, s.head(), rowsBefore, len(s.opsRows()), applied)
				}
				return
			}
			if refused || s.head() != 3 {
				t.Fatalf("a large change with the agent's own lease: head %d %.200s", s.head(), applied)
			}
			if strings.Contains(applied, "\"undoable\":true") {
				t.Errorf("the card offers Undo for an inverse that exceeds the storage limit: %.300s", applied)
			}
			rows := s.opsRows()
			if last := rows[len(rows)-1]; last.Kind != "agent" || last.Version != 3 || last.Replayable || last.Inverse != "[]" {
				t.Errorf("the large change's ops row is replayable (ops %d B, inverse %d B): %+v", len(last.Ops), len(last.Inverse), last.OperationID)
			}
		})
	}
}

// A change whose inverse cannot be read (one range.clear over L!C2:C81: its read for the inverse
// answers more than the read's 1 MiB) is never presented as undoable: it is refused, or it is stored
// without "undoable":true; an undo of it never answers 2xx as if it had put the values back, and the
// cells stay cleared only when the person was told.
func TestSheetITAChangeWithoutAnInverseIsNotUndoable(t *testing.T) {
	s := newSheetITScenario(t)
	call, human, _, generation := s.sitLargeAgent(sitLargeCells([]int{3}, 81), true)
	applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "2", "request_id": "sit-no-inverse", "commands": sitCommands(
		sitCmd("range.clear", map[string]any{"sheet": "L", "range": "C2:C81"}))})
	if refused {
		if s.head() != 2 {
			t.Errorf("refused, yet head %d", s.head())
		}
		t.Logf("refused: %.200s", applied)
		return
	}
	t.Logf("apply: %.400s", applied)
	if strings.Contains(applied, `"undoable":true`) {
		t.Errorf("a change without an inverse is answered undoable: %.300s", applied)
	}
	var result struct {
		OperationID string `json:"operation_id"`
	}
	_ = json.Unmarshal([]byte(applied), &result)
	rows := s.opsRows()
	last := rows[len(rows)-1]
	t.Logf("stored v%d: replayable %v, ops %d B, inverse %d B", last.Version, last.Replayable, len(last.Ops), len(last.Inverse))
	s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})
	generationH := s.humanLease(human)
	w := s.undo(human, generationH, result.OperationID)
	got := s.headSheets(human)["L"]
	restored := strings.HasPrefix(got["C2"], "3-02")
	if w.Code < 300 && !restored {
		t.Errorf("undo of a change without an inverse answers %d without putting the values back: %s", w.Code, w.Body)
	}
	t.Logf("undo: %d %s; L!C2 back %v", w.Code, w.Body, restored)
}

// Beside the person's lease a change without an inverse is refused as holder_human before the
// engine runs: the person could not undo it (ce86b4832). Head and operations unchanged.
func TestSheetITCoWriteRefusesAChangeWithoutAnInverse(t *testing.T) {
	s := newSheetITScenario(t)
	call, _, _, generation := s.sitLargeAgent(sitLargeCells([]int{3}, 81), false)
	rowsBefore := len(s.opsRows())
	applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "2", "request_id": "sit-no-inverse-cowrite", "commands": sitCommands(
		sitCmd("range.clear", map[string]any{"sheet": "L", "range": "C2:C81"}))})
	if !refused || !strings.Contains(applied, "holder_human") || s.head() != 2 || len(s.opsRows()) != rowsBefore {
		t.Errorf("a change without an inverse beside the person's lease: refused=%v head %d ops %d→%d %.200s", refused, s.head(), rowsBefore, len(s.opsRows()), applied)
	}
}

// An agent lease lasts 15 seconds and only an apply, a renew or a co-write check keeps it. A model
// that takes longer than that between its acquire and its apply (a large change) finds the lease
// expired. Nobody took the Sheet meanwhile, so the apply takes the lease back and is stored without
// a new acquire. When another editor took it, the apply is refused as lease_expired, not as a
// change of the Sheet ("stale"), and nothing is stored.
func TestSheetITAnAgentLeaseThatExpiredBeforeItsApplyIsRenewedUnlessTaken(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Mark item 7 done", true)
	call := s.agent()
	generation := s.acquire(call)
	mustExec(t, s.f.rig.db, `UPDATE mooring_edit_leases SET expires_ns=0 WHERE page_id='sheet-own'`)
	if applied := s.tool(call, "craft_sheet_apply", sitApply(generation, "expired-1")); !strings.Contains(applied, `"version":2`) {
		t.Fatalf("an apply over an expired lease nobody took: %.300s", applied)
	}
	mustExec(t, s.f.rig.db, `UPDATE mooring_edit_leases SET expires_ns=0, generation=generation+1, session_id='another-tab' WHERE page_id='sheet-own'`)
	answer, refused := s.toolCall(call, "craft_sheet_apply", sitApply(generation, "expired-2"))
	if !refused || !strings.Contains(answer, "lease_expired") || s.head() != 2 {
		t.Errorf("an apply over an expired lease taken since: refused=%v head %d %.300s", refused, s.head(), answer)
	}
}
