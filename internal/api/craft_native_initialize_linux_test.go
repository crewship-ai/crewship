package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCraftNativeInitializeExistingEmptyOwnerPageOverTCPThenReadSave(t *testing.T) {
	f, router, _, owner, _, cfg := craftNativeDataTCPFixture(t, true)
	const page = "native-init-empty"
	f.seedPage(t, page, page, "Explicit native owner sample", f.userID, "")
	call := craftNativeInitializeTCP(t, router, f.wsID)
	if status, raw := call("GET", page, "", owner, "", nil); status != 200 {
		t.Fatalf("existing owner Page calibration %d %s", status, raw)
	}
	initial := []byte(`{"v":1}`)
	status, raw := call("POST", page, "initialize", owner, "", map[string]any{"application_id": "wordcraft", "bytes": initial})
	if status != 201 {
		t.Fatalf("explicit native initialize expected201 got%d %s", status, raw)
	}
	var admitted struct {
		Token      string `json:"session_token"`
		ID         string `json:"session_id"`
		Page       string `json:"page_id"`
		App        string `json:"application_id"`
		Generation string `json:"generation"`
		Version    string `json:"version"`
	}
	if err := json.Unmarshal(raw, &admitted); err != nil || len(admitted.Token) != 64 || admitted.ID == "" || admitted.ID == admitted.Token || admitted.Page != page || admitted.App != "wordcraft" || admitted.Generation == "" || admitted.Version != "1" {
		t.Fatal("initialization receipt lacks exact admission/version binding")
	}
	status, raw = call("GET", page, "document", owner, admitted.Token, nil)
	if status != 200 {
		t.Fatalf("initialized read %d %s", status, raw)
	}
	var read craftNativeDocumentResponse
	if err := json.Unmarshal(raw, &read); err != nil || read.Version != "1" || !bytes.Equal(read.Bytes, initial) {
		t.Fatal("initial model was not preserved")
	}
	body := craftNativeDataTCPBody(f, cfg, admitted.ID, admitted.Generation, "1", "opaque:init-next", []byte(`{"v":2}`))
	body["pageId"] = page
	if status, raw = call("POST", page, "document/save", owner, admitted.Token, body); status != 200 {
		t.Fatalf("issued lease cannot save %d %s", status, raw)
	}
	if status, _ = call("POST", page, "initialize", owner, "", map[string]any{"application_id": "wordcraft", "bytes": initial}); status != 409 {
		t.Fatalf("reinitialization must not replace content: %d", status)
	}
	var head int
	if err := f.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, page).Scan(&head); err != nil || head != 2 {
		t.Fatalf("reinitialize changed head=%d err=%v", head, err)
	}
}
func TestCraftNativeInitializeRejectsNonOwnerReadScopeLegacyAndForgedBindings(t *testing.T) {
	for _, name := range []string{"nonowner", "readscope", "legacy", "crewowner", "forgedscope", "forgedpin", "wrongapp", "invalidengine"} {
		t.Run(name, func(t *testing.T) {
			f, router, _, owner, other, _ := craftNativeDataTCPFixture(t, true)
			const page = "native-init-denied"
			ownerUser, ownerCrew := f.userID, ""
			if name == "crewowner" {
				f.seedCrew(t, "native-init-crew", "Native init crew", "native-init-crew")
				ownerUser, ownerCrew = "", "native-init-crew"
			}
			f.seedPage(t, page, page, "Denied initialization", ownerUser, ownerCrew)
			if name == "legacy" {
				f.seedCrew(t, "native-init-panel-crew", "Panel", "native-init-panel-crew")
				f.seedPanel(t, "native-init-panel", page, "native-init-panel-crew")
			}
			bearer := owner
			if name == "nonowner" {
				bearer = other
			}
			if name == "readscope" {
				bearer = mintIngressScopedToken(t, f.db, f.userID, "native-initialize-read-only", "pages:read")
			}
			body := map[string]any{"application_id": "wordcraft", "bytes": []byte(`{"v":1}`)}
			expected := 403
			switch name {
			case "legacy":
				expected = 409
			case "forgedscope":
				body["page_id"] = "some-other-page"
				expected = 400
			case "forgedpin":
				body["cli_path"] = "/bin/true"
				expected = 400
			case "wrongapp":
				body["application_id"] = "gridcraft"
				expected = 400
			case "invalidengine":
				body["bytes"] = []byte("not native JSON")
				expected = 422
			}
			call := craftNativeInitializeTCP(t, router, f.wsID)
			status, _ := call("POST", page, "initialize", bearer, "", body)
			if status != expected {
				t.Errorf("denied initialize %s expected%d got%d", name, expected, status)
			}
			craftNativeInitializeNoPartial(t, f, router, page)
		})
	}
}
func TestCraftNativeInitializeFinalMembershipRevocationRollsBackEverything(t *testing.T) {
	f, router, _, owner, _, _ := craftNativeDataTCPFixture(t, true)
	const page = "native-init-revoke"
	f.seedPage(t, page, page, "Atomic initialization", f.userID, "")
	mustExec(t, f.db, `CREATE TRIGGER native_init_revoke_member AFTER INSERT ON artifact_versions WHEN NEW.page_id='native-init-revoke' BEGIN DELETE FROM workspace_members WHERE workspace_id=NEW.workspace_id AND user_id=(SELECT owner_user_id FROM pages WHERE id=NEW.page_id); END`)
	call := craftNativeInitializeTCP(t, router, f.wsID)
	status, _ := call("POST", page, "initialize", owner, "", map[string]any{"application_id": "wordcraft", "bytes": []byte(`{"v":1}`)})
	if status != 403 {
		t.Errorf("final revoked membership must refuse publication: got%d", status)
	}
	craftNativeInitializeNoPartial(t, f, router, page)
	var exists int
	if err := f.db.QueryRow(`SELECT count(*) FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.wsID, f.userID).Scan(&exists); err != nil || exists != 1 {
		t.Fatalf("publication rollback failed to undo injected revocation: %d %v", exists, err)
	}
}

func TestCraftNativeInitializeUnavailableWithoutQualifiedStartupProfile(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	const page = "native-init-unavailable"
	f.seedPage(t, page, page, "Unqualified initializer", f.userID, "")
	router, err := NewRouter(f.db, "native-init-unavailable-secret-32chars", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	bearer := mintIngressScopedToken(t, f.db, f.userID, "native-init-unavailable-owner", "pages:*")
	call := craftNativeInitializeTCP(t, router, f.wsID)
	status, _ := call("POST", page, "initialize", bearer, "", map[string]any{"application_id": "wordcraft", "bytes": []byte(`{"v":1}`)})
	if status != 503 {
		t.Errorf("unqualified native initializer expected503 got%d", status)
	}
	craftNativeInitializeNoPartial(t, f, router, page)
}

func TestCraftNativeInitializeExistingCollaborationReturnsConflictOverTCP(t *testing.T) {
	for _, command := range []bool{false, true} {
		t.Run(map[bool]string{false: "causal", true: "command"}[command], func(t *testing.T) {
			f, router, _, owner, _, _ := craftNativeDataTCPFixture(t, true)
			const page = "native-init-existing-c1"
			f.seedPage(t, page, page, "Existing collaboration", f.userID, "")
			craftNativeSeedCollaboration(t, f, page, command)
			call := craftNativeInitializeTCP(t, router, f.wsID)
			// Calibration: this authenticated owner can still read this personal Page.
			if status, _ := call("GET", page, "", owner, "", nil); status != 200 {
				t.Fatalf("owner Page GET calibration expected200 got%d", status)
			}
			status, _ := call("POST", page, "initialize", owner, "", map[string]any{"application_id": "wordcraft", "bytes": []byte(`{"v":1}`)})
			if status != 409 {
				t.Errorf("existing collaboration must refuse native initialization: expected409 got%d", status)
			}
			craftNativeInitializeNoPartial(t, f, router, page)
		})
	}
}
