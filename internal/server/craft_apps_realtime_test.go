package server

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/craftassets"
)

func TestCraftRealtimeChatSheetRequiresTheExactEditorAndEngine(t *testing.T) {
	pilot := craftassets.Component{
		Application: "gridcraft", DocumentFormat: craftassets.DocumentFormatWorkbook,
		Engine: craftassets.File{SHA256: "33052c82d83e42f7bd51892d01535863d3f1aad3ccc339e2da6d49d6eb153a70"},
		Editor: craftassets.Editor{Digest: "a97823bef84bfb3bf2633b736e7a9226511a06747fb0c2fce96b16d912bdfcb7"},
	}
	if !craftRealtimeChatSheetPin(pilot) {
		t.Fatal("the verified GridCraft pair should be admitted")
	}
	otherEngine, otherEditor, previous, native := pilot, pilot, pilot, pilot
	otherEngine.Engine.SHA256 = "896aa0acefb35ef6f2d2fd400ff6db346df4de9c7949690d4f5dd5c787229c2f"
	otherEditor.Editor.Digest = "9fbaa966d143feea52b64e91a12e475b5d6efb6e304e380cdcade56a8b334515"
	previous.Engine, previous.Editor = otherEngine.Engine, otherEditor.Editor
	native.DocumentFormat = ""
	for name, component := range map[string]craftassets.Component{
		"previous engine with current editor": otherEngine,
		"current engine with previous editor": otherEditor,
		"previous pair serves preview only":   previous,
		"native document format":              native,
	} {
		t.Run(name, func(t *testing.T) {
			if craftRealtimeChatSheetPin(component) {
				t.Fatal("an unmatched package was admitted to realtime editing")
			}
		})
	}
}
