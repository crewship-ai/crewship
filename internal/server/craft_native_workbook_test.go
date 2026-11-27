package server

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftassets"
)

// CREWSHIP_SHEET_WORKBOOK=1 only confirms a workbook Sheet package; asked without one, the server
// refuses to start with a reason, never a silent fallback to native documents.
func TestSheetWorkbookSwitchNeedsAWorkbookPackage(t *testing.T) {
	sheet := func(format string) map[string]*craftassets.Installed {
		return map[string]*craftassets.Installed{"gridcraft": {Component: craftassets.Component{Application: "gridcraft", DocumentFormat: format}}}
	}
	if err := sheetWorkbookRequired("", sheet("")); err != nil {
		t.Fatal(err)
	}
	if err := sheetWorkbookRequired("1", sheet(craftassets.DocumentFormatWorkbook)); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		value     string
		installed map[string]*craftassets.Installed
	}{"native package": {"1", sheet("")}, "no package": {"1", nil}, "other value": {"true", sheet(craftassets.DocumentFormatWorkbook)}} {
		if err := sheetWorkbookRequired(c.value, c.installed); err == nil || !strings.Contains(err.Error(), "CREWSHIP_SHEET_WORKBOOK") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
