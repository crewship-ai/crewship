package server

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/craftassets"
)

func TestCraftRealtimeChatSheetRequiresTheExactEditorAndEngine(t *testing.T) {
	pilot := craftassets.Component{
		Application: "gridcraft", DocumentFormat: craftassets.DocumentFormatWorkbook,
		Engine: craftassets.File{SHA256: "896aa0acefb35ef6f2d2fd400ff6db346df4de9c7949690d4f5dd5c787229c2f"},
		Editor: craftassets.Editor{Digest: "9fbaa966d143feea52b64e91a12e475b5d6efb6e304e380cdcade56a8b334515"},
	}
	if !craftRealtimeChatSheetPin(pilot) {
		t.Fatal("the verified GridCraft pair should be admitted")
	}
	otherEngine, otherEditor, previous, native := pilot, pilot, pilot, pilot
	otherEngine.Engine.SHA256 = "efd47c329c2c4087860bc2c4d895bc0d1d76d4e551e9fb4ce15233bee000a300"
	otherEditor.Editor.Digest = "76b6cda379bec2abcbea7ab3de31601b80b43ddf92aa003b481e786dd9f44271"
	previous.Engine, previous.Editor = otherEngine.Engine, otherEditor.Editor
	native.DocumentFormat = ""
	for name, component := range map[string]craftassets.Component{
		"other engine": otherEngine, "other editor": otherEditor, "previous pair": previous, "native document format": native,
	} {
		t.Run(name, func(t *testing.T) {
			if craftRealtimeChatSheetPin(component) {
				t.Fatal("an unmatched package was admitted to realtime editing")
			}
		})
	}
}
