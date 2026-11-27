package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The agent names a sheet either way, params.sheet or in the address ('L'!B8, L!B8); both reach
// the engine and the log as 'L'!B8, and two different sheets in one write are refused.
func TestAgentWritesNameTheirSheetEitherWay(t *testing.T) {
	p := CraftNativeDocumentProfile{Application: "gridcraft"}
	raw := []byte("PK\x03\x04 before")
	engine := &fakeSheetEngine{session: &fakeSheetSession{snapshot: []byte("PK\x03\x04 after")}}
	commands := json.RawMessage(`[{"command":"cell.set","params":{"cell":"'L'!C1","input":"a"}},{"command":"range.clear","params":{"range":"L!B8:B9"}},{"command":"range.setValues","params":{"range":"'Loan book'!A1:A1","values":[[1]],"sheet":"Loan book"}}]`)
	if _, err := NewCraftNativeSheetAgentAdapter(engine).Apply(context.Background(), p, raw, commands); err != nil {
		t.Fatal(err)
	}
	if want := "set 'L'!C1=a|clear 'L'!B8:B9|write 'Loan book'!A1|snapshot"; strings.Join(engine.session.calls, "|") != want {
		t.Fatalf("calls %v", engine.session.calls)
	}
	ops, ok := craftNativeAgentResolvedOps(commands)
	if !ok || !strings.Contains(string(ops), `"cell":"'L'!C1"`) || !strings.Contains(string(ops), `"range":"'L'!B8:B9"`) {
		t.Fatalf("ops %v %s", ok, ops)
	}
	if _, err := nativeAgentEngineCommandsFor("gridcraft", json.RawMessage(`[{"command":"cell.set","params":{"cell":"'L'!C1","input":"a","sheet":"M"}}]`), false, nativeAgentEngineRules{}); err == nil {
		t.Fatal("two sheets admitted")
	}
}

// range.columnByHeader finds every column under a header (a query, so also in read-only), and a
// workbook Sheet is read without the native JSON open check.
func TestAgentFindsAColumnByItsHeaderInAWorkbook(t *testing.T) {
	session := &fakeSheetSession{columns: json.RawMessage(`{"columns":[{"sheet":"L","headerCell":"B7","range":"B8:B577"}]}`)}
	checked := 0
	a := NewCraftNativeSheetAgentAdapter(&fakeSheetEngine{session: session}, WithCraftNativeSheetOpenCheck(func(base, snapshot []byte) error { checked++; return errors.New("not JSON") }))
	out, err := a.Inspect(context.Background(), CraftNativeDocumentProfile{Application: "gridcraft"}, []byte("PK\x03\x04"), json.RawMessage(`{"command":"range.columnByHeader","params":{"header":"Loan ID","matchCase":false}}`))
	if err != nil || checked != 0 || !strings.Contains(string(out), `"headerCell":"B7"`) || !strings.Contains(string(out), `"command":"range.columnByHeader"`) {
		t.Fatalf("inspect %s %v, checks %d", out, err, checked)
	}
	if strings.Join(session.calls, "|") != "header Loan ID sheet= case=false" {
		t.Fatalf("calls %v", session.calls)
	}
	for _, bad := range []string{`{"command":"range.columnByHeader","params":{}}`, `{"command":"range.columnByHeader","params":{"header":""}}`, `{"command":"range.columnByHeader","params":{"header":"x","extra":1}}`} {
		if _, err := nativeAgentEngineCommandsFor("gridcraft", json.RawMessage(bad), true, nativeAgentEngineRules{}); err == nil {
			t.Errorf("admitted %s", bad)
		}
	}
}
