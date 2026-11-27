package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func sheetValuesJSON(rows, cols int, cell string) string {
	var b strings.Builder
	b.WriteString("[")
	for r := 0; r < rows; r++ {
		if r > 0 {
			b.WriteString(",")
		}
		b.WriteString("[")
		for c := 0; c < cols; c++ {
			if c > 0 {
				b.WriteString(",")
			}
			b.WriteString(cell)
		}
		b.WriteString("]")
	}
	b.WriteString("]")
	return b.String()
}

// The Sheet agent surface: a named sheet on cell.set/cell.get, bounded bulk
// writes of values, and bounded reads of a range and of the workbook outline.
func TestNativeAgentSheetCommandsAcceptedShapes(t *testing.T) {
	apply := []string{
		`[{"command":"cell.set","params":{"cell":"B2","input":"41","sheet":"Q1 Sales"}}]`,
		`[{"command":"range.setValues","params":{"range":"A1","values":[[1,"two",true,null],[2.5,"x","",false]]}}]`,
		`[{"command":"range.setValues","params":{"sheet":"Data","range":"B2:D3","values":[[1,2,3],[4,5,6]]}}]`,
		`[{"command":"range.setValues","params":{"range":"A1","values":` + sheetValuesJSON(100, 20, `1.5`) + `}}]`,
		`[{"command":"cell.set","params":{"cell":"A1","input":"x"}},{"command":"range.setValues","params":{"range":"A2","values":[["y"]]}}]`,
	}
	for _, raw := range apply {
		if _, err := nativeAgentEngineCommands("gridcraft", json.RawMessage(raw), false); err != nil {
			t.Errorf("valid Sheet command refused: %.120s: %v", raw, err)
		}
	}
	inspect := []string{
		`{"command":"cell.get","params":{"cell":"A1","sheet":"Data"}}`,
		`{"command":"sheet.read","params":{"range":"A1:D10"}}`,
		`{"command":"sheet.read","params":{"range":"A1:D10","sheet":"Data","formulas":true,"formatted":false}}`,
		`{"command":"document.inspect","params":{}}`,
	}
	for _, raw := range inspect {
		if _, err := nativeAgentEngineCommands("gridcraft", json.RawMessage(raw), true); err != nil {
			t.Errorf("valid Sheet query refused: %s: %v", raw, err)
		}
	}
}

func TestNativeAgentSheetCommandsRefuseUnsafeShapes(t *testing.T) {
	tooMany := sheetValuesJSON(1001, 1, `1`)
	tooWide := sheetValuesJSON(1, 201, `1`)
	apply := map[string]string{
		"sheet name with a path separator": `[{"command":"cell.set","params":{"cell":"A1","input":"x","sheet":"../x"}}]`,
		"sheet name with a bracket":        `[{"command":"cell.set","params":{"cell":"A1","input":"x","sheet":"[book]S"}}]`,
		"sheet name over 31 characters":    `[{"command":"cell.set","params":{"cell":"A1","input":"x","sheet":"` + strings.Repeat("s", 32) + `"}}]`,
		"empty sheet name":                 `[{"command":"cell.set","params":{"cell":"A1","input":"x","sheet":""}}]`,
		"formula in a bulk write":          `[{"command":"range.setValues","params":{"range":"A1","values":[["=1+1"]]}}]`,
		"plus function in a bulk write":    `[{"command":"range.setValues","params":{"range":"A1","values":[["+WEBSERVICE(\"u\")"]]}}]`,
		"too many rows":                    `[{"command":"range.setValues","params":{"range":"A1","values":` + tooMany + `}}]`,
		"too many columns":                 `[{"command":"range.setValues","params":{"range":"A1","values":` + tooWide + `}}]`,
		"spills past the last column":      `[{"command":"range.setValues","params":{"range":"XFD1","values":[[1,2]]}}]`,
		"spills past the last row":         `[{"command":"range.setValues","params":{"range":"A1048576","values":[[1],[2]]}}]`,
		"object value":                     `[{"command":"range.setValues","params":{"range":"A1","values":[[{"a":1}]]}}]`,
		"nested array value":               `[{"command":"range.setValues","params":{"range":"A1","values":[[[1]]]}}]`,
		"flat values":                      `[{"command":"range.setValues","params":{"range":"A1","values":[1,2]}}]`,
		"missing values":                   `[{"command":"range.setValues","params":{"range":"A1"}}]`,
		"extra key":                        `[{"command":"range.setValues","params":{"range":"A1","values":[[1]],"path":"/etc/passwd"}}]`,
		"bad anchor":                       `[{"command":"range.setValues","params":{"range":"Sheet1!","values":[[1]]}}]`,
		"range anchor too large":           `[{"command":"range.setValues","params":{"range":"A1:ZZZ9999999","values":[[1]]}}]`,
		"non-finite number":                `[{"command":"range.setValues","params":{"range":"A1","values":[[1e999]]}}]`,
		"oversized text":                   `[{"command":"range.setValues","params":{"range":"A1","values":[["` + strings.Repeat("x", (32<<10)+1) + `"]]}}]`,
		"unlisted engine command":          `[{"command":"edit.replace","params":{"find":"a","replace":"b"}}]`,
		"file command":                     `[{"command":"file.save","params":{"path":"/tmp/x"}}]`,
	}
	for name, raw := range apply {
		if _, err := nativeAgentEngineCommands("gridcraft", json.RawMessage(raw), false); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	inspect := map[string]string{
		"read with a file":             `{"command":"sheet.read","params":{"range":"A1:D10","path":"/etc/passwd"}}`,
		"read of the whole sheet":      `{"command":"sheet.read","params":{}}`,
		"read range too large":         `{"command":"sheet.read","params":{"range":"A1:ZZ99999"}}`,
		"read range with a sheet name": `{"command":"sheet.read","params":{"range":"Sheet1!A1:B2"}}`,
		"read with a bad sheet":        `{"command":"sheet.read","params":{"range":"A1:B2","sheet":"a/b"}}`,
		"read with a non-bool flag":    `{"command":"sheet.read","params":{"range":"A1:B2","formulas":"yes"}}`,
		"inspect with params":          `{"command":"document.inspect","params":{"x":1}}`,
		"unlisted query":               `{"command":"selection.stats","params":{}}`,
		"history":                      `{"command":"history.list","params":{}}`,
	}
	for name, raw := range inspect {
		if _, err := nativeAgentEngineCommands("gridcraft", json.RawMessage(raw), true); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_ = fmt.Sprint
}

// Word keeps its own, unchanged surface.
func TestNativeAgentSheetCommandsDoNotWidenWord(t *testing.T) {
	for _, raw := range []string{
		`[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]`,
		`[{"command":"range.setValues","params":{"range":"A1","values":[[1]]}}]`,
	} {
		if _, err := nativeAgentEngineCommands("wordcraft", json.RawMessage(raw), false); err == nil {
			t.Errorf("Sheet command accepted for Word: %s", raw)
		}
	}
	if _, err := nativeAgentEngineCommands("wordcraft", json.RawMessage(`{"command":"sheet.read","params":{"range":"A1:B2"}}`), true); err == nil {
		t.Error("Sheet query accepted for Word")
	}
}
