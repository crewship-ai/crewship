package craftsession

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// HeldValue marks a cell whose value is the one the document brought: its formula calls an
// Excel function the engine does not compute (GETPIVOTDATA, RTD, …), so the engine keeps
// Excel's value and does not recalculate it. Formulas that read the cell are calculated from
// that value. Editing the formula drops the mark.
type HeldValue struct {
	Functions []string `json:"functions"`
	Note      string   `json:"note"`
}

// heldFunctionNames are the Excel functions the pinned engine keeps the document's value
// for (gridcraft_calc::is_held_function). The engine decides; this list only words the
// import note, so it names those the engine is known to lack.
var heldFunctionNames = []string{"COPILOT", "DETECTLANGUAGE", "FIELDVALUE", "GETPIVOTDATA", "IMAGE", "PY", "RTD", "STOCKHISTORY", "TRANSLATE"}

var (
	heldCallRe      = regexp.MustCompile(`(?i)(?:^|[^A-Z0-9_.])(?:_xlfn\.)?(` + strings.Join(heldFunctionNames, "|") + `)\s*\(`)
	formulaTextRe   = regexp.MustCompile(`"formula":\{"text":"((?:[^"\\]|\\.)*)"`)
	formulaStringRe = regexp.MustCompile(`"(?:[^"]|"")*"`)
)

// heldFormulas counts the formulas of a compact native document that call a held function,
// and names those functions in order of first appearance.
func heldFormulas(native []byte) (int, []string) {
	n := 0
	var functions []string
	for _, m := range formulaTextRe.FindAllSubmatch(native, -1) {
		var text string
		if json.Unmarshal(append(append([]byte{'"'}, m[1]...), '"'), &text) != nil {
			continue
		}
		text = formulaStringRe.ReplaceAllString(text, `""`) // a name inside a text literal is not a call
		calls := heldCallRe.FindAllStringSubmatch(text, -1)
		if len(calls) == 0 {
			continue
		}
		n++
		for _, c := range calls {
			name := strings.ToUpper(c[1])
			if !containsString(functions, name) {
				functions = append(functions, name)
			}
		}
	}
	return n, functions
}

func heldNote(n int, functions []string) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d formulas keep the value Excel calculated and are not recalculated (%s)", n, strings.Join(functions, ", "))
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
