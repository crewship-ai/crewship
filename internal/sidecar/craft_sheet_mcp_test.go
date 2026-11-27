package sidecar

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCraftSheetCatalogIsClosedAndCarriesNoPage(t *testing.T) {
	tools := craftSheetMCPCatalog()
	if len(tools) != 6 {
		t.Fatalf("%d tools", len(tools))
	}
	for _, tool := range tools {
		if !strings.HasPrefix(tool.Name, "craft_sheet_") {
			t.Errorf("unexpected tool %s", tool.Name)
		}
		var schema struct {
			AdditionalProperties bool                       `json:"additionalProperties"`
			Properties           map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(tool.InputSchema, &schema) != nil || schema.AdditionalProperties {
			t.Errorf("%s: schema is not closed", tool.Name)
		}
		for _, forbidden := range []string{"page_id", "page", "slug", "run_id", "mandate_id", "workspace_id", "agent_id"} {
			if _, ok := schema.Properties[forbidden]; ok {
				t.Errorf("%s takes %s", tool.Name, forbidden)
			}
		}
	}
}

// The packet is built by the same closed grammar the task tools use: a foreign
// field never reaches the host, and the operation is the tool's own.
func TestCraftSheetPacketRefusesForeignFieldsAndTakesTheOperationFromTheName(t *testing.T) {
	for _, bad := range []string{
		`{"page_id":"other","query":{"command":"cell.get","params":{"cell":"A1"}}}`,
		`{"slug":"x"}`, `{"operation":"apply"}`, `{"run_id":"r"}`,
	} {
		if _, ok := nativeCraftToolPacket("craft_read", json.RawMessage(bad)); ok {
			t.Errorf("accepted: %s", bad)
		}
	}
	packet, ok := nativeCraftToolPacket("craft_read", json.RawMessage(`{"query":{"command":"cell.get","params":{"cell":"A1"}}}`))
	if !ok || !strings.HasPrefix(string(packet), `{"operation":"read"`) {
		t.Fatalf("packet %s %v", packet, ok)
	}
}

func TestCraftSheetToolsAreListedOnlyWhenEnabled(t *testing.T) {
	if len(craftSheetMCPCatalog()) == 0 {
		t.Fatal("empty catalog")
	}
	names := map[string]bool{}
	for _, tool := range craftSheetMCPCatalog() {
		names[tool.Name] = true
	}
	for _, op := range craftSheetOperations {
		if !names[craftSheetToolName(op)] {
			t.Errorf("missing %s", op)
		}
	}
}

// The agent is told what a change does not do, so it can say it to the person.
func TestCraftSheetApplyDescriptionSaysPivotTablesAreNotRefreshed(t *testing.T) {
	for _, tool := range craftSheetMCPCatalog() {
		if tool.Name == "craft_sheet_apply" {
			if !strings.Contains(tool.Description, "pivot tables are not refreshed") {
				t.Fatalf("apply does not say that pivot tables stay unrefreshed: %s", tool.Description)
			}
			return
		}
	}
	t.Fatal("no apply tool")
}

// The agent learns from the tool itself that range.clear exists and that every write in a Sheet
// kept as its file names its sheet.
func TestCraftSheetApplyDescriptionNamesTheSheetAndTheClear(t *testing.T) {
	d := craftSheetDescription("apply")
	for _, want := range []string{"range.clear params{range,sheet?}", "name the sheet in every write with params.sheet", "'L'!B8"} {
		if !strings.Contains(d, want) {
			t.Errorf("apply description lacks %q", want)
		}
	}
}

func TestCraftSheetReadDescriptionFindsAColumnByHeader(t *testing.T) {
	if d := craftSheetDescription("read"); !strings.Contains(d, "range.columnByHeader,params:{header,sheet?,matchCase?}") {
		t.Fatalf("read description %q", d)
	}
}
