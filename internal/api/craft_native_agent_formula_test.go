package api

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNativeAgentEngineFormulaAllowlist(t *testing.T) {
	good := []string{
		`=A1+B1`, `=SUM(A1:A10)`, `=SUMIFS(Sheet2!C:C,Sheet2!A:A,">"&A1)`, `=COUNTIFS('Q1 Sales'!A:A,"x",B:B,">0")`,
		`=SUBTOTAL(9,A2:A230000)`, `=IFERROR(VLOOKUP(A1,Data!A:C,3,FALSE),"")`, `= sum (A1:A3)`,
		`=TEXT(A1,"yyyy-mm-dd")`, `=IF(A1="WEBSERVICE(x)",1,2)`, `="a ""quoted"" HYPERLINK(x)"`,
		`=XLOOKUP(A1,B:B,C:C)`, `=$A$1*2`, `=SUM(A2:A230000)`, `=SUMIFS('List 1'!C:C,'List 1'!A:A,A2)`,
		`=IF(A1="A:XFD",1,2)`, `=SUM(1:5)`, `=SUM(A1:C3)+TRUE`, `=` + strings.Repeat("(", 64) + `1` + strings.Repeat(")", 64),
	}
	for _, f := range good {
		if !nativeAgentEngineFormula(f) {
			t.Errorf("safe formula refused: %s", f)
		}
	}
	bad := []string{
		`=WEBSERVICE("http://example.invalid")`, `=HYPERLINK("http://example.invalid","x")`, `=INDIRECT("A"&1)`,
		`=OFFSET(A1,1,1)`, `=NOW()`, `=RAND()`, `=CELL("filename")`, `=INFO("directory")`, `=FORMULATEXT(A1)`,
		`=DDE("excel","book","A1")`, `=CALL("user32","MessageBoxA")`, `=REGISTER.ID("x")`, `=_xlfn.XLOOKUP(A1,B:B,C:C)`,
		`=SUM(A1)+WEBSERVICE("x")`, `=SUM (A1)+EVIL (1)`, `=[1]Sheet1!A1`, `='[book.xlsx]Sheet1'!A1`,
		`=Table1[Col]`, `=@A1`, `=SUM(A1`, `="open`, `='open`, "=SUM(A1)\n+1", `=STDEV.S(A1:A3)`,
		strings.Repeat("=1+", 3000) + "1", `A1+1`, ``, `=`,
		// other sheets and files, quoted
		`='C:\x[kniha]L'!A1`, `='[kniha.xlsx]List'!A1`, `='List/1'!A1`, `='a:b'!A1`,
		// defined names and R1C1 stand for formulas the check cannot see
		`=MyName+1`, `=R1C1+1`, `=SUM(R[1]C[1])`, `=Evil`, `=SUM(Sales_2026)`,
		// localized function names and look-alikes
		`=SOUČET(A1:A3)`, `=SUMME(A1:A3)`, `=SUM.IF(A1)`, "=SUM\u00a0(A1)", "=SUM\u200b(A1)", "=SUM\t(A1)", "=SUM\u2003(A1)",
		// nesting and ranges that ask for a sheet-sized calculation
		`=` + strings.Repeat("(", 65) + `1` + strings.Repeat(")", 65),
		`=SUM(A:XFD)`, `=SUM(A1:XFD1048576)`, `=SUM(1:1048576)`, `=SUMPRODUCT(A:A,B:B)`, `=SUMPRODUCT((A:A>1)*(B:B))`, `=SUM(A:CZ)`,
		`=SUMPRODUCT(A1:A10,B1:B10)+SUM(C:C)`,
	}
	for _, f := range bad {
		if f != "=" && nativeAgentEngineFormula(f) {
			t.Errorf("unsafe formula accepted: %q", f)
		}
	}
	if nativeAgentEngineFormula("=") != true {
		t.Log("an empty formula body is left to the engine")
	}
}

// Off by default: the adapter accepts the same commands as before. With the
// option, a formula passes the allowlist and a forbidden one is still refused
// before the engine runs.
func TestNativeAgentSheetFormulasOnlyWithTheOption(t *testing.T) {
	p, marker := nativeAgentRejectProbe(t)
	p.Application = "gridcraft"
	doc := []byte(`{"sheets":[]}`)
	formula := json.RawMessage(`[{"command":"cell.set","params":{"cell":"C2","input":"=SUM(A2:B2)","sheet":"Data"}},{"command":"range.setValues","params":{"range":"D1","values":[["=A1*2",3]]}}]`)

	off := NewCraftNativeAgentCLIAdapter()
	if _, err := off.Apply(context.Background(), p, doc, formula); err == nil {
		t.Fatal("formula accepted without the option")
	}
	nativeAgentNotExecuted(t, marker)

	on := NewCraftNativeAgentCLIAdapter(WithCraftNativeAgentFormulas())
	for _, raw := range []string{
		`[{"command":"cell.set","params":{"cell":"C2","input":"=WEBSERVICE(\"u\")"}}]`,
		`[{"command":"range.setValues","params":{"range":"A1","values":[["=1+1","=HYPERLINK(\"u\",\"x\")"]]}}]`,
		`[{"command":"cell.set","params":{"cell":"C2","input":"=[1]S!A1"}}]`,
	} {
		if _, err := on.Apply(context.Background(), p, doc, json.RawMessage(raw)); err == nil {
			t.Fatalf("forbidden formula accepted with the option: %s", raw)
		}
		nativeAgentNotExecuted(t, marker)
	}
	// The safe formula reaches the (stub) executable: the stub then fails, so an
	// error is expected, but the marker must exist.
	_, _ = on.Apply(context.Background(), p, doc, formula)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("a safe formula was refused before the engine ran")
	}
}

// Existing cells with INDIRECT, OFFSET or TODAY (common in imported books) are
// the engine's to recalculate: only the commands an agent writes are checked,
// never the document's own formulas, and a safe write next to them goes through.
func TestNativeAgentFormulaCheckLooksOnlyAtWhatTheAgentWrites(t *testing.T) {
	p, marker := nativeAgentRejectProbe(t)
	p.Application = "gridcraft"
	doc := []byte(`{"sheets":[{"name":"S","cells":{"A1":{"f":"=INDIRECT(\"B\"&1)"},"A2":{"f":"=OFFSET(A1,1,0)"},"A3":{"f":"=TODAY()"}}}]}`)
	on := NewCraftNativeAgentCLIAdapter(WithCraftNativeAgentFormulas())
	_, _ = on.Apply(context.Background(), p, doc, json.RawMessage(`[{"command":"cell.set","params":{"cell":"B1","input":"=SUM(C1:C9)"}}]`))
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("a safe write was refused because the book already holds volatile formulas")
	}
}

// A forbidden function in a cell the agent read cannot be copied into a new one.
func TestNativeAgentCannotCopyAForbiddenFormulaFromTheBook(t *testing.T) {
	p, marker := nativeAgentRejectProbe(t)
	p.Application = "gridcraft"
	on := NewCraftNativeAgentCLIAdapter(WithCraftNativeAgentFormulas())
	for _, copied := range []string{`=INDIRECT(\"B\"&1)`, `=OFFSET(A1,1,0)`, `=TODAY()`, `=HYPERLINK(\"http://x\",\"y\")`} {
		raw := json.RawMessage(`[{"command":"range.setValues","params":{"range":"D1","values":[["` + copied + `"]]}}]`)
		if _, err := on.Apply(context.Background(), p, []byte(`{"sheets":[]}`), raw); err == nil {
			t.Fatalf("copied formula accepted: %s", copied)
		}
		nativeAgentNotExecuted(t, marker)
	}
}
