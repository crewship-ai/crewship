package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// crewConnectionsRig builds workspace+user fixtures plus a constructed
// handler. Two crews are seeded in the primary workspace so most tests
// can immediately exercise Create without re-seeding crews. Returns the
// handler, db (for cross-workspace fixtures + post-assertions), userID,
// workspaceID, and the two crew IDs.
func crewConnectionsRig(t *testing.T) (h *CrewConnectionHandler, db *sql.DB, userID, wsID, crewA, crewB string) {
	t.Helper()
	db = setupTestDB(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h = NewCrewConnectionHandler(db, logger)

	// Seed two crews in the primary workspace. container_memory_mb +
	// container_cpus are NOT NULL on this table, so we set them explicitly
	// even though the handler doesn't read them.
	crewA = "c-alpha"
	crewB = "c-beta"
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, container_memory_mb, container_cpus)
		 VALUES (?, ?, 'Alpha', 'alpha', 4096, 2.0),
		        (?, ?, 'Beta',  'beta',  4096, 2.0)`,
		crewA, wsID, crewB, wsID); err != nil {
		t.Fatalf("seed crews: %v", err)
	}
	return h, db, userID, wsID, crewA, crewB
}

// seedOtherWorkspaceWithCrew creates an isolated second workspace +
// crew so tenant-isolation tests can attempt cross-workspace reads.
// Returns (otherWorkspaceID, otherCrewID).
func seedOtherWorkspaceWithCrew(t *testing.T, db *sql.DB) (string, string) {
	t.Helper()
	otherWS := "ws-other"
	otherCrew := "c-other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, container_memory_mb, container_cpus)
		 VALUES (?, ?, 'Other Crew', 'other-crew', 4096, 2.0)`,
		otherCrew, otherWS); err != nil {
		t.Fatalf("seed other crew: %v", err)
	}
	return otherWS, otherCrew
}

// insertConnection is a direct-DB seed used by tests that need a
// pre-existing connection row (Delete tests, List ordering tests,
// UNIQUE-conflict tests). It bypasses the handler intentionally so the
// test only asserts the handler under test, not the create path.
func insertConnection(t *testing.T, db *sql.DB, id, wsID, fromCrew, toCrew, direction string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'active', datetime('now'), datetime('now'))`,
		id, wsID, fromCrew, toCrew, direction); err != nil {
		t.Fatalf("seed connection %s: %v", id, err)
	}
}

// ── List ────────────────────────────────────────────────────────────────

// Empty workspace must serialize as `[]` rather than `null`. The handler
// pre-initializes the slice; a regression to `var result ...` would
// silently break clients that .map() over the response.
func TestCrewConnections_List_EmptyWorkspace_Returns200WithEmptyArray(t *testing.T) {
	h, _, userID, wsID, _, _ := crewConnectionsRig(t)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/crew-connections", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	// Body must be the literal JSON array `[]` (with optional trailing
	// newline). Detecting `null` here is the whole point.
	body := strings.TrimSpace(rr.Body.String())
	if body != "[]" {
		t.Fatalf("body = %q, want %q (slice must serialize empty, not null)", body, "[]")
	}
}

// List must JOIN the crews table and surface name+slug on both sides of
// the connection. The seed-data bug that caused the P0 was about ROWS
// missing; this test locks the SHAPE so the UI's "Engineering <→ QA"
// labels keep rendering once rows do exist.
func TestCrewConnections_List_HappyPath_ReturnsJoinedCrewNamesAndSlugs(t *testing.T) {
	h, db, userID, wsID, crewA, crewB := crewConnectionsRig(t)
	insertConnection(t, db, "cc-1", wsID, crewA, crewB, "bidirectional")

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/crew-connections", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp []crewConnectionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("len = %d, want 1", len(resp))
	}
	got := resp[0]
	if got.ID != "cc-1" {
		t.Errorf("id = %q, want cc-1", got.ID)
	}
	if got.FromCrewName != "Alpha" || got.FromCrewSlug != "alpha" {
		t.Errorf("from-crew label = %q/%q, want Alpha/alpha", got.FromCrewName, got.FromCrewSlug)
	}
	if got.ToCrewName != "Beta" || got.ToCrewSlug != "beta" {
		t.Errorf("to-crew label = %q/%q, want Beta/beta", got.ToCrewName, got.ToCrewSlug)
	}
	if got.Direction != "bidirectional" {
		t.Errorf("direction = %q, want bidirectional", got.Direction)
	}
	if got.Status != "active" {
		t.Errorf("status = %q, want active", got.Status)
	}
}

// Tenant isolation: a connection living in workspace A must never leak
// when the request runs under workspace B's context. The seed-data bug
// that exposed this handler to production traffic also exposes the
// scoping; if we ever drop the WHERE clause this catches it.
func TestCrewConnections_List_CrossWorkspace_DoesNotLeakRows(t *testing.T) {
	h, db, userID, wsID, crewA, crewB := crewConnectionsRig(t)
	insertConnection(t, db, "cc-ws-a", wsID, crewA, crewB, "bidirectional")

	// Build a parallel workspace + crew so we have a valid wsID to query
	// from. We deliberately do NOT seed a connection there.
	otherWS, _ := seedOtherWorkspaceWithCrew(t, db)

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/crew-connections", nil), userID, otherWS, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp []crewConnectionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp) != 0 {
		t.Fatalf("cross-workspace leak: got %d rows, want 0; rows=%+v", len(resp), resp)
	}
}

// ── Create ──────────────────────────────────────────────────────────────

// MEMBER role lacks "create"; the handler must reject before touching
// the DB. Without this gate any logged-in user could wire up cross-crew
// dispatch, which is an org-policy decision.
func TestCrewConnections_Create_MemberRole_Returns403(t *testing.T) {
	h, _, userID, wsID, crewA, crewB := crewConnectionsRig(t)
	body := `{"from_crew_id":"` + crewA + `","to_crew_id":"` + crewB + `"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// Garbage JSON in the body must surface as 400 — the readJSON helper's
// contract — not a 500 from a downstream nil deref.
func TestCrewConnections_Create_BadJSON_Returns400(t *testing.T) {
	h, _, userID, wsID, _, _ := crewConnectionsRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(`{NOT_JSON`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// Missing from_crew_id is a 400 contract violation; orchestrator code
// would happily pass a "" through which would later 500 deep in
// AreCrewsConnected. Guard the input.
func TestCrewConnections_Create_MissingFromCrew_Returns400(t *testing.T) {
	h, _, userID, wsID, _, crewB := crewConnectionsRig(t)
	body := `{"to_crew_id":"` + crewB + `"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// Symmetric guard for to_crew_id. Together with the previous test these
// pin both required-field branches.
func TestCrewConnections_Create_MissingToCrew_Returns400(t *testing.T) {
	h, _, userID, wsID, crewA, _ := crewConnectionsRig(t)
	body := `{"from_crew_id":"` + crewA + `"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// A crew connected to itself is a logic nonsense and would corrupt the
// AreCrewsConnected fast-path. The handler short-circuits before any
// DB write, so we assert 400 and zero rows.
func TestCrewConnections_Create_SelfConnection_Returns400(t *testing.T) {
	h, db, userID, wsID, crewA, _ := crewConnectionsRig(t)
	body := `{"from_crew_id":"` + crewA + `","to_crew_id":"` + crewA + `"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_connections`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("rows written despite 400: count = %d", count)
	}
}

// The handler defaults direction to "bidirectional" when omitted from
// the request body. This is the friendliest default (matches the seed
// data fix) and the contract should not silently regress to
// "unidirectional".
func TestCrewConnections_Create_OmittedDirection_DefaultsToBidirectional(t *testing.T) {
	h, db, userID, wsID, crewA, crewB := crewConnectionsRig(t)
	body := `{"from_crew_id":"` + crewA + `","to_crew_id":"` + crewB + `"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var direction string
	if err := db.QueryRow(`SELECT direction FROM crew_connections WHERE from_crew_id = ? AND to_crew_id = ?`,
		crewA, crewB).Scan(&direction); err != nil {
		t.Fatalf("read direction: %v", err)
	}
	if direction != "bidirectional" {
		t.Errorf("direction = %q, want bidirectional", direction)
	}
}

// Invalid direction values must be rejected at the handler layer; the
// CHECK constraint at the DB layer would surface as a 409 from the
// fallback error path, but the handler should pre-empt with 400 for a
// clearer client experience.
func TestCrewConnections_Create_InvalidDirection_Returns400(t *testing.T) {
	h, _, userID, wsID, crewA, crewB := crewConnectionsRig(t)
	body := `{"from_crew_id":"` + crewA + `","to_crew_id":"` + crewB + `","direction":"sideways"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// Both crews must be in the CURRENT workspace. Allowing one to live in
// a different workspace would let a caller "tether" their workspace to
// someone else's crew — a tenant-isolation breach. The handler returns
// 404 ("not found in this workspace").
func TestCrewConnections_Create_CrewFromAnotherWorkspace_Returns404(t *testing.T) {
	h, db, userID, wsID, crewA, _ := crewConnectionsRig(t)
	_, foreignCrew := seedOtherWorkspaceWithCrew(t, db)
	body := `{"from_crew_id":"` + crewA + `","to_crew_id":"` + foreignCrew + `"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// Unknown crew IDs that exist nowhere also yield 404. Covers the
// fromFound==false branch via a not-found rather than wrong-workspace
// route — the message is shared but the path differs.
func TestCrewConnections_Create_UnknownCrewID_Returns404(t *testing.T) {
	h, _, userID, wsID, crewA, _ := crewConnectionsRig(t)
	body := `{"from_crew_id":"` + crewA + `","to_crew_id":"c-does-not-exist"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// Happy path: 201 + {id: "cc_..."} payload and the row materialises in
// the DB. This is the primary case the seed-data fix relies on; a 500
// here would have re-broken cross-crew dispatch.
func TestCrewConnections_Create_HappyPath_Returns201WithID(t *testing.T) {
	h, db, userID, wsID, crewA, crewB := crewConnectionsRig(t)
	body := `{"from_crew_id":"` + crewA + `","to_crew_id":"` + crewB + `","direction":"unidirectional"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(resp.ID, "cc_") {
		t.Errorf("id = %q, want prefix 'cc_'", resp.ID)
	}

	// Verify the row actually landed with the requested direction —
	// guarding against a future change that 201s without inserting.
	var direction, status string
	if err := db.QueryRow(`SELECT direction, status FROM crew_connections WHERE id = ?`, resp.ID).
		Scan(&direction, &status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if direction != "unidirectional" {
		t.Errorf("direction = %q, want unidirectional", direction)
	}
	if status != "active" {
		t.Errorf("status = %q, want active (handler hard-codes)", status)
	}
}

// UNIQUE(from_crew_id, to_crew_id) on the crew_connections table means
// a second create for the same pair must surface as a 409. The handler
// translates any insert error into 409 with an "already exists"
// message; verifying the status keeps the contract stable for clients
// that retry idempotently.
func TestCrewConnections_Create_DuplicatePair_Returns409(t *testing.T) {
	h, _, userID, wsID, crewA, crewB := crewConnectionsRig(t)
	body := `{"from_crew_id":"` + crewA + `","to_crew_id":"` + crewB + `"}`

	// First create — must succeed.
	req1 := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr1 := httptest.NewRecorder()
	h.Create(rr1, req1)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201; body=%s", rr1.Code, rr1.Body.String())
	}

	// Second create with the same pair — UNIQUE constraint trips.
	req2 := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections", strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr2 := httptest.NewRecorder()
	h.Create(rr2, req2)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409; body=%s", rr2.Code, rr2.Body.String())
	}
}

// ── Delete ──────────────────────────────────────────────────────────────

// MEMBER role can't delete; symmetric with Create's role gate. Without
// this gate a member could break running missions by removing the
// connection underneath them.
func TestCrewConnections_Delete_MemberRole_Returns403(t *testing.T) {
	h, db, userID, wsID, crewA, crewB := crewConnectionsRig(t)
	insertConnection(t, db, "cc-del", wsID, crewA, crewB, "bidirectional")

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/crew-connections/cc-del", nil),
		userID, wsID, "MEMBER",
	)
	req.SetPathValue("connectionId", "cc-del")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// Deleting an unknown connection ID is a 404; a green response here
// would mask client-side state-drift bugs (re-DELETE of an already
// removed connection looking successful).
func TestCrewConnections_Delete_UnknownID_Returns404(t *testing.T) {
	h, _, userID, wsID, _, _ := crewConnectionsRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/crew-connections/cc-nope", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("connectionId", "cc-nope")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// Cross-workspace delete must return 404, NOT 204. The handler scopes
// the DELETE by workspace_id; a missing scope predicate would let any
// authenticated owner of workspace B nuke workspace A's connection.
// This is the most security-sensitive case in the file.
func TestCrewConnections_Delete_CrossWorkspace_Returns404(t *testing.T) {
	h, db, userID, wsID, crewA, crewB := crewConnectionsRig(t)
	insertConnection(t, db, "cc-private", wsID, crewA, crewB, "bidirectional")

	otherWS, _ := seedOtherWorkspaceWithCrew(t, db)

	// Attempt to delete cc-private (owned by wsID) under otherWS's
	// context. Must 404, and the row must still be present after.
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/crew-connections/cc-private", nil),
		userID, otherWS, "OWNER",
	)
	req.SetPathValue("connectionId", "cc-private")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace delete status = %d, want 404", rr.Code)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_connections WHERE id = 'cc-private'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row was deleted across workspaces! count = %d, want 1 (still present)", count)
	}
}

// Happy path: 204 No Content and the row vanishes from the DB. The
// handler chose 204 (no body) over 200; we accept either to keep the
// test resilient to a deliberate future change in success semantics.
func TestCrewConnections_Delete_HappyPath_Returns204(t *testing.T) {
	h, db, userID, wsID, crewA, crewB := crewConnectionsRig(t)
	insertConnection(t, db, "cc-bye", wsID, crewA, crewB, "bidirectional")

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/crew-connections/cc-bye", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("connectionId", "cc-bye")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 204 or 200; body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_connections WHERE id = 'cc-bye'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("row not deleted: count = %d, want 0", count)
	}
}
