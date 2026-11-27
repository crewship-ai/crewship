package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// A workbook kept as xlsm is never saved as xlsx: the change of format drops the
// macros, so it is refused as fidelity_loss and nothing is stored.
func TestWorkbookSaveAsXlsxOverAnXlsmIsRefused(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	xlsx := craftTestWorkbook(t, "xlsx", "edited")
	w := rig.save(xlsx, "1", workbookHash(xlsx))
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"fidelity_loss"`) || !strings.Contains(w.Body.String(), `"vba_project"`) {
		t.Fatalf("xlsm saved as xlsx: %d %s", w.Code, w.Body)
	}
	if doc := workbookRead(t, rig.read(nil)); doc.Version != "1" {
		t.Fatalf("a refused save was stored: %s", doc.Version)
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
