package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The card of an agent change says where it changed what: every sheet, every range in order
// (cleared cells count as changed), at most 20 ranges with the rest marked truncated.
func TestAgentChangeSummaryNamesSheetsRangesAndCells(t *testing.T) {
	commands := json.RawMessage(`[{"command":"range.clear","params":{"range":"B8:B577","sheet":"L"}},{"command":"range.clear","params":{"range":"'M'!C2:C3"}},{"command":"cell.set","params":{"cell":"C1","input":"done","sheet":"L"}}]`)
	got := nativeAgentChangeSummary(commands)
	want := craftNativeChangeSummary{CellsChanged: 573, Sheets: []string{"L", "M"}, Ranges: []string{"'L'!B8:B577", "'M'!C2:C3", "'L'!C1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summary %+v", got)
	}
	if n := nativeAgentChangedCells(commands); n != 573 {
		t.Fatalf("changed cells %d", n)
	}
	var many []map[string]any
	for i := 1; i <= 25; i++ {
		many = append(many, map[string]any{"command": "cell.set", "params": map[string]any{"cell": "A" + string(rune('0'+i%10)), "input": "x", "sheet": "L"}})
	}
	raw, _ := json.Marshal(many)
	if s := nativeAgentChangeSummary(raw); len(s.Ranges) != 20 || !s.Truncated || s.CellsChanged != 25 {
		t.Fatalf("many %+v", s)
	}
}
