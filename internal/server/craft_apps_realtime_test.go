package server

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/craftassets"
)

func TestCraftRealtimeChatSheetRequiresTheExactEditorAndEngine(t *testing.T) {
	pilot := craftassets.Component{
		Application: "gridcraft", DocumentFormat: craftassets.DocumentFormatWorkbook,
		Engine: craftassets.File{SHA256: "efd47c329c2c4087860bc2c4d895bc0d1d76d4e551e9fb4ce15233bee000a300"},
		Editor: craftassets.Editor{Digest: "76b6cda379bec2abcbea7ab3de31601b80b43ddf92aa003b481e786dd9f44271"},
	}
	if !craftRealtimeChatSheetPin(pilot) {
		t.Fatal("the verified GridCraft pair should be admitted")
	}
	otherEngine, otherEditor, native := pilot, pilot, pilot
	otherEngine.Engine.SHA256 = "592acd55"
	otherEditor.Editor.Digest = "different-editor"
	native.DocumentFormat = ""
	for name, component := range map[string]craftassets.Component{
		"other engine": otherEngine, "other editor": otherEditor, "native document format": native,
	} {
		t.Run(name, func(t *testing.T) {
			if craftRealtimeChatSheetPin(component) {
				t.Fatal("an unmatched package was admitted to realtime editing")
			}
		})
	}
}
