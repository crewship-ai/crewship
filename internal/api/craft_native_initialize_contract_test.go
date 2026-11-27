package api

import (
	"encoding/json"
	"os"
	"testing"
)

// The browser checks the answer of POST .../craft/initialize and of
// POST .../craft/import/{id}/initialize field by field
// (lib/craft-native-initialize.ts). This golden file is the shape both sides
// agree on: the Go struct must marshal to it, and the TypeScript test parses it.
func TestCraftNativeInitializeResponseMatchesTheBrowserContract(t *testing.T) {
	golden, err := os.ReadFile("testdata/craft_native_initialize_response.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(craftNativeInitializeResponse{
		Token: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SessionID: "sess_1",
		ApplicationID: "gridcraft", PageID: "pg_1", Generation: "7", Version: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	if err := json.Unmarshal(golden, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &b); err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("fields differ: golden %v, struct %v", a, b)
	}
	for k, v := range a {
		if b[k] != v {
			t.Errorf("field %q: golden %v, struct %v", k, v, b[k])
		}
	}
}
