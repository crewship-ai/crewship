package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1910: no API that lists or reads a waitpoint hands out a callback
// capability; the only way to one is the MANAGER+ mint, which shows it once.

func listWaitpointsAs(t *testing.T, h *PipelineHandler, user, ws, role string) (string, []map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ListPendingWaitpoints(rr, withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), user, ws, role))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	return rr.Body.String(), rows
}

func mintWaitpointCallbackAs(t *testing.T, h *PipelineHandler, user, ws, role, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("POST", "/x", nil), user, ws, role)
	req.SetPathValue("token", token)
	rr := httptest.NewRecorder()
	h.MintWaitpointCallbackToken(rr, req)
	return rr
}

func TestListPendingWaitpoints_CarriesNoCallbackCapability(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	_, tok := seedPendingWaitpoint(t, h, ws)

	for _, role := range []string{"VIEWER", "MEMBER", "OWNER"} {
		body, rows := listWaitpointsAs(t, h, user, ws, role)
		if strings.Contains(body, "callback_url") || strings.Contains(body, "waitpoint-tokens") {
			t.Fatalf("%s: list exposes a callback URL: %s", role, body)
		}
		if len(rows) != 1 || rows[0]["token"] != tok || rows[0]["callback_available"] != true {
			t.Fatalf("%s: list row = %v, want the public id with callback_available", role, rows)
		}
	}
	// The Inbox card points at the same public id, so its approve button and
	// the list agree on the handle.
	var source string
	if err := h.db.QueryRow(`SELECT source_id FROM inbox_items WHERE kind='waitpoint' AND workspace_id=?`, ws).Scan(&source); err != nil || source != tok {
		t.Fatalf("inbox source_id = %q (%v), want %q", source, err, tok)
	}
}

func TestMintWaitpointCallbackToken_RoleAndOnce(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	_, tok := seedPendingWaitpoint(t, h, ws)

	for _, role := range []string{"VIEWER", "MEMBER"} {
		if rr := mintWaitpointCallbackAs(t, h, user, ws, role, tok); rr.Code != http.StatusForbidden {
			t.Fatalf("%s minted a callback URL: %d %s", role, rr.Code, rr.Body.String())
		}
	}
	if rr := mintWaitpointCallbackAs(t, h, user, ws, "MANAGER", "wp_missing"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown waitpoint: %d, want 404", rr.Code)
	}

	rr := mintWaitpointCallbackAs(t, h, user, ws, "MANAGER", tok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("mint: %d %s", rr.Code, rr.Body.String())
	}
	var minted struct {
		Token       string `json:"token"`
		CallbackURL string `json:"callback_url"`
		Rotated     bool   `json:"rotated"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &minted); err != nil {
		t.Fatal(err)
	}
	secret := minted.CallbackURL[strings.LastIndex(minted.CallbackURL, "/")+1:]
	if minted.Token != tok || minted.Rotated || !strings.HasPrefix(secret, "wpt_") {
		t.Fatalf("mint response = %+v", minted)
	}
	// Shown once: it is not in the list and not in the database.
	if body, _ := listWaitpointsAs(t, h, user, ws, "OWNER"); strings.Contains(body, secret) {
		t.Fatal("list returns the minted secret")
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pipeline_waitpoints WHERE token = ? OR callback_token_hash = ?`, secret, secret).Scan(&n); err != nil || n != 0 {
		t.Fatalf("minted secret stored in the clear (%d, %v)", n, err)
	}

	// The minted secret is what the public callback takes; the id is not.
	call := func(path string) int {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"approved":true}`))
		req.SetPathValue("token", path)
		rr := httptest.NewRecorder()
		h.CompleteWaitpointToken(rr, req)
		return rr.Code
	}
	if code := call(tok); code != http.StatusConflict {
		t.Fatalf("callback with the public id: %d, want 409", code)
	}
	if status, _ := waitpointRow(t, h, tok); status != "pending" {
		t.Fatalf("public id moved the waitpoint to %s", status)
	}
	if code := call(secret); code != http.StatusOK {
		t.Fatalf("callback with the minted secret: %d, want 200", code)
	}
	if status, by := waitpointRow(t, h, tok); status != "approved" || by != "external-callback" {
		t.Fatalf("waitpoint = (%s, %q)", status, by)
	}
	if rr := mintWaitpointCallbackAs(t, h, user, ws, "MANAGER", tok); rr.Code != http.StatusConflict {
		t.Fatalf("mint on a decided waitpoint: %d, want 409", rr.Code)
	}
}
