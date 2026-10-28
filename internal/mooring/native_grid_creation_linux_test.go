package mooring

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// Optional ordinary tests do not require a private executable. Qualification
// explicitly supplies the pinned original engine and checks the actual UI bytes.
// Set CREWSHIP_TEST_GRIDCRAFT_CLI to that executable and
// CREWSHIP_TEST_GRIDCRAFT_CLI_SHA256 to its pinned SHA256, then run this test.
// These are test inputs, not server startup configuration. A skip is not qualification.
func TestNativeCLIValidatorCanonicalSheetCreation(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_CLI")
	if binary == "" {
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_CLI and its pinned SHA256 to qualify original-engine Sheet creation")
	}
	hash := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_CLI_SHA256")
	if len(hash) != 64 {
		t.Fatal("explicit pinned CLI SHA256 required")
	}
	raw, err := os.ReadFile("../../lib/mooring/gridcraft-empty.ts")
	if err != nil {
		t.Fatal(err)
	}
	_, literal, ok := strings.Cut(string(raw), "const emptyWorkbook = ")
	if !ok {
		t.Fatal("canonical engine workbook literal not found")
	}
	literal, _, ok = strings.Cut(literal, "\n\nexport function")
	if !ok {
		t.Fatal("canonical engine workbook boundary not found")
	}
	var document string
	if err := json.Unmarshal([]byte(strings.TrimSpace(literal)), &document); err != nil {
		t.Fatal(err)
	}
	validator := NativeCLIValidator{Application: "gridcraft", BinaryPath: binary, SHA256: hash}
	if err := validator.Validate(context.Background(), []byte(document)); err != nil {
		t.Fatalf("canonical Sheet creation rejected by pinned engine: %v", err)
	}
	var model map[string]any
	if err := json.Unmarshal([]byte(document), &model); err != nil {
		t.Fatal(err)
	}
	model["unrecognized_authored_extension"] = "must never be silently discarded"
	malformed, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(context.Background(), malformed); !errors.Is(err, ErrNativeValidation) {
		t.Fatalf("lossy authored extension was not refused: %v", err)
	}
}
