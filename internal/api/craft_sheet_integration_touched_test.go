package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// sitTouchedSetup: the agent wrote L!B7=1 and L!B8=done as version 2 and released; the person holds
// the lease. It returns the person's session, generation and the agent's operation.
func (s *sitScenario) sitTouchedSetup() (human, generationH, operation string) {
	s.t.Helper()
	human = s.humanSession()
	s.send("Set item 6 to 1 and mark item 7 done", true)
	call := s.agent()
	generation := s.acquire(call)
	applied := s.tool(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "touched", "commands": sitCommands(
		sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B7", "input": "1"}),
		sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}),
	)})
	var result struct {
		OperationID string `json:"operation_id"`
	}
	if json.Unmarshal([]byte(applied), &result) != nil || result.OperationID == "" || s.head() != 2 {
		s.t.Fatalf("agent apply: %s", applied)
	}
	s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})
	return human, s.humanLease(human), result.OperationID
}

// Undo by the addresses the person's save names (X-Craft-Touched). The person saves version 3 with
// only Other!A3 changed; their touching of L!B8 (retyped to the agent's input, gap (c)) only the
// header tells:
//   - ['L'!B8]: B8 stays the agent's, B7 comes back; restored 1, skipped 1;
//   - "unknown": every cell after the version is held, nothing comes back;
//   - only Other!A3: both come back;
//   - no header: no touched row, the raw-input comparison decides (both come back here).
func TestSheetITUndoByTheAddressesTheSaveTouched(t *testing.T) {
	for _, tc := range []struct {
		name              string
		touched           *string
		b7, b8            string
		restored, skipped int
		touchedRow        bool
	}{
		{"one cell", sitPtr(`["'L'!B8","'Other'!A3"]`), "open", "done", 1, 1, true},
		{"unknown", sitPtr(`unknown`), "1", "done", 0, 2, true},
		{"only another sheet", sitPtr(`["'Other'!A3"]`), "open", "open", 2, 0, true},
		{"no header", nil, "open", "open", 2, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSheetITScenario(t)
			human, generationH, operation := s.sitTouchedSetup()
			headers := map[string]string{}
			if tc.touched != nil {
				headers["X-Craft-Touched"] = *tc.touched
			}
			sheets := s.headSheets(human)
			sheets["Other"]["A3"] = "X3"
			body := sitWorkbook(sheets["L"], sheets["Other"])
			if w := s.humanSaveWith(human, generationH, "2", body, "sit-touched-"+strings.ReplaceAll(tc.name, " ", "-"), "", headers); w.Code != 200 || s.head() != 3 {
				t.Fatalf("the person's save: %d %s", w.Code, w.Body)
			}
			var row *sitOpsRow
			for _, r := range s.opsRows() {
				if r.OperationID == "touched:3" {
					r := r
					row = &r
				}
			}
			if (row != nil) != tc.touchedRow {
				t.Fatalf("touched: row touched:3 present=%v, want %v: %+v", row != nil, tc.touchedRow, s.opsRows())
			}
			if row != nil && (row.Kind != "human" || row.Replayable || strings.Contains(string(row.Ops), "open") || strings.Contains(string(row.Ops), "done")) {
				t.Errorf("the touched row is not a human, non-replayable list of addresses only: %+v", *row)
			}
			w := s.undo(human, generationH, operation)
			var answer struct {
				Restored *int `json:"restored"`
				Skipped  *int `json:"skipped"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &answer)
			got := s.headSheets(human)["L"]
			if got["B7"] != tc.b7 || got["B8"] != tc.b8 {
				t.Errorf("touched %s: after undo B7=%q B8=%q, want %q %q (%d %s)", tc.name, got["B7"], got["B8"], tc.b7, tc.b8, w.Code, w.Body)
			}
			if answer.Restored == nil || answer.Skipped == nil || *answer.Restored != tc.restored || *answer.Skipped != tc.skipped {
				t.Errorf("touched %s: undo answer %s, want restored %d skipped %d", tc.name, w.Body, tc.restored, tc.skipped)
			}
		})
	}
}

// A malformed X-Craft-Touched stores nothing: 400, the head and the operations unchanged. A sheet
// the workbook does not have is no malformed input (see TestSheetITTouchedOnASheetNotInTheWorkbook).
func TestSheetITInvalidTouchedStoresNothing(t *testing.T) {
	for name, header := range map[string]string{
		"no sheet":       `["B8"]`,
		"not an address": `["'L'!ZZZZZ0"]`,
		"not json":       `B8`,
		"too many":       `[` + strings.TrimSuffix(strings.Repeat(`"'L'!A1",`, 2001), ",") + `]`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newSheetITScenario(t)
			human, generationH, _ := s.sitTouchedSetup()
			before := len(s.opsRows())
			sheets := s.headSheets(human)
			sheets["Other"]["A3"] = "X3"
			body := sitWorkbook(sheets["L"], sheets["Other"])
			w := s.humanSaveWith(human, generationH, "2", body, "sit-bad-"+strings.ReplaceAll(name, " ", "-"), "", map[string]string{"X-Craft-Touched": header})
			if w.Code != 400 || s.head() != 2 || len(s.opsRows()) != before {
				t.Errorf("touched %s: %d %.200s, head %d, ops %d→%d", name, w.Code, w.Body, s.head(), before, len(s.opsRows()))
			}
		})
	}
}

// An address on a sheet the workbook has neither in the new nor in the previous version never refuses
// the person's save: it is stored, its touched row is "*" (unknown) and undo holds every cell. An
// address on a sheet the person deleted in this version (it is in the previous one) stays as sent,
// and the agent's cells on L come back.
func TestSheetITTouchedOnASheetNotInTheWorkbook(t *testing.T) {
	for _, tc := range []struct {
		name              string
		touched           string
		deleteOther       bool
		wantStar          bool
		b7, b8            string
		restored, skipped int
	}{
		{"no such sheet", `["'Nope'!B8"]`, false, true, "1", "done", 0, 2},
		{"sheet deleted in this version", `["'Other'!A3"]`, true, false, "open", "open", 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSheetITScenario(t)
			human, generationH, operation := s.sitTouchedSetup()
			sheets := s.headSheets(human)
			var body []byte
			if tc.deleteOther {
				body = sitWorkbookOnly(sheets["L"])
			} else {
				sheets["Other"]["A3"] = "X3"
				body = sitWorkbook(sheets["L"], sheets["Other"])
			}
			attempt := "sit-touched-" + strings.ReplaceAll(tc.name, " ", "-")
			if w := s.humanSaveWith(human, generationH, "2", body, attempt, "", map[string]string{"X-Craft-Touched": tc.touched}); w.Code != 200 || s.head() != 3 {
				t.Fatalf("touched %s: the person's save is refused: %d %.200s, head %d", tc.name, w.Code, w.Body, s.head())
			}
			var row *sitOpsRow
			for _, r := range s.opsRows() {
				if r.OperationID == "touched:3" {
					r := r
					row = &r
				}
			}
			if row == nil {
				t.Fatalf("touched %s: no touched row: %+v", tc.name, s.opsRows())
			}
			star := strings.Contains(string(row.Ops), `"*"`)
			if star != tc.wantStar || (!tc.wantStar && !strings.Contains(string(row.Ops), "Other")) {
				t.Errorf("touched %s: touched ops %s, want unknown %v", tc.name, row.Ops, tc.wantStar)
			}
			w := s.undo(human, generationH, operation)
			var answer struct {
				Restored *int `json:"restored"`
				Skipped  *int `json:"skipped"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &answer)
			got := s.headSheets(human)["L"]
			if got["B7"] != tc.b7 || got["B8"] != tc.b8 || answer.Restored == nil || *answer.Restored != tc.restored || answer.Skipped == nil || *answer.Skipped != tc.skipped {
				t.Errorf("touched %s: after undo B7=%q B8=%q (want %q %q), answer %d %s (want restored %d skipped %d)", tc.name, got["B7"], got["B8"], tc.b7, tc.b8, w.Code, w.Body, tc.restored, tc.skipped)
			}
		})
	}
}

func sitPtr(s string) *string { return &s }

// sitWorkbookOnly is sitWorkbook after the person deleted the sheet Other (and the pivot cache over it).
func sitWorkbookOnly(l sitCells) []byte {
	var parts []sitPart
	for _, p := range sitBaseParts() {
		switch p.name {
		case "xl/pivotCache/pivotCacheDefinition2.xml":
			continue
		case "[Content_Types].xml":
			p.body = strings.Replace(p.body, `<Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, "", 1)
		case "xl/workbook.xml":
			p.body = strings.Replace(strings.Replace(p.body, `<sheet name="Other" sheetId="2" r:id="rId2"/>`, "", 1), `<pivotCache cacheId="2" r:id="rId5"/>`, "", 1)
		case "xl/_rels/workbook.xml.rels":
			p.body = strings.Replace(strings.Replace(p.body, `<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>`, "", 1), `<Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/pivotCacheDefinition" Target="pivotCache/pivotCacheDefinition2.xml"/>`, "", 1)
		}
		parts = append(parts, p)
	}
	parts = append(parts, sitPart{"xl/worksheets/sheet1.xml", sitSheetXML(l)})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			panic(err)
		}
		_, _ = w.Write([]byte(p.body))
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
