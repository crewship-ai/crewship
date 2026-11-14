//go:build craftqualification

package mooring

import (
	"context"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftengine"
)

// TestNativeWASMEngineQualification runs a real bundled engine module through
// the same validator the store uses. It is opt-in, because ordinary builds do
// not carry the module:
//
//	CRAFT_WASM_QUALIFICATION_APP=wordcraft CRAFT_WASM_QUALIFICATION_MODULE=/abs/wordcraft-cli.wasm go test -tags craftqualification ./internal/mooring -run WASMEngineQualification
func TestNativeWASMEngineQualification(t *testing.T) {
	app, path := os.Getenv("CRAFT_WASM_QUALIFICATION_APP"), os.Getenv("CRAFT_WASM_QUALIFICATION_MODULE")
	if path == "" {
		t.Fatal("set CRAFT_WASM_QUALIFICATION_MODULE to qualify a bundled engine")
	}
	module, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := craftengine.NewWASM(app, module, "", craftengine.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.Background())
	documents := map[string]string{"wordcraft": "../../lib/mooring/wordcraft-native-empty.json"}
	if fixture := os.Getenv("CRAFT_WASM_QUALIFICATION_DOCUMENT"); fixture != "" {
		documents[app] = fixture
	}
	raw, err := os.ReadFile(documents[app])
	if err != nil {
		t.Fatalf("no document to validate for %s: %v", app, err)
	}
	v := NativeCLIValidator{Application: app, Engine: engine}
	if err := v.Validate(context.Background(), raw); err != nil {
		t.Fatalf("pinned WASM engine rejected a valid document: %v", err)
	}
	if err := v.Validate(context.Background(), []byte(`{"not":"a document"}`)); err == nil {
		t.Fatal("WASM engine accepted a non-document")
	}
}
