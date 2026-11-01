package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCraftCLINativeDocumentActionsUseBoundSessionAndAllRoutes(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/health/craft"
	secret := strings.Repeat("a", 64)
	stub.OnPost(base+"/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"public-native-session","application_id":"wordcraft"}`), "application/json"
	})
	stub.OnGet(base+"/document", func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != secret {
			t.Error("unbound document read")
		}
		return 200, []byte(`{"version":"1","bytes":"e30="}`), "application/json"
	})
	for _, operation := range []string{"save", "import", "restore", "reconcile"} {
		stub.OnPost(base+"/document/"+operation, func(r *http.Request, body []byte) (int, []byte, string) {
			if r.Header.Get("X-Craft-Session") != secret {
				t.Error("unbound document mutation")
			}
			if !strings.Contains(string(body), `"generation":"9007199254740993"`) {
				t.Errorf("lost canonical generation: %s", body)
			}
			return 200, []byte(`{"version":"2","durability":"node"}`), "application/json"
		})
	}
	snapshot := `{"workspaceId":"` + covWSCli3 + `","pageId":"p","applicationId":"wordcraft","enginePin":"` + strings.Repeat("b", 64) + `","sessionId":"public-native-session","generation":"9007199254740993","baseVersion":"1","editorRevision":"opaque:1:2","contentHash":"sha256:` + strings.Repeat("c", 64) + `","bytes":"e30=","targetVersion":"1"}`
	input := `{"action":"read"}` + "\n"
	for _, action := range []string{"save", "import", "restore", "reconcile"} {
		input += `{"action":"` + action + `","snapshot":` + snapshot + `}` + "\n"
	}
	out, err := runPageCLI(t, input, "craft", "connect", "health")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "session_token") {
		t.Fatal("private admission leaked")
	}
	for _, operation := range []string{"save", "import", "restore", "reconcile"} {
		if len(stub.CallsFor("POST", base+"/document/"+operation)) != 1 {
			t.Errorf("missing document operation %s", operation)
		}
	}
	if len(stub.CallsFor("GET", base+"/document")) != 1 {
		t.Error("missing document read")
	}
}

func TestCraftCLINativeDocumentPreservesFiveMiBAndRejectsForeignSession(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/health/craft"
	secret := strings.Repeat("a", 64)
	stub.OnPost(base+"/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"large-native-session"}`), "application/json"
	})
	native := bytes.Repeat([]byte{7}, 5<<20)
	snapshot := craftDocumentSnapshot{WorkspaceID: covWSCli3, PageID: "p", ApplicationID: "wordcraft", EnginePin: strings.Repeat("b", 64), SessionID: "large-native-session", Generation: "9007199254740993", BaseVersion: "1", EditorRevision: "opaque:large", ContentHash: "sha256:" + strings.Repeat("c", 64), Bytes: native}
	stub.OnPost(base+"/document/save", func(r *http.Request, raw []byte) (int, []byte, string) {
		var got craftDocumentSnapshot
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes, native) || got.Generation != snapshot.Generation {
			t.Error("large native bytes or canonical generation changed")
		}
		return 200, []byte(`{"version":"2"}`), "application/json"
	})
	raw, err := json.Marshal(craftAction{Action: "save", Snapshot: &snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runPageCLI(t, string(raw)+"\n", "craft", "connect", "health"); err != nil {
		t.Fatal(err)
	}
	snapshot.SessionID = "foreign-public-session"
	raw, err = json.Marshal(craftAction{Action: "save", Snapshot: &snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runPageCLI(t, string(raw)+"\n", "craft", "connect", "health"); err == nil {
		t.Fatal("accepted foreign public session binding")
	}
	if len(stub.CallsFor("POST", base+"/document/save")) != 1 {
		t.Fatal("foreign session reached transport")
	}
	snapshot.SessionID = "large-native-session"
	snapshot.Bytes = make([]byte, (32<<20)+1)
	if err = craftValidateDocumentAction(craftAction{Action: "import", Snapshot: &snapshot}, snapshot.SessionID); err == nil {
		t.Fatal("accepted native bytes above32MiB")
	}
}

func TestCraftCLIVersionsUsesBoundSessionAndNeverPrintsIt(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/health/craft"
	secret := strings.Repeat("d", 64)
	stub.OnPost(base+"/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"public-history-session","application_id":"wordcraft"}`), "application/json"
	})
	stub.OnGet(base+"/document/versions", func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != secret {
			t.Error("unbound version listing")
		}
		return 200, []byte(`{"currentVersion":"2","truncated":false,"versions":[{"version":"2","createdAt":"2026-10-31T10:00:00Z","author":{"actorId":"agent:a","kind":"agent","displayName":"Agent"},"current":true},{"version":"1","createdAt":"2026-10-30T10:00:00Z","author":{"actorId":"user:u","kind":"human","displayName":"Pavel"},"current":false}]}`), "application/json"
	})
	out, err := runPageCLI(t, "", "craft", "versions", "health")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "session_token") || !strings.Contains(out, "currentVersion") {
		t.Fatalf("versions output: %s", out)
	}
	if _, err = runPageCLI(t, `{"action":"versions"}`+"\n", "craft", "connect", "health"); err != nil {
		t.Fatal(err)
	}
	if got := len(stub.CallsFor("GET", base+"/document/versions")); got != 2 {
		t.Fatalf("version listings %d, want 2", got)
	}
	if len(stub.CallsFor("POST", base+"/document/restore")) != 0 {
		t.Fatal("listing wrote")
	}
}
