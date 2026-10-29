//go:build linux && craft_native_engine_qualification

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// Explicit local HTTP/original-CLI qualification, not a browser/provider or
// managed WASM release claim. The editor URL/bundle below are format fixtures.
// CRAFT_QUALIFICATION_ENGINE, CRAFT_QUALIFICATION_SHA256 and
// CRAFT_QUALIFICATION_REVISION identify the pinned original Deck executable;
// CRAFT_QUALIFICATION_DECK_ARCHIVE names its canonical native ZIP fixture.
// These are test-only inputs, never server configuration; missing inputs fail.
func TestCraftNativeDeckOriginalCLIInitializeReadAndDenials(t *testing.T) {
	required := func(key string) string {
		t.Helper()
		s := os.Getenv(key)
		if s == "" {
			t.Fatalf("explicit qualification input %s required", key)
		}
		return s
	}
	p := CraftNativeDocumentProfile{Application: "deckcraft", EngineRevision: required("CRAFT_QUALIFICATION_REVISION"), CLIPath: required("CRAFT_QUALIFICATION_ENGINE"), CLISHA256: required("CRAFT_QUALIFICATION_SHA256"), EditorBundle: strings.Repeat("e", 64), EditorURL: "/_editor/b/" + strings.Repeat("e", 64) + "/index.html"}
	if p.CLISHA256 != "4b3c62acc833745335f8c84eb538a88996d312e784b826cbaf90667a7c7c45d9" || p.EngineRevision != "6d09d3770f82364320f1f098a7e74bd9b2ad3ac4" {
		t.Fatal("qualification requires the frozen original Deck CLI/source pin")
	}
	fzip, err := os.Open(required("CRAFT_QUALIFICATION_DECK_ARCHIVE"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(io.LimitReader(fzip, (32<<20)+1))
	closeErr := fzip.Close()
	if err != nil || closeErr != nil || len(raw) > 32<<20 {
		t.Fatal("bounded native fixture unavailable")
	}
	f, _, _ := craftNativeHTTPFixture(t)
	const page = "qualified-deck-init"
	f.seedPage(t, page, page, "Deck initializer qualification", f.userID, "")
	router, err := NewRouter(f.db, "deck-original-http-fixture-secret32", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true, Profiles: map[string]CraftNativeDocumentProfile{"deckcraft": p}}))
	if err != nil {
		t.Fatalf("qualified original Deck startup refused: %v", err)
	}
	t.Cleanup(router.Shutdown)
	call := craftNativeInitializeTCP(t, router, f.wsID)
	owner := mintIngressScopedToken(t, f.db, f.userID, "deck-original-owner", "pages:*")
	readOnly := mintIngressScopedToken(t, f.db, f.userID, "deck-original-reader", "pages:read")
	other := mintIngressScopedToken(t, f.db, f.adminID, "deck-original-other", "pages:*")
	body := map[string]any{"application_id": "deckcraft", "bytes": raw}
	for name, credential := range map[string]string{"read-only": readOnly, "not-page-owner": other} {
		if status, response := call("POST", page, "initialize", credential, "", body); status != 403 {
			t.Fatalf("%s initialized Deck: %d %s", name, status, response)
		}
		craftNativeInitializeNoPartial(t, f, router, page)
	}
	status, response := call("POST", page, "initialize", owner, "", body)
	if status != 201 {
		t.Fatalf("qualified Deck initialize: %d %s", status, response)
	}
	var admission craftNativeInitializeResponse
	if err = json.Unmarshal(response, &admission); err != nil || admission.ApplicationID != "deckcraft" || admission.PageID != page || admission.Version != "1" || len(admission.Token) != 64 || admission.Generation == "" {
		t.Fatalf("invalid exact Deck receipt: %s %v", response, err)
	}
	status, response = call("GET", page, "document", owner, admission.Token, nil)
	var snapshot craftNativeDocumentResponse
	if status != 200 || json.Unmarshal(response, &snapshot) != nil || snapshot.ApplicationID != "deckcraft" || snapshot.Version != "1" || !bytes.Equal(snapshot.Bytes, raw) {
		t.Fatalf("original Deck ZIP not preserved through read: %d %s", status, response)
	}
	if status, response = call("POST", page, "initialize", owner, "", body); status != 409 {
		t.Fatalf("reinitialize overwrote original: %d %s", status, response)
	}
	status, response = call("POST", page, "sessions", readOnly, "", map[string]any{})
	var reader struct {
		Token string `json:"session_token"`
	}
	if status != 201 || json.Unmarshal(response, &reader) != nil {
		t.Fatalf("read-only admission: %d %s", status, response)
	}
	if status, response = call("GET", page, "document", readOnly, reader.Token, nil); status != 200 {
		t.Fatalf("reader lost existing read right: %d %s", status, response)
	}
	if status, response = call("POST", page, "lease/acquire", readOnly, reader.Token, map[string]any{}); status != 403 {
		t.Fatalf("read-only Deck acquired edit: %d %s", status, response)
	}
}
