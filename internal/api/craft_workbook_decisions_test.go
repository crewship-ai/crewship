package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// A workbook kept as xlsm is never saved as xlsx: the change of format drops the
// macros, so it is refused as fidelity_loss and nothing is stored.
func TestWorkbookSaveAsXlsxOverAnXlsmIsRefused(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	xlsx := craftTestWorkbook(t, "xlsx", "edited")
	w := rig.save(xlsx, mooring.WorkbookMediaTypeXLSX, "1", workbookHash(xlsx), int64(len(xlsx)))
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"fidelity_loss"`) || !strings.Contains(w.Body.String(), `"vba_project"`) {
		t.Fatalf("xlsm saved as xlsx: %d %s", w.Code, w.Body)
	}
	if read := rig.read(nil); read.Header().Get("X-Craft-Version") != "1" {
		t.Fatalf("a refused save was stored: %v", read.Header())
	}
}

// A change of a workbook with an XML package signature keeps the signature byte for byte but says it
// no longer matches: the save's receipt and the version in the operations listing carry
// signature_invalidated; a workbook without a signature never does.
func TestSignedWorkbookChangeSaysTheSignatureNoLongerMatches(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	signed := workbookEdited(t, rig.original, "", map[string]string{"_xmlsignatures/sig1.xml": "<Signature/>", "_xmlsignatures/origin.sigs": ""})
	w := rig.save(signed, mooring.WorkbookMediaTypeXLSM, "1", workbookHash(signed), int64(len(signed)))
	if w.Code != 200 || strings.Contains(w.Body.String(), "signature_invalidated") {
		t.Fatalf("signing an unsigned workbook: %d %s", w.Code, w.Body)
	}
	changed := workbookEdited(t, signed, "", map[string]string{"xl/worksheets/sheet1.xml": `<worksheet><sheetData/></worksheet>`})
	w = rig.save(changed, mooring.WorkbookMediaTypeXLSM, "2", workbookHash(changed), int64(len(changed)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"signature_invalidated":true`) {
		t.Fatalf("change of a signed workbook: %d %s", w.Code, w.Body)
	}
	ops := rig.operations("1")
	var list struct {
		Versions []map[string]any `json:"versions"`
	}
	if ops.Code != 200 || json.Unmarshal(ops.Body.Bytes(), &list) != nil || len(list.Versions) != 2 {
		t.Fatalf("operations %d %s", ops.Code, ops.Body)
	}
	if _, ok := list.Versions[0]["signature_invalidated"]; ok || list.Versions[1]["signature_invalidated"] != true {
		t.Fatalf("operations versions %v", list.Versions)
	}
}

// formulas.calculateNow is in the agent's command list: the recalculation is an effect of the write,
// so it is not logged as an operation and the batch stays replayable. A batch of it alone changes
// nothing and is refused.
func TestAgentCalculateNowIsAnEffectOfTheWrite(t *testing.T) {
	batch := json.RawMessage(`[{"command":"cell.set","params":{"cell":"B8","input":"done","sheet":"L"}},{"command":"formulas.calculateNow","params":{}}]`)
	ops, ok := craftNativeAgentResolvedOps(batch)
	if !ok || string(ops) != `[{"command":"cell.set","params":{"cell":"'L'!B8","input":"done"}}]` {
		t.Fatalf("ops %v %s", ok, ops)
	}
	if _, err := nativeAgentEngineCommandsFor("gridcraft", batch, false, nativeAgentEngineRules{}); err != nil {
		t.Fatalf("calculateNow refused: %v", err)
	}
	if _, err := nativeAgentEngineCommandsFor("gridcraft", json.RawMessage(`[{"command":"formulas.calculateNow","params":{}}]`), false, nativeAgentEngineRules{}); err == nil {
		t.Fatal("a batch that changes nothing was taken")
	}
	if _, err := sitAdapter(&sitEngine{}).Apply(t.Context(), sitProfile, sitWorkbook(sitInitialL(), sitInitialOther()), batch); err != nil {
		t.Fatalf("apply with calculateNow: %v", err)
	}
}

// An agent change of a workbook with an XML package signature (the test workbook has one) says the
// signature no longer matches, in the apply result and in the operations listing.
func TestAgentChangeOfASignedWorkbookSaysTheSignatureNoLongerMatches(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Mark item 7 done", true)
	call := s.agent()
	generation := s.acquire(call)
	applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "signed-1", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
	if refused || !strings.Contains(applied, `"signature_invalidated":true`) {
		t.Fatalf("apply result: %v %s", refused, applied)
	}
	code, _, raw := s.operations(s.humanSession(), "1")
	if code != 200 || !strings.Contains(raw, `"signature_invalidated":true`) {
		t.Fatalf("operations from 1: %d %s", code, raw)
	}
}
