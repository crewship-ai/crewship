package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
// lease (Own) the tool rejects it as an unsupported engine operation and stores nothing. Every command the
// agent may send is replayable: all of them together are published beside the person's lease.
func TestSheetITCoWriteOnlyReplayableCommands(t *testing.T) {
	rejected := []struct {
		name    string
		command map[string]any
	}{
		{"insert a row", sitCmd("rows.insert", map[string]any{"sheet": "L", "range": "A5:A5"})},
		{"delete a sheet", sitCmd("sheet.delete", map[string]any{"sheet": "Other"})},
		{"rename a sheet", sitCmd("sheet.rename", map[string]any{"sheet": "Other", "name": "Codes"})},
		{"format a range", sitCmd("range.setFormat", map[string]any{"sheet": "L", "range": "B2:B9", "format": "0.00"})},
		{"unknown", sitCmd("sit.unknown", map[string]any{"sheet": "L", "range": "B2"})},
	}
	for _, mode := range []struct {
		name, reason string
		cowrite      bool
	}{
		{"co-write", CraftSheetReasonHolderHuman, true},
		{"own lease", CraftSheetReasonEngineFailed, false},
	} {
		for _, order := range []string{"forward", "reverse"} {
			t.Run(mode.name+"/"+order, func(t *testing.T) {
				start := time.Now()
				s := newSheetITScenario(t)
				var call func(string, any) (string, bool, json.RawMessage)
				if mode.cowrite {
					call, _, _ = s.sitAgentWithPersonsLease()
				} else {
					s.send("Change the Sheet", true)
					call = s.agent()
				}
				generation := s.acquire(call)
				t.Logf("fixture and lease: %s", time.Since(start))
				before := s.sitLease()
				for i := range rejected {
					index := i
					if order == "reverse" {
						index = len(rejected) - 1 - i
					}
					tc := rejected[index]
					t.Run(tc.name, func(t *testing.T) {
						requestID := fmt.Sprintf("sit-struct-%d", index)
						applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": requestID, "commands": sitCommands(tc.command)})
						var failure struct {
							Reason string `json:"reason"`
						}
						if !refused || json.Unmarshal([]byte(applied), &failure) != nil || failure.Reason != mode.reason {
							t.Fatalf("want refusal %s: refused=%v %.200s", mode.reason, refused, applied)
						}
						if s.head() != 1 || len(s.opsRows()) != 0 {
							t.Fatal("refusal changed the head or journal")
						}
						if after := s.sitLease(); after != before {
							t.Errorf("lease changed after refusal: %+v → %+v", before, after)
						}
					})
				}
				applied := s.tool(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "sit-all", "commands": sitCommands(
					sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}),
					sitCmd("range.setValues", map[string]any{"sheet": "L", "range": "B2:B3", "values": [][]any{{"a"}, {"b"}}}),
					sitCmd("range.clear", map[string]any{"sheet": "L", "range": "B4:B4"}),
					sitCmd("formulas.calculateNow", map[string]any{}),
				)})
				if !strings.Contains(applied, `"version":2`) || s.head() != 2 {
					t.Fatalf("replayable commands after the refusals: %.300s", applied)
				}
				if rows := s.opsRows(); len(rows) != 1 || !rows[0].Replayable || !strings.Contains(rows[0].Ops, "edit.clearContents") {
					t.Errorf("ops after the successful write: %+v", rows)
				}
				if after := s.sitLease(); after != before {
					t.Errorf("lease changed after the successful write: %+v → %+v", before, after)
				}
			})
		}
	}
}

// sitLargeCells fills the columns cols of L!2:last with 32 000 synthetic characters a cell, text that
// does not compress past the import's ratio limit.
func sitLargeCells(cols []int, last int) sitCells {
	l := sitInitialL()
	rng := rand.New(rand.NewPCG(26, 11))
	for _, col := range cols {
		for i := 2; i <= last; i++ {
			const cellBytes = 32000
			text := make([]byte, 0, cellBytes+15)
			text = fmt.Appendf(text, "%d-%02d", col, i)
			for len(text) < cellBytes {
				var word [8]byte
				binary.BigEndian.PutUint64(word[:], rng.Uint64())
				text = hex.AppendEncode(text, word[:])
			}
			l[sitAddress(col, i)] = string(text[:cellBytes])
		}
	}
	return l
}

// sitLargeAgent: the person saves version 2 with l, then (holding their lease, or not when own) sends
// the message; the agent's run takes a grant.
func (s *sitScenario) sitLargeAgent(workbook []byte, own bool) (call func(string, any) (string, bool, json.RawMessage), human, generationH, generation string) {
	s.t.Helper()
	human = s.humanSession()
	generationH = s.humanLease(human)
	if w := s.humanSave(human, generationH, "1", workbook, "sit-large", ""); w.Code != 200 || s.head() != 2 {
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

// Measure the undo reader's JSON and resolved journal payload using the same
// values and serialization as the workbook adapter, before any test write.
func sitUndoFixture(t *testing.T, cells sitCells, commands []any, oversizedRead bool) []byte {
	t.Helper()
	start := time.Now()
	raw, err := json.Marshal(commands)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := resolveCraftWorkbookOps(raw)
	if err != nil {
		t.Fatal(err)
	}
	reads, ok := craftUndoReads(ops)
	if !ok || len(reads) == 0 {
		t.Fatal("fixture contains no undo reads")
	}
	session := &sitSession{cells: map[string]sitCells{"L": cells}}
	inputs := make([][][]any, 0, len(reads))
	for _, rd := range reads {
		read, err := session.ReadRange(t.Context(), rd.address, true)
		if err != nil {
			t.Fatal(err)
		}
		area, ok := nativeAgentEngineRange(json.RawMessage(strconv.Quote(rd.rng)))
		if !ok {
			t.Fatalf("invalid fixture range %s", rd.rng)
		}
		if err := craftUndoEncodeText(t.Context(), session, area, read); err != nil {
			t.Fatal(err)
		}
		answer, err := json.Marshal(map[string]any{"command": "sheet.read", "result": read})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("undo read %s: %d bytes", rd.address, len(answer))
		if oversizedRead {
			if len(reads) != 1 || len(answer) <= 1<<20 {
				t.Fatal("fixture must exceed the limit for one inverse read")
			}
		} else if len(answer) > 1<<20 {
			t.Fatal("journal fixture exceeds the limit for one inverse read")
		}
		inputs = append(inputs, read.Values)
	}
	if !oversizedRead {
		inverse, complete := craftInverseFrom(ops, inputs)
		if !complete || craftVersionOpsFit(ops, inverse) {
			t.Fatalf("fixture must produce a complete journal exceeding the limit, got %d bytes", len(ops)+len(inverse))
		}
		t.Logf("complete journal: %d bytes", len(ops)+len(inverse))
	}
	t.Logf("measure undo fixture: %s", time.Since(start))
	start = time.Now()
	workbook := sitWorkbook(cells, sitInitialOther())
	t.Logf("build immutable workbook ZIP: %s", time.Since(start))
	return workbook
}

func TestSheetITRefusesOversizedUndoJournal(t *testing.T) {
	clears := sitCommands(
		sitCmd("range.clear", map[string]any{"sheet": "L", "range": "C2:C24"}),
		sitCmd("range.clear", map[string]any{"sheet": "L", "range": "D2:D24"}),
		sitCmd("range.clear", map[string]any{"sheet": "L", "range": "E2:E24"}),
	)
	workbook := sitUndoFixture(t, sitLargeCells([]int{3, 4, 5}, 24), clears, false)
	for _, own := range []bool{false, true} {
		name := map[bool]string{false: "beside the person's lease", true: "own lease"}[own]
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			s := newSheetITScenario(t)
			call, _, _, generation := s.sitLargeAgent(workbook, own)
			t.Logf("fixture, save and acquire: %s", time.Since(start))

			rowsBefore := len(s.opsRows())
			_, appliesBefore := s.wb.count()
			start = time.Now()
			applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "2", "request_id": "sit-large-clear", "commands": clears})
			t.Logf("apply: %s", time.Since(start))
			_, appliesAfter := s.wb.count()
			if !refused || !strings.Contains(applied, CraftSheetReasonNotUndoable) || s.head() != 2 || len(s.opsRows()) != rowsBefore || appliesAfter != appliesBefore {
				t.Errorf("oversized journal: refused=%v head %d ops %d→%d applied=%d→%d %.200s", refused, s.head(), rowsBefore, len(s.opsRows()), appliesBefore, appliesAfter, applied)
			}
		})
	}
}

func TestSheetITRefusesAChangeWithoutAnInverse(t *testing.T) {
	clears := sitCommands(sitCmd("range.clear", map[string]any{"sheet": "L", "range": "C2:C35"}))
	workbook := sitUndoFixture(t, sitLargeCells([]int{3}, 35), clears, true)
	for _, own := range []bool{false, true} {
		name := map[bool]string{false: "beside the person's lease", true: "own lease"}[own]
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			s := newSheetITScenario(t)
			call, _, _, generation := s.sitLargeAgent(workbook, own)
			t.Logf("fixture, save and acquire: %s", time.Since(start))

			rowsBefore := len(s.opsRows())
			_, appliesBefore := s.wb.count()
			start = time.Now()
			applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "2", "request_id": "sit-no-inverse", "commands": clears})
			t.Logf("apply: %s", time.Since(start))
			_, appliesAfter := s.wb.count()
			if !refused || !strings.Contains(applied, CraftSheetReasonNotUndoable) || s.head() != 2 || len(s.opsRows()) != rowsBefore || appliesAfter != appliesBefore {
				t.Fatalf("unreadable inverse: refused=%v head %d ops %d→%d applied=%d→%d %.200s", refused, s.head(), rowsBefore, len(s.opsRows()), appliesBefore, appliesAfter, applied)
			}
			if !bytes.Equal(s.stored(2), workbook) {
				t.Fatal("original workbook bytes changed")
			}
		})
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
