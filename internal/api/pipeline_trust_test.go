package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// grantTrustReq builds a POST .../trust request for slug as the given role.
func grantTrustReq(t *testing.T, userID, wsID, slug, role, body string) *http.Request {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("POST", "/x", strings.NewReader(body)), userID, wsID, role)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("slug", slug)
	return req
}

// seedTrustableRoutine saves a routine and returns its slug + current
// definition hash.
func seedTrustableRoutine(t *testing.T, h *PipelineHandler, wsID, slug string) string {
	t.Helper()
	crewID := seedCrewRow(t, h.db, "crew_"+slug, wsID, "Eng", "eng-"+slug)
	_ = seedAgentRow(t, h.db, "ag_"+slug, wsID, crewID, "Eva", "eva-"+slug, "LEAD")
	if rr := doInternalSave(t, h, internalSaveBody(t, wsID, slug, crewID, httpRoutineDef())); rr.Code != http.StatusCreated {
		t.Fatalf("save status=%d; body=%s", rr.Code, rr.Body.String())
	}
	p, err := h.store.GetBySlug(t.Context(), wsID, slug)
	if err != nil {
		t.Fatalf("load routine: %v", err)
	}
	return p.DefinitionHash
}

// TestTrustGrantAPI_Grant covers the write path an operator reaches by
// answering "stop asking me" on an inbox card.
func TestTrustGrantAPI_Grant(t *testing.T) {
	t.Run("manager can grant standing trust on a gate", func(t *testing.T) {
		h, userID, wsID := newPipelineHandlerForCRUDTest(t)
		hash := seedTrustableRoutine(t, h, wsID, "tg-ok")

		rr := httptest.NewRecorder()
		h.GrantTrust(rr, grantTrustReq(t, userID, wsID, "tg-ok", "MANAGER",
			`{"step_id":"publish","reason":"approved 12x, always identical","prior_approvals":12}`))
		if rr.Code != http.StatusCreated {
			t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
		}
		var got struct {
			ID             string `json:"id"`
			StepID         string `json:"step_id"`
			DefinitionHash string `json:"definition_hash"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.StepID != "publish" {
			t.Errorf("step_id=%q want publish", got.StepID)
		}
		// The server pins the grant to what is saved RIGHT NOW, so a
		// client cannot nominate a body it likes better.
		if got.DefinitionHash != hash {
			t.Errorf("definition_hash=%q want the routine's current %q", got.DefinitionHash, hash)
		}
	})

	// The operator answered a question about a specific routine body. If
	// the routine moved between the card being rendered and the click,
	// granting the CURRENT body would trust something they never read.
	t.Run("stale definition hash is refused", func(t *testing.T) {
		h, userID, wsID := newPipelineHandlerForCRUDTest(t)
		_ = seedTrustableRoutine(t, h, wsID, "tg-stale")

		rr := httptest.NewRecorder()
		h.GrantTrust(rr, grantTrustReq(t, userID, wsID, "tg-stale", "MANAGER",
			`{"step_id":"publish","definition_hash":"a-hash-from-an-older-card"}`))
		if rr.Code != http.StatusConflict {
			t.Fatalf("status=%d want 409 for a hash that no longer matches; body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("viewer cannot grant", func(t *testing.T) {
		h, userID, wsID := newPipelineHandlerForCRUDTest(t)
		_ = seedTrustableRoutine(t, h, wsID, "tg-viewer")

		rr := httptest.NewRecorder()
		h.GrantTrust(rr, grantTrustReq(t, userID, wsID, "tg-viewer", "VIEWER", `{"step_id":"publish"}`))
		if rr.Code != http.StatusForbidden {
			t.Errorf("status=%d want 403 — disarming a gate is a MANAGER+ act", rr.Code)
		}
	})

	t.Run("step_id is required", func(t *testing.T) {
		h, userID, wsID := newPipelineHandlerForCRUDTest(t)
		_ = seedTrustableRoutine(t, h, wsID, "tg-nostep")

		rr := httptest.NewRecorder()
		h.GrantTrust(rr, grantTrustReq(t, userID, wsID, "tg-nostep", "MANAGER", `{}`))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status=%d want 400 — a grant with no step would be routine-wide trust", rr.Code)
		}
	})

	t.Run("unknown routine is 404", func(t *testing.T) {
		h, userID, wsID := newPipelineHandlerForCRUDTest(t)
		rr := httptest.NewRecorder()
		h.GrantTrust(rr, grantTrustReq(t, userID, wsID, "no-such-routine", "MANAGER", `{"step_id":"publish"}`))
		if rr.Code != http.StatusNotFound {
			t.Errorf("status=%d want 404", rr.Code)
		}
	})

	t.Run("re-granting the same gate is a conflict, not a silent reset", func(t *testing.T) {
		h, userID, wsID := newPipelineHandlerForCRUDTest(t)
		_ = seedTrustableRoutine(t, h, wsID, "tg-dup")

		first := httptest.NewRecorder()
		h.GrantTrust(first, grantTrustReq(t, userID, wsID, "tg-dup", "MANAGER", `{"step_id":"publish"}`))
		if first.Code != http.StatusCreated {
			t.Fatalf("first grant status=%d; body=%s", first.Code, first.Body.String())
		}
		second := httptest.NewRecorder()
		h.GrantTrust(second, grantTrustReq(t, userID, wsID, "tg-dup", "MANAGER", `{"step_id":"publish"}`))
		if second.Code != http.StatusConflict {
			t.Errorf("status=%d want 409 — a second grant would reset the use counter", second.Code)
		}
	})
}

// TestTrustGrantAPI_ListAndRevoke covers the surfaces that make a grant
// reversible. A standing grant nobody can find or withdraw is a liability.
func TestTrustGrantAPI_ListAndRevoke(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	_ = seedTrustableRoutine(t, h, wsID, "tg-life")

	create := httptest.NewRecorder()
	h.GrantTrust(create, grantTrustReq(t, userID, wsID, "tg-life", "MANAGER", `{"step_id":"publish","max_uses":5}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("grant status=%d; body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode grant: %v", err)
	}

	listReq := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "VIEWER")
	listReq.SetPathValue("slug", "tg-life")
	listRR := httptest.NewRecorder()
	h.ListTrustGrants(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status=%d want 200; body=%s", listRR.Code, listRR.Body.String())
	}
	var listed struct {
		Grants []struct {
			ID     string `json:"id"`
			Live   bool   `json:"live"`
			StepID string `json:"step_id"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Grants) != 1 || listed.Grants[0].ID != created.ID {
		t.Fatalf("list = %+v, want the one grant just created", listed.Grants)
	}
	if !listed.Grants[0].Live {
		t.Error("freshly granted trust is not reported live")
	}

	revReq := withWorkspaceUser(httptest.NewRequest("DELETE", "/x", nil), userID, wsID, "MANAGER")
	revReq.SetPathValue("slug", "tg-life")
	revReq.SetPathValue("grantId", created.ID)
	revRR := httptest.NewRecorder()
	h.RevokeTrust(revRR, revReq)
	if revRR.Code != http.StatusOK {
		t.Fatalf("revoke status=%d want 200; body=%s", revRR.Code, revRR.Body.String())
	}

	afterRR := httptest.NewRecorder()
	h.ListTrustGrants(afterRR, listReq)
	var after struct {
		Grants []struct {
			Live bool `json:"live"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(afterRR.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode list after revoke: %v", err)
	}
	if len(after.Grants) != 1 {
		t.Fatalf("revoked grant vanished from the list — the audit cannot answer who withdrew trust")
	}
	if after.Grants[0].Live {
		t.Error("revoked grant still reports live")
	}

	// Revoking twice is a no-op, not a 500.
	againRR := httptest.NewRecorder()
	h.RevokeTrust(againRR, revReq)
	if againRR.Code != http.StatusNotFound {
		t.Errorf("second revoke status=%d want 404", againRR.Code)
	}
}

// TestTrustGrantAPI_RevokeIsScopedToTheRoutineInTheURL pins that the
// slug is part of the predicate. Grant ids are workspace-unique, so a
// revoke that ignored the slug would let a request naming one routine
// retire another routine's grant — the URL would be describing something
// other than what happened.
func TestTrustGrantAPI_RevokeIsScopedToTheRoutineInTheURL(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	_ = seedTrustableRoutine(t, h, wsID, "tg-owner")
	_ = seedTrustableRoutine(t, h, wsID, "tg-bystander")

	create := httptest.NewRecorder()
	h.GrantTrust(create, grantTrustReq(t, userID, wsID, "tg-owner", "MANAGER", `{"step_id":"review"}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("grant status=%d; body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode grant: %v", err)
	}

	// Same workspace, same role — but the wrong routine in the path.
	wrong := withWorkspaceUser(httptest.NewRequest("DELETE", "/x", nil), userID, wsID, "MANAGER")
	wrong.SetPathValue("slug", "tg-bystander")
	wrong.SetPathValue("grantId", created.ID)
	wrongRR := httptest.NewRecorder()
	h.RevokeTrust(wrongRR, wrong)
	if wrongRR.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 — a grant was revoked through an unrelated routine's URL", wrongRR.Code)
	}

	// The grant is genuinely still live.
	listReq := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "VIEWER")
	listReq.SetPathValue("slug", "tg-owner")
	listRR := httptest.NewRecorder()
	h.ListTrustGrants(listRR, listReq)
	var listed struct {
		Grants []struct {
			Live bool `json:"live"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Grants) != 1 || !listed.Grants[0].Live {
		t.Errorf("grant did not survive the mis-scoped revoke: %+v", listed.Grants)
	}
}

// TestTrustGrantAccessor_IsRaceFree guards the accessor that serves the
// grant store. An earlier version memoised it lazily on the shared
// handler — a write to a field concurrent requests also read, i.e. a data
// race introduced, ironically, while making the handlers share one store.
//
// This hammers trustGrants() DIRECTLY rather than driving HTTP handlers.
// Going through a handler does not work: database/sql takes internal
// mutexes on the way to the DB, and the happens-before edges those create
// between the goroutines hide the unsynchronised field write from the
// race detector. A concurrency test that cannot fail is worse than none,
// because it reads as coverage.
func TestTrustGrantAccessor_IsRaceFree(t *testing.T) {
	h, _, _ := newPipelineHandlerForCRUDTest(t)
	// Clear the constructor's assignment so the fallback path — the one a
	// bare struct literal takes — is what the goroutines exercise.
	h.trustGrantStore = nil

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together, no staggering
			if got := h.trustGrants(); got == nil {
				t.Error("trustGrants() returned nil")
			}
		}()
	}
	close(start)
	wg.Wait()
}
