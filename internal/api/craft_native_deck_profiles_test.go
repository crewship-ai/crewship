package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCraftNativeDeckProfileRequiresQualification(t *testing.T) {
	p := craftNativeProfileFixture(t)
	p.Application = "deckcraft"
	profiles, err := qualifyCraftNativeProfiles(map[string]CraftNativeDocumentProfile{"deckcraft": p})
	if err != nil || profiles["deckcraft"].Store.Validate == nil {
		t.Fatalf("Deck profile qualification: %v", err)
	}
	p.CLISHA256 = "unqualified"
	if _, err = qualifyCraftNativeProfiles(map[string]CraftNativeDocumentProfile{"deckcraft": p}); err == nil {
		t.Fatal("unqualified Deck CLI admitted")
	}
}
func TestCraftNativeDeckStatusIsIndependentAndDefaultOff(t *testing.T) {
	for _, mode := range []string{"disabled", "unqualified", "word-only", "deck-only", "both"} {
		t.Run(mode, func(t *testing.T) {
			human := &craftNativeHTTP{enabled: mode != "disabled"}
			docs := newCraftNativeDocumentsHTTP(human, nil)
			if mode == "disabled" || mode == "deck-only" || mode == "both" {
				docs.applications["deckcraft"] = craftNativeApplication{}
			}
			if mode == "word-only" || mode == "both" {
				docs.applications["wordcraft"] = craftNativeApplication{}
			}
			r := &Router{pages: &PageHandler{}, craftNativeHTTP: human, craftNativeDocuments: docs}
			raw, err := json.Marshal(r.documentAvailability(httptest.NewRequest("GET", "/api/v1/documents/status", nil)))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err = json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if got, present := fields["deckcraft_native"]; !present || got != (mode == "deck-only" || mode == "both") {
				t.Fatalf("Deck independent status %s: %s", mode, raw)
			}
			if fields["enabled"] != false || fields["gridcraft_native"] != false {
				t.Fatal("Deck status enabled legacy Docs or unrelated Grid")
			}
		})
	}
}

func TestCraftNativeDeckProfileDoesNotEnableAgentCommands(t *testing.T) {
	for _, application := range []string{"deckcraft", "", "othercraft"} {
		if craftNativeAgentApplicationSupported(application) {
			t.Fatalf("unqualified agent application admitted: %q", application)
		}
	}
	for _, application := range []string{"wordcraft", "gridcraft"} {
		if !craftNativeAgentApplicationSupported(application) {
			t.Fatalf("qualified agent application withdrawn: %q", application)
		}
	}
}

func TestCraftNativeDeckInitializeUnqualifiedOrDisabledLeavesNoAuthority(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "unqualified"}[enabled], func(t *testing.T) {
			f, native, _ := craftNativeHTTPFixture(t)
			const page = "deck-unqualified-init"
			f.seedPage(t, page, page, "Unqualified Deck", f.userID, "")
			router, err := NewRouter(f.db, "deck-unqualified-fixture-secret32", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: enabled, newOwnerClock: func() (mooring.OwnerClock, error) { return native.clock, nil }}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			bearer := mintIngressScopedToken(t, f.db, f.userID, "deck-unqualified-owner", "pages:*")
			call := craftNativeInitializeTCP(t, router, f.wsID)
			status, _ := call("POST", page, "initialize", bearer, "", map[string]any{"application_id": "deckcraft", "bytes": []byte("not an archive")})
			if status != 503 {
				t.Fatalf("unqualified Deck initializer: %d", status)
			}
			craftNativeInitializeNoPartial(t, f, router, page)
		})
	}
}

// Synthetic readiness pointers exercise only the availability capability fence,
// never a provider, dispatcher execution, or agent qualification.
func TestCraftNativeDeckAvailabilityWithReadyManagerRemainsHumanOnly(t *testing.T) {
	f, h, ctx := craftNativeHTTPFixture(t)
	h.agents = &craftNativeAgentManager{enabled: true, ctx: ctx, runtime: &craftNativeAgentRuntime{}, dispatcher: &dispatch.Dispatcher{}}
	if !h.agents.runtimeReady() {
		t.Fatal("readiness fixture is not calibrated")
	}
	for _, app := range []string{"wordcraft", "gridcraft", "deckcraft"} {
		page := "native-availability-" + app
		f.seedPage(t, page, page, "Native capability fixture", f.userID, "")
		mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) SELECT ?,workspace_id,?,engine_revision,engine_sha256,editor_bundle FROM craft_native_documents WHERE page_id='native-page'`, page, app)
		req := httptest.NewRequest("GET", "/availability", nil).WithContext(ctx)
		req.SetPathValue("slug", page)
		w := httptest.NewRecorder()
		h.availability(w, req)
		var status craftNativeAvailabilityResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || !status.Enabled || status.AgentsEnabled != (app != "deckcraft") {
			t.Fatalf("%s human/agent capability fence: %d %s", app, w.Code, w.Body)
		}
	}
}
