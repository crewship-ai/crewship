//go:build craft_native_engine_qualification

package mooring

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// Explicit release qualification uses the original pinned executable.
// A requested qualification must fail when its required inputs are absent.
func TestNativeCLIValidatorCanonicalWordCreation(t *testing.T) {
	binary := os.Getenv("CRAFT_QUALIFICATION_ENGINE")
	if binary == "" {
		t.Fatal("set CRAFT_QUALIFICATION_ENGINE and its pinned SHA256 to qualify original-engine creation")
	}
	hash := os.Getenv("CRAFT_QUALIFICATION_SHA256")
	if len(hash) != 64 {
		t.Fatal("explicit pinned CLI SHA256 required")
	}
	raw, err := os.ReadFile("../../lib/mooring/wordcraft-native-empty.json")
	if err != nil {
		t.Fatal(err)
	}
	validator := NativeCLIValidator{Application: "wordcraft", BinaryPath: binary, SHA256: hash}
	if err := validator.Validate(context.Background(), raw); err != nil {
		t.Fatalf("canonical New Doc bytes rejected by pinned engine: %v", err)
	}
	var model map[string]any
	if err := json.Unmarshal(raw, &model); err != nil {
		t.Fatal(err)
	}
	styles := model["styles"].(map[string]any)
	para := styles["defaultPara"].(map[string]any)
	spacing := para["lineSpacing"].(map[string]any)
	spacing["value"] = 1.149999976158142
	malformed, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(context.Background(), malformed); !errors.Is(err, ErrNativeValidation) {
		t.Fatalf("lossy float promotion was not refused: %v", err)
	}
}
