package api

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type workbookProfileEngine struct {
	craftengine.Engine
	app string
}

func (e workbookProfileEngine) Application() string { return e.app }
func (workbookProfileEngine) SHA256() string        { return strings.Repeat("b", 64) }

// A workbook profile keeps Sheets as files and validates the archive, never with the engine; a
// Word profile cannot ask for it.
func TestWorkbookProfileKeepsSheetsAsFiles(t *testing.T) {
	bundle := strings.Repeat("c", 64)
	p := CraftNativeDocumentProfile{Application: "gridcraft", EngineRevision: strings.Repeat("a", 40), EditorBundle: bundle, EditorURL: "/_editor/b/" + bundle + "/index.html", Engine: workbookProfileEngine{app: "gridcraft"}, Workbook: true}
	apps, _, err := qualifyCraftNativeProfiles(map[string]CraftNativeDocumentProfile{"gridcraft": p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := apps["gridcraft"].Store
	if store.Format != mooring.NativeFormatWorkbook || store.Validate == nil || store.Validate(t.Context(), craftTestWorkbook(t, "xlsx", "profile")) != nil || store.Validate(t.Context(), []byte(`{"v":1}`)) == nil {
		t.Fatalf("workbook store %+v", store.Format)
	}
	p.Workbook = false
	if apps, _, err = qualifyCraftNativeProfiles(map[string]CraftNativeDocumentProfile{"gridcraft": p}, nil); err != nil || apps["gridcraft"].Store.Format != "" {
		t.Fatalf("native profile %v %q", err, apps["gridcraft"].Store.Format)
	}
	word := p
	word.Application, word.Engine = "wordcraft", workbookProfileEngine{app: "wordcraft"}
	if _, _, err = qualifyCraftNativeProfiles(map[string]CraftNativeDocumentProfile{"wordcraft": word}, nil); err != nil {
		t.Fatalf("the Word profile itself: %v", err)
	}
	word.Workbook = true
	if _, _, err = qualifyCraftNativeProfiles(map[string]CraftNativeDocumentProfile{"wordcraft": word}, nil); err == nil {
		t.Fatal("a Word profile became a workbook profile")
	}
}
