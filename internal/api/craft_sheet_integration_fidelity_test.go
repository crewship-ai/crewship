package api

import (
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

// An xlsm with a VBA project, its two signatures and an XML package signature: an agent change of
// one cell keeps every part it did not change byte for byte (by SHA-256 of the part).
func TestSheetITAgentChangeKeepsVBAAndSignaturesByteForByte(t *testing.T) {
	v1 := sitWorkbook(sitInitialL(), sitInitialOther())
	out, err := sitAdapter(&sitEngine{}).Apply(t.Context(), sitProfile, v1, json.RawMessage(`[{"command":"cell.set","params":{"cell":"B8","input":"done","sheet":"L"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	before, after := sitParts(t, v1), sitParts(t, out)
	for _, name := range []string{"xl/vbaProject.bin", "xl/vbaProjectSignature.bin", "xl/vbaProjectSignatureAgile.bin", "xl/_rels/vbaProject.bin.rels", "_xmlsignatures/sig1.xml", "_xmlsignatures/origin.sigs", "_xmlsignatures/_rels/origin.sigs.rels", "[Content_Types].xml", "xl/worksheets/sheet2.xml", "customXml/item1.xml", "customXml/itemProps1.xml", "customXml/_rels/item1.xml.rels"} {
		if sitSHA(after[name]) != sitSHA(before[name]) {
			t.Errorf("part %s changed", name)
		}
	}
	if sitParseSheet(string(after["xl/worksheets/sheet1.xml"]))["B8"] != "done" {
		t.Fatal("the change is not in the file")
	}
}

// The engine says nothing is lost (parts=[]), yet its file has no VBA project, no VBA or package
// signature, or no custom XML. The server must compare the parts of the base with the candidate
// itself and refuse such a change as fidelity_loss (422), whatever the engine reports: the agent
// is told fidelity_loss, nothing is stored, no operations row is written. Where the server checks
// (adapter, applyTool, store) is open; this test is the whole path.
func TestSheetITAgentChangeThatDropsVBAIsNeverStored(t *testing.T) {
	for _, dropped := range []string{"xl/vbaProject.bin", "xl/vbaProjectSignature.bin", "xl/vbaProjectSignatureAgile.bin", "_xmlsignatures/sig1.xml", "customXml/item1.xml", "customXml/itemProps1.xml"} {
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
