package main

// Acceptance for `crewship inbox read`, driven through the BUILT BINARY.
//
// A7 (PRD-ISSUES-AND-ROUTINES-2026.md §9.7) made inbox read state per-caller:
// PATCH /api/v1/inbox/{id} state=read only ever marks the item read for the
// user who sent the request, and the `state` a subsequent GET/list returns is
// computed for THAT caller (a role-targeted item another recipient already
// read still shows unread for someone who hasn't opened it). The CLI's job in
// this contract is narrow but load-bearing: it must send exactly
// `{"state":"read"}` to the item's own PATCH route (not silently widen it, not
// hit a sibling route), and it must print back whatever `state` the server
// computed for the caller rather than assuming "read" locally — the server is
// the only party that knows the caller's row in inbox_item_reads.
//
// A stubbed unit test against the command's Go code would not catch a client
// that guesses the post-PATCH state instead of asking the server for it; this
// drives the real binary against an httptest server and asserts on the wire.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type inboxReadStateStub struct {
	mu         sync.Mutex
	patchCalls []struct {
		path string
		body map[string]string
	}
}

func (s *inboxReadStateStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/inbox/ibx_shared_1":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			s.patchCalls = append(s.patchCalls, struct {
				path string
				body map[string]string
			}{path: r.URL.Path, body: body})
			s.mu.Unlock()
			// Mirrors PatchState's real response shape (internal/api/inbox_handler.go):
			// {"id": ..., "state": ...} — nothing more.
			_, _ = w.Write([]byte(`{"id":"ibx_shared_1","state":"read"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/inbox":
			// The list fixture stands in for the per-caller computed state
			// (inboxEffectiveStateExpr): this item is targeted at a role two
			// people share, and THIS caller has now read it, so the server
			// reports "read" — even though another recipient who hasn't
			// opened it would get "unread" back for the identical row.
			_, _ = w.Write([]byte(`{
				"rows": [
					{"id":"ibx_shared_1","kind":"message","source_id":"src_1",
					 "title":"Review production deploy","sender_type":"agent","sender_name":"Daniel",
					 "state":"read","priority":"high","blocking":false,
					 "created_at":"2026-08-30T09:00:00Z"}
				],
				"count": 1,
				"unread_count": 0,
				"has_more": false
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runInboxReadStateCLI(t *testing.T, serverURL string, args ...string) (string, error) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAcceptance_InboxRead_PerCallerState(t *testing.T) {
	stub := &inboxReadStateStub{}
	srv := stub.start(t)

	// (a) `crewship inbox read` hits the item's own PATCH route with
	// exactly state=read — not a bulk endpoint, not a different id.
	out, err := runInboxReadStateCLI(t, srv.URL, "inbox", "read", "ibx_shared_1")
	if err != nil {
		t.Fatalf("inbox read: %v\noutput: %s", err, out)
	}
	stub.mu.Lock()
	calls := append([]struct {
		path string
		body map[string]string
	}(nil), stub.patchCalls...)
	stub.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("PATCH called %d times, want 1: %+v", len(calls), calls)
	}
	if calls[0].path != "/api/v1/inbox/ibx_shared_1" {
		t.Errorf("PATCH path = %q, want /api/v1/inbox/ibx_shared_1", calls[0].path)
	}
	if calls[0].body["state"] != "read" {
		t.Errorf("PATCH body state = %q, want %q", calls[0].body["state"], "read")
	}

	// (b) A subsequent list shows the item's state exactly as the server
	// computed it for this caller — the CLI must not derive/assume "read"
	// itself, since the same row can legitimately read "unread" for a
	// different recipient of a role-targeted item.
	out, err = runInboxReadStateCLI(t, srv.URL, "inbox", "list", "--state", "all", "--format", "json")
	if err != nil {
		t.Fatalf("inbox list: %v\noutput: %s", err, out)
	}
	var rows []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("decode list output: %v\noutput: %s", err, out)
	}
	found := false
	for _, r := range rows {
		if r.ID != "ibx_shared_1" {
			continue
		}
		found = true
		if r.State != "read" {
			t.Errorf("row state = %q, want the server's computed %q", r.State, "read")
		}
	}
	if !found {
		t.Fatalf("ibx_shared_1 not in list output: %s", out)
	}
	if strings.Contains(out, `"state":"unread"`) {
		t.Errorf("list output still shows unread for a row this caller marked read:\n%s", out)
	}
}
