package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftsession"
)

// sitAdapter is the production session adapter (with the production open check) over sitEngine.
func sitAdapter(engine *sitEngine) *CraftNativeSheetAgentAdapter {
	return NewCraftNativeSheetAgentAdapter(engine, WithCraftNativeSheetOpenCheck(CraftNativeSheetSnapshotKeepsEverything))
}

var sitProfile = CraftNativeDocumentProfile{Application: "gridcraft"}

// The first read of a workbook Sheet goes through the same adapter the server wires
// (nativeCraftAgentEngine: WithCraftNativeSheetOpenCheck(CraftNativeSheetSnapshotKeepsEverything)).
// Apply skips that check for a workbook file; Inspect does not, and the check parses both sides as
// native JSON, so every read of a workbook Sheet the engine has not just written is refused.
func TestSheetITInspectOfAWorkbookSheetPassesTheProductionOpenCheck(t *testing.T) {
	v1 := sitWorkbook(sitInitialL(), sitInitialOther())
	for _, query := range []string{`{"command":"document.inspect","params":{}}`, `{"command":"cell.get","params":{"cell":"B1","sheet":"L"}}`, `{"command":"sheet.read","params":{"range":"A1:B3","sheet":"L"}}`} {
		out, err := sitAdapter(&sitEngine{}).Inspect(t.Context(), sitProfile, v1, json.RawMessage(query))
		if err != nil {
			t.Errorf("BUG %s of a workbook Sheet refused by the open check (craft_native_sheet_session.go Inspect→checkOpened→CraftNativeSheetSnapshotKeepsEverything parses the xlsx as JSON): %v", query, err)
			continue
		}
		if !strings.Contains(string(out), `"result"`) {
			t.Errorf("%s: %s", query, out)
		}
	}
}

// An xlsm with a VBA project: an agent change of
// one cell keeps every part it did not change byte for byte (by SHA-256 of the part).
func TestSheetITAgentChangeKeepsVBAByteForByte(t *testing.T) {
	v1 := sitWorkbook(sitInitialL(), sitInitialOther())
	out, err := sitAdapter(&sitEngine{}).Apply(t.Context(), sitProfile, v1, json.RawMessage(`[{"command":"cell.set","params":{"cell":"B8","input":"done","sheet":"L"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	before, after := sitParts(t, v1), sitParts(t, out)
	for _, name := range []string{"xl/vbaProject.bin", "[Content_Types].xml", "xl/worksheets/sheet2.xml", "customXml/item1.xml", "customXml/itemProps1.xml", "customXml/_rels/item1.xml.rels"} {
		if sitSHA(after[name]) != sitSHA(before[name]) {
			t.Errorf("part %s changed", name)
		}
	}
	if sitParseSheet(string(after["xl/worksheets/sheet1.xml"]))["B8"] != "done" {
		t.Fatal("the change is not in the file")
	}
}

// The engine says nothing is lost (parts=[]), yet its file has no VBA project or no custom XML. The server must compare the parts of the base with the candidate
// itself and refuse such a change as fidelity_loss (422), whatever the engine reports: the agent
// is told fidelity_loss, nothing is stored, no operations row is written. Where the server checks
// (adapter, applyTool, store) is open; this test is the whole path.
func TestSheetITAgentChangeThatDropsVBAIsNeverStored(t *testing.T) {
	for _, dropped := range []string{"xl/vbaProject.bin", "customXml/item1.xml", "customXml/itemProps1.xml"} {
		t.Run(dropped, func(t *testing.T) {
			s := newSheetITScenario(t)
			s.wb.drop = map[string]bool{dropped: true}
			s.send("Mark item 7 done", true)
			call := s.agent()
			generation := s.acquire(call)
			text, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "sit-drop", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
			if !refused || !strings.Contains(text, "fidelity_loss") || s.head() != 1 || len(s.opsRows()) != 0 {
				t.Errorf("a candidate without %s (engine parts=[]) is not refused as fidelity_loss 422: refused=%v head %d ops %d %.160s", dropped, refused, s.head(), len(s.opsRows()), text)
			}
		})
	}
	// Control: the same path without a dropped part stores the change, so the refusal above is the
	// missing part and nothing else.
	s := newSheetITScenario(t)
	s.send("Mark item 7 done", true)
	call := s.agent()
	generation := s.acquire(call)
	s.tool(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "sit-keep", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
	if s.head() != 2 {
		t.Fatalf("control change not stored: head %d", s.head())
	}
	// A loss the engine reports is refused and named (adapter level).
	reported := &craftsession.FidelityLossError{Parts: []craftsession.FidelityPart{{Code: "vba_project", Count: 1}}}
	if _, err := sitAdapter(&sitEngine{loss: reported}).Apply(t.Context(), sitProfile, sitWorkbook(sitInitialL(), sitInitialOther()), json.RawMessage(`[{"command":"cell.set","params":{"cell":"B8","input":"done","sheet":"L"}}]`)); !errors.As(err, new(*craftsession.FidelityLossError)) {
		t.Fatalf("a reported loss came back as %v", err)
	}
}

// Undo of an agent change: POST …/craft/document/undo {operation_id} answers {version, skipped}. The
// contract names the route; the server must serve it and the API spec describe it.
func TestSheetITUndoRouteIsServedAndDescribed(t *testing.T) {
	routes, err := os.ReadFile("craft_native_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(routes), `r.authedMut("POST", "/api/v1/pages/{slug}/craft/document/undo", roleSelf, d.undoOperation)`) {
		t.Errorf("Undo must be registered as a self-scoped mutation")
	}
	spec, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if json.Unmarshal(spec, &doc) != nil {
		t.Fatal("openapi.gen.json does not parse")
	}
	if _, ok := doc.Paths["/api/v1/pages/{slug}/craft/document/undo"]["post"]; !ok {
		t.Errorf("GAP the OpenAPI spec has no POST /api/v1/pages/{slug}/craft/document/undo")
	}
}

const (
	sitFidelitySheet = "xl/worksheets/sheet1.xml"
	sitFidelityProps = `<autoFilter ref="A1:B9"/><customProperties><customPr name="Synthetic" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" r:id="rIdProperty"/></customProperties>`
)

func sitFidelityWorkbook(t *testing.T, format string) []byte {
	t.Helper()
	l := sitInitialL()
	l["C8"] = "=L!B8|open"
	base := sitWorkbook(l, sitInitialOther())
	parts := sitParts(t, base)
	types := string(parts["[Content_Types].xml"])
	types = strings.Replace(types, "</Types>", `<Override PartName="/xl/customProperty1.bin" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.customProperty"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`, 1)
	book := strings.Replace(string(parts["xl/workbook.xml"]), "</workbook>", `<definedNames><definedName name="_xlnm._FilterDatabase" localSheetId="0" hidden="1">L!$A$1:$B$9</definedName><definedName name="Synthetic_XML" hidden="1">"&lt;synthetic&gt;
  data
&lt;/synthetic&gt;"</definedName><definedName name="MacroReference">Module1.SyntheticHelp()</definedName><definedName name="HeaderText">'L'!$A$1 &amp; 'L'!$B$1</definedName></definedNames></workbook>`, 1)
	rels := string(parts["xl/_rels/workbook.xml.rels"])
	rels = strings.Replace(rels, "</Relationships>", `<Relationship Id="rIdStyle" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`, 1)
	drop := ""
	if format == "xlsx" {
		types = strings.Replace(types, "application/vnd.ms-excel.sheet.macroEnabled.main+xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml", 1)
		types = strings.Replace(types, `<Default Extension="bin" ContentType="application/vnd.ms-office.vbaProject"/>`, "", 1)
		rels = strings.Replace(rels, `<Relationship Id="rId3" Type="http://schemas.microsoft.com/office/2006/relationships/vbaProject" Target="vbaProject.bin"/>`, "", 1)
		drop = "xl/vbaProject.bin"
	}
	return workbookEdited(t, base, drop, map[string]string{
		"[Content_Types].xml":                 types,
		"xl/workbook.xml":                     book,
		"xl/_rels/workbook.xml.rels":          rels,
		sitFidelitySheet:                      strings.Replace(string(parts[sitFidelitySheet]), "</worksheet>", sitFidelityProps+"</worksheet>", 1),
		"xl/customProperty1.bin":              "\x00synthetic worksheet property\xff",
		"xl/worksheets/_rels/sheet1.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdProperty" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/customProperty" Target="../customProperty1.bin"/></Relationships>`,
		"xl/styles.xml":                       `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dxfs count="1"><dxf><font><b/></font></dxf></dxfs><tableStyles count="1" defaultTableStyle="SyntheticTable"><tableStyle name="SyntheticTable" pivot="0" table="1" count="1"><tableStyleElement type="headerRow" dxfId="0"/></tableStyle></tableStyles></styleSheet>`,
	})
}

func TestSheetITWorkbookFidelityThroughApplyAndUndo(t *testing.T) {
	for _, format := range []string{"xlsx", "xlsm"} {
		t.Run(format, func(t *testing.T) {
			initial := sitFidelityWorkbook(t, format)
			s := newSheetITScenario(t, initial)
			s.send("Mark item 7 done", true)
			call := s.agent()
			generation := s.acquire(call)
			raw := s.tool(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "fidelity-apply",
				"commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
			var receipt struct {
				Version     int64  `json:"version"`
				OperationID string `json:"operation_id"`
			}
			if json.Unmarshal([]byte(raw), &receipt) != nil || receipt.Version != 2 || receipt.OperationID == "" {
				t.Fatalf("apply: %s", raw)
			}
			s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})
			human := s.humanSession()
			if w := s.undo(human, s.humanLease(human), receipt.OperationID); w.Code != 201 || s.head() != 3 {
				t.Fatalf("undo: %d %s, head %d", w.Code, w.Body, s.head())
			}
			before := sitParts(t, initial)
			for version, expected := range map[int64]string{1: "open", 2: "done", 3: "open"} {
				p := sitParts(t, s.stored(version))
				if len(p) != len(before) {
					t.Fatalf("v%d: %d parts, want %d", version, len(p), len(before))
				}
				for name, data := range before {
					if name != sitFidelitySheet && !bytes.Equal(p[name], data) {
						t.Errorf("v%d: %s changed", version, name)
					}
				}
				sheet := string(p[sitFidelitySheet])
				cells := sitParseSheet(sheet)
				if !strings.Contains(sheet, sitFidelityProps) || cells["B8"] != expected || cells["C8"] != "=L!B8|"+expected {
					t.Errorf("v%d: property/filter preserved=%t, B8=%q, C8=%q", version, strings.Contains(sheet, sitFidelityProps), cells["B8"], cells["C8"])
				}
			}
		})
	}
}

func TestWorkbookFidelityRejectsUnprovableChanges(t *testing.T) {
	base := sitFidelityWorkbook(t, "xlsm")
	p := sitParts(t, base)
	edit := func(part, old, new string) []byte {
		t.Helper()
		original := string(p[part])
		if !strings.Contains(original, old) {
			t.Fatalf("fixture %s lacks %q", part, old)
		}
		return workbookEdited(t, base, "", map[string]string{part: strings.Replace(original, old, new, 1)})
	}
	var scope craftWorkbookEdits
	scope.add("'L'!B8")
	for _, tc := range []struct {
		name string
		body []byte
		code string
	}{
		{"binary property", workbookEdited(t, base, "xl/customProperty1.bin", nil), "package_parts"},
		{"worksheet relationship", workbookEdited(t, base, "xl/worksheets/_rels/sheet1.xml.rels", nil), "relationships"},
		{"content type", edit("[Content_Types].xml", "spreadsheetml.customProperty", "spreadsheetml.unrecognized"), "content_types"},
		{"property reference", edit(sitFidelitySheet, sitFidelityProps, `<autoFilter ref="A1:B9"/>`), "worksheet"},
		{"filter scope", edit(sitFidelitySheet, `ref="A1:B9"`, `ref="A1:B8"`), "worksheet"},
		{"table style", edit("xl/styles.xml", `name="SyntheticTable"`, `name="ChangedTable"`), "styles"},
		{"cell style", edit(sitFidelitySheet, `r="B8"`, `r="B8" s="1"`), "worksheet"},
		{"defined name scope", edit("xl/workbook.xml", `localSheetId="0"`, `localSheetId="1"`), "workbook"},
		{"defined name removed", edit("xl/workbook.xml", `<definedName name="_xlnm._FilterDatabase" localSheetId="0" hidden="1">L!$A$1:$B$9</definedName>`, ""), "workbook"},
		{"defined name whitespace", edit("xl/workbook.xml", "  data", "data"), "workbook"},
		{"defined name macro", edit("xl/workbook.xml", "Module1.SyntheticHelp()", "MODULE1.SYNTHETICHELP()"), "workbook"},
		{"formula definition", edit(sitFidelitySheet, "<f>L!B8</f>", "<f>L!B7</f>"), "worksheet"},
		{"formula cache removed", edit(sitFidelitySheet, "</f><v>open</v>", "</f>"), "worksheet"},
		{"formula cache type changed", edit(sitFidelitySheet, `r="C8" t="str"`, `r="C8" t="e"`), "worksheet"},
		{"unrelated cell", edit(sitFidelitySheet, `r="B7" t="inlineStr"><is><t>open`, `r="B7" t="inlineStr"><is><t>changed`), "worksheet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lost := craftWorkbookPartsLost(base, tc.body, scope)
			if len(lost) != 1 || lost[0].Code != tc.code {
				t.Fatalf("losses: %+v, want %s", lost, tc.code)
			}
		})
	}
	if lost := craftWorkbookPartsLost([]byte("PK bad base"), base, scope); len(lost) != 1 || lost[0].Code != "package" {
		t.Fatalf("unreadable base: %+v", lost)
	}
}

func TestSheetITWorkbookFidelityRefusalLeavesHeadAndUndoUnchanged(t *testing.T) {
	base := sitFidelityWorkbook(t, "xlsm")
	s := newSheetITScenario(t, base)
	s.send("Mark item 7 done", true)
	call := s.agent()
	generation := s.acquire(call)
	args := map[string]any{"generation": generation, "base_version": "1", "request_id": "fidelity-reject",
		"commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))}
	s.wb.drop = map[string]bool{"xl/customProperty1.bin": true}
	text, refused := s.toolCall(call, "craft_sheet_apply", args)
	if !refused || !strings.Contains(text, "fidelity_loss") || s.head() != 1 || len(s.opsRows()) != 0 {
		t.Fatalf("lossy apply: %t %s, head %d", refused, text, s.head())
	}
	s.wb.drop = nil
	args["request_id"] = "fidelity-accepted"
	raw := s.tool(call, "craft_sheet_apply", args)
	var receipt struct {
		OperationID string `json:"operation_id"`
	}
	if json.Unmarshal([]byte(raw), &receipt) != nil || receipt.OperationID == "" || s.head() != 2 {
		t.Fatalf("apply: %s", raw)
	}
	s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})
	human := s.humanSession()
	s.wb.edit = func(part, body string) string {
		return strings.Replace(body, sitFidelityProps, "", 1)
	}
	if w := s.undo(human, s.humanLease(human), receipt.OperationID); w.Code != 422 || !strings.Contains(w.Body.String(), "fidelity_loss") || s.head() != 2 || len(s.opsRows()) != 1 {
		t.Fatalf("lossy undo: %d %s, head %d", w.Code, w.Body, s.head())
	}
}

func TestSheetITWorkbookFidelityRefusesSignedChanges(t *testing.T) {
	for part, code := range map[string]string{
		"_xmlsignatures/sig1.xml":                   "xml_signatures",
		"xl/vbaProjectSignatureAgile.bin":           "vba_signature",
		"signatures/nonstandard-package-origin.bin": "xml_signatures",
	} {
		t.Run(code+"/"+part, func(t *testing.T) {
			base := sitFidelityWorkbook(t, "xlsm")
			extra := map[string]string{part: "synthetic signature data"}
			if strings.HasPrefix(part, "signatures/") {
				types := string(sitParts(t, base)["[Content_Types].xml"])
				extra["[Content_Types].xml"] = strings.Replace(types, "</Types>", `<Override PartName="/`+part+`" ContentType="application/vnd.openxmlformats-package.digital-signature-origin"/></Types>`, 1)
			}
			s := newSheetITScenario(t, workbookEdited(t, base, "", extra))
			s.send("Mark item 7 done", true)
			call := s.agent()
			generation := s.acquire(call)
			_, writes := s.wb.count()
			text, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "fidelity-signed",
				"commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
			_, currentWrites := s.wb.count()
			if !refused || !strings.Contains(text, "fidelity_loss") || !strings.Contains(text, code) ||
				!strings.Contains(text, "signed workbook") || s.head() != 1 || len(s.opsRows()) != 0 || currentWrites != writes {
				t.Fatalf("signed write: %t %s, head %d, engine writes %d → %d", refused, text, s.head(), writes, currentWrites)
			}
		})
	}
}
