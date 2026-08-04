package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// appearanceFixture seeds one routine and returns the handler, its
// workspace, and the routine as saved — the last so a test can assert
// the definition hash did not move.
func appearanceFixture(t *testing.T, slug string) (*PipelineHandler, string, *pipeline.Pipeline) {
	t.Helper()
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-"+slug, slug, 1)
	appearanceUser = userID
	p, err := h.store.GetBySlug(context.Background(), wsID, slug)
	if err != nil {
		t.Fatalf("seed lookup: %v", err)
	}
	return h, wsID, p
}

// appearanceUser carries the seeded user id between the fixture and
// patchAppearance so each test body stays about the assertion.
var appearanceUser string

func patchAppearance(t *testing.T, h *PipelineHandler, wsID, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(body))
	req.SetPathValue("slug", slug)
	req = withWorkspaceUser(req, appearanceUser, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SetAppearance(rr, req)
	return rr
}

// Appearance is presentation, and the reason it is its own endpoint is
// that Save is not: Save rewrites the definition, recomputes
// definition_hash, can mint a version and re-runs the risk classifier.
// Picking a colour must do none of that.
//
// The partial-update contract is the subtle part. An ABSENT field means
// "leave it alone"; an explicit "" means "clear it". Without that
// distinction there is no way to set a colour without wiping an icon
// the caller never mentioned — and the CLI relies on it.

func TestSetAppearance_SetsBothFields(t *testing.T) {
	h, ws, _ := appearanceFixture(t, "appearance-both")

	rr := patchAppearance(t, h, ws, "appearance-both", `{"icon":"receipt","color":"amber"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"icon":"receipt"`) || !strings.Contains(body, `"color":"amber"`) {
		t.Fatalf("response did not echo the appearance: %s", body)
	}
}

func TestSetAppearance_AbsentFieldIsLeftAlone(t *testing.T) {
	h, ws, _ := appearanceFixture(t, "appearance-partial")

	patchAppearance(t, h, ws, "appearance-partial", `{"icon":"rocket","color":"violet"}`)
	// Only the colour is named. The icon must survive — a client
	// changing one field cannot be made to resend the other.
	rr := patchAppearance(t, h, ws, "appearance-partial", `{"color":"cyan"}`)
	body := rr.Body.String()
	if !strings.Contains(body, `"icon":"rocket"`) {
		t.Fatalf("absent icon was wiped: %s", body)
	}
	if !strings.Contains(body, `"color":"cyan"`) {
		t.Fatalf("colour did not update: %s", body)
	}
}

func TestSetAppearance_EmptyStringClears(t *testing.T) {
	h, ws, _ := appearanceFixture(t, "appearance-clear")

	patchAppearance(t, h, ws, "appearance-clear", `{"icon":"rocket","color":"violet"}`)
	rr := patchAppearance(t, h, ws, "appearance-clear", `{"icon":"","color":""}`)
	body := rr.Body.String()
	// omitempty: cleared fields drop out of the response entirely, which
	// is how the client tells "unset" from "set to something blank".
	if strings.Contains(body, `"icon"`) || strings.Contains(body, `"color"`) {
		t.Fatalf("appearance was not cleared: %s", body)
	}
}

func TestSetAppearance_DoesNotMoveTheDefinitionHash(t *testing.T) {
	h, ws, before := appearanceFixture(t, "appearance-hash")

	patchAppearance(t, h, ws, "appearance-hash", `{"icon":"rocket","color":"violet"}`)

	after, err := h.store.GetBySlug(context.Background(), ws, "appearance-hash")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// The entire reason this is a column and not a definition key. If
	// this fails, recolouring has started minting routine versions and
	// invalidating save tokens issued against the old hash.
	if after.DefinitionHash != before.DefinitionHash {
		t.Fatalf("definition hash moved on a recolour: %q -> %q",
			before.DefinitionHash, after.DefinitionHash)
	}
}

func TestSetAppearance_RejectsOverlongValues(t *testing.T) {
	h, ws, _ := appearanceFixture(t, "appearance-long")

	rr := patchAppearance(t, h, ws, "appearance-long",
		`{"icon":"`+strings.Repeat("x", 200)+`"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for an overlong icon, got %d", rr.Code)
	}
}

func TestSetAppearance_UnknownRoutineIs404(t *testing.T) {
	h, ws, _ := appearanceFixture(t, "appearance-404")

	rr := patchAppearance(t, h, ws, "no-such-routine", `{"icon":"rocket"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}
