//go:build linux && mooring_rust_integration

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// Fixture pins come from this run's public-source build manifest. They describe
// test input bytes, never participant, source or application authority.
func mooringActualFixtureSHA(t *testing.T, name string) string {
	t.Helper()
	want := os.Getenv(name)
	raw, err := hex.DecodeString(want)
	if err != nil || len(raw) != sha256.Size || hex.EncodeToString(raw) != want {
		t.Fatalf("actual fixture requires canonical SHA256 %s", name)
	}
	return want
}

func mooringActualSDKPrefix(t *testing.T) string {
	t.Helper()
	assets := map[string]string{
		"mooring_wordcraft_client.js":      mooringActualFixtureSHA(t, "MOORING_SDK_JS_SHA256"),
		"mooring_wordcraft_client_bg.wasm": mooringActualFixtureSHA(t, "MOORING_SDK_WASM_SHA256"),
	}
	raw, err := json.Marshal(assets)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("/mooring-sdk/generated/%x", sha256.Sum256(raw))
}
