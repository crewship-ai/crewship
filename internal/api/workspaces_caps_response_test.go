package api

// Tests for the currentUserCapabilities field on the workspace
// list/get responses (#1034). The frontend ability layer consumes
// this so UI can gate on per-membership capability grants (e.g. show
// Rotate to a MANAGER holding credential.rotate) instead of role
// alone. Resolution must match the runtime gate exactly:
//
//   - explicit capabilities JSON → that set verbatim
//   - NULL capabilities         → role-derived fallback bundle
//   - drained/malformed JSON    → chat-only baseline
//
// (see resolveCapabilitiesFromRow in capabilities_check.go).

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func newWsCapsHandler(t *testing.T) (*WorkspaceHandler, *testWsCapsFixture) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	f := &testWsCapsFixture{userID: "wscaps-user", wsID: "wscaps-ws"}
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'wscaps@x.com', 'C')`, f.userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Caps', 'caps')`, f.wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return NewWorkspaceHandler(db, logger), f
}

type testWsCapsFixture struct {
	userID string
	wsID   string
}

// seedMember inserts the fixture membership row. capsJSON == "" seeds
// a NULL capabilities column (the legacy / pre-backfill shape).
func (f *testWsCapsFixture) seedMember(t *testing.T, h *WorkspaceHandler, role, capsJSON string) {
	t.Helper()
	var err error
	if capsJSON == "" {
		_, err = h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wscaps-m', ?, ?, ?)`,
			f.wsID, f.userID, role)
	} else {
		_, err = h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES ('wscaps-m', ?, ?, ?, ?)`,
			f.wsID, f.userID, role, capsJSON)
	}
	if err != nil {
		t.Fatalf("seed member: %v", err)
	}
	// The Get path reads through the process-global capability cache —
	// drop any entry a previous test may have pinned for these ids.
	InvalidateCapabilityCache(f.wsID, f.userID)
}

func listWorkspaceCaps(t *testing.T, h *WorkspaceHandler, f *testWsCapsFixture) []string {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: f.userID, Email: "wscaps@x.com"}))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", rr.Code, rr.Body.String())
	}
	var result []workspaceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	return result[0].CurrentUserCapabilities
}

func TestWorkspaceList_CurrentUserCapabilities_Explicit(t *testing.T) {
	h, f := newWsCapsHandler(t)
	f.seedMember(t, h, "MEMBER", `["chat","credential.rotate"]`)

	got := listWorkspaceCaps(t, h, f)
	want := []string{"chat", "credential.rotate"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("capabilities = %v, want %v", got, want)
	}
}

func TestWorkspaceList_CurrentUserCapabilities_NullFallsBackToRole(t *testing.T) {
	h, f := newWsCapsHandler(t)
	f.seedMember(t, h, "MANAGER", "") // NULL column → MANAGER bundle

	got := listWorkspaceCaps(t, h, f)
	want := []string{"chat", "issue.create", "memory.write", "routine.create"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("capabilities = %v, want %v", got, want)
	}
}

func TestWorkspaceList_CurrentUserCapabilities_DrainedIsChatOnly(t *testing.T) {
	h, f := newWsCapsHandler(t)
	// Explicit-but-empty set: deliberate operator strip-back — must NOT
	// silently restore the role bundle.
	f.seedMember(t, h, "MANAGER", `[]`)

	got := listWorkspaceCaps(t, h, f)
	want := []string{"chat"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("capabilities = %v, want %v", got, want)
	}
}

func TestWorkspaceGet_CurrentUserCapabilities(t *testing.T) {
	h, f := newWsCapsHandler(t)
	f.seedMember(t, h, "MEMBER", `["chat","credential.create"]`)

	req := httptest.NewRequest("GET", "/api/v1/workspaces/"+f.wsID, nil)
	req = withWorkspaceUser(req, f.userID, f.wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", rr.Code, rr.Body.String())
	}
	var ws workspaceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &ws); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{"chat", "credential.create"}
	if !reflect.DeepEqual(ws.CurrentUserCapabilities, want) {
		t.Errorf("capabilities = %v, want %v", ws.CurrentUserCapabilities, want)
	}
}
