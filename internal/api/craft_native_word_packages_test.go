//go:build craftpackages

package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftassets"
	"github.com/crewship-ai/crewship/internal/craftengine"
)

func TestReleaseCraftPackagesWordAgentProfiles(t *testing.T) {
	dir := os.Getenv("CREWSHIP_CRAFT_TEST_PACKAGES")
	historical := os.Getenv("CRAFT_GATE_RETAINED_WORD_DIR")
	if dir == "" || historical == "" {
		t.Fatal("current packages and the verified historical WordCraft release are required")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "catalog.txt"))
	if err != nil {
		t.Fatal(err)
	}
	currentPins, err := craftassets.ParsePins(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../tools/craft-components/retained-word-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var historicalSource struct {
		Pin string `json:"pin"`
	}
	if err := json.Unmarshal(source, &historicalSource); err != nil {
		t.Fatal(err)
	}
	historicalPins, err := craftassets.ParsePins(historicalSource.Pin)
	if err != nil {
		t.Fatal(err)
	}
	currentPin, historicalPin := currentPins["wordcraft"], historicalPins["wordcraft"]
	if currentPin == historicalPin || currentPin.Application == "" || historicalPin.Application == "" {
		t.Fatal("two independently pinned versions of WordCraft are required")
	}
	store := craftassets.Store{Root: t.TempDir()}
	load := func(dir string, pin craftassets.Pin) CraftNativeDocumentProfile {
		t.Helper()
		archive, err := os.Open(filepath.Join(dir, craftassets.AssetName(pin.Application)))
		if err != nil {
			t.Fatal(err)
		}
		defer archive.Close()
		installed, err := store.Install(pin, archive)
		if err != nil {
			t.Fatal(err)
		}
		c := installed.Component
		engine, err := craftengine.NewLazyWASM(c.Application, c.Engine.SHA256, installed.EngineLoader(), craftengine.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = engine.Close(context.Background()) })
		return CraftNativeDocumentProfile{
			Application: c.Application, EngineRevision: c.EngineRevision, CLISHA256: c.Engine.SHA256, Engine: engine,
			EditorBundle: c.Editor.Digest, EditorURL: "/_editor/b/" + c.Editor.Digest + "/index.html",
		}
	}
	old := load(historical, historicalPin)
	current := load(dir, currentPin)
	if old.EngineRevision == current.EngineRevision || old.CLISHA256 == current.CLISHA256 || old.EditorBundle == current.EditorBundle {
		t.Fatal("both WordCraft packages must carry distinct engines and editors")
	}
	initial, err := os.ReadFile("../../lib/mooring/wordcraft-native-empty.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("retained_document", func(t *testing.T) {
		qualifyNativeAgentHTTP(t, old, initial, current, old)
	})
	t.Run("current_document", func(t *testing.T) {
		qualifyNativeAgentHTTP(t, current, initial, current, old)
	})
	t.Run("rollback_keeps_new_document", func(t *testing.T) {
		qualifyNativeAgentHTTP(t, current, initial, old, current)
	})
}
