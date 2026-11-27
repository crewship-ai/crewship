package craftsession

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The import notes say how many formulas keep the value Excel calculated, and for which
// functions. A misspelled name or a function the engine computes is not counted.
func TestHeldFormulasNoteCountsExcelFunctionsTheEngineKeeps(t *testing.T) {
	native := []byte(`{"sheets":[{"cells":{"rows":{` +
		`"0":{"0":{"value":{"t":"Number","v":630},"formula":{"text":"GETPIVOTDATA(\"Sum of B\",$F$1)"}}},` +
		`"1":{"0":{"value":{"t":"Number","v":1},"formula":{"text":"_xlfn.STOCKHISTORY(\"X\",1)+getpivotdata(\"a\",B1)"}}},` +
		`"2":{"0":{"value":{"t":"Error","v":"Name"},"formula":{"text":"GETPIVOTDATAX(1)"}}},` +
		`"3":{"0":{"value":{"t":"Text","v":"GETPIVOTDATA(x)"},"formula":null}},` +
		`"4":{"0":{"value":{"t":"Number","v":3},"formula":{"text":"SUM(1,2)"}}}` +
		`}}}]}`)
	n, functions := heldFormulas(native)
	if n != 2 || !reflect.DeepEqual(functions, []string{"GETPIVOTDATA", "STOCKHISTORY"}) {
		t.Fatalf("held formulas %d %v", n, functions)
	}
	note := heldNote(n, functions)
	if !strings.Contains(note, "2 formulas") || !strings.Contains(note, "GETPIVOTDATA, STOCKHISTORY") || !strings.Contains(note, "not recalculated") {
		t.Fatalf("note %q", note)
	}
	if n, _ := heldFormulas([]byte(`{"sheets":[]}`)); n != 0 || heldNote(0, nil) != "" {
		t.Fatal("a workbook without them has a note")
	}
}

// An agent reading a cell or a range learns which values come from Excel.
func TestReadsCarryTheHeldValueMark(t *testing.T) {
	var c Cell
	if err := json.Unmarshal([]byte(`{"cell":"A1","formula":"=GETPIVOTDATA(\"Sum of B\",$F$1)","value":630,"heldValue":{"functions":["GETPIVOTDATA"],"note":"The value Excel calculated."}}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.HeldValue == nil || !reflect.DeepEqual(c.HeldValue.Functions, []string{"GETPIVOTDATA"}) || c.HeldValue.Note == "" {
		t.Fatalf("cell %+v", c)
	}
	var plain Cell
	_ = json.Unmarshal([]byte(`{"cell":"B1","value":1260}`), &plain)
	if out, _ := json.Marshal(plain); strings.Contains(string(out), "heldValue") {
		t.Fatalf("a calculated cell reports a held value: %s", out)
	}
	var v Values
	if err := json.Unmarshal([]byte(`{"range":"A1:B1","sheet":"S","values":[[630,1260]],"heldValues":["A1"]}`), &v); err != nil || !reflect.DeepEqual(v.HeldValues, []string{"A1"}) {
		t.Fatalf("values %+v %v", v, err)
	}
}
