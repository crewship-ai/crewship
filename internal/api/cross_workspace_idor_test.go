package api

// Finding T0.1 (Tier-0 invariant sweep) from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): a member of workspace A must
// NOT be able to GET / PATCH / DELETE a resource owned by workspace B. The
// resource handlers scope every query by the request-context workspace_id
// (e.g. agents_query.go:213, crews_query.go:100, credentials.go:190), so a
// caller authorized for WS-A who supplies a WS-B-owned id resolves zero rows
// and gets a 404 — never a 200 leak or a cross-tenant mutation.
//
// The audit classifies consistent workspace_id scoping as GENUINELY
// WELL-DEFENDED, so unlike the C1/SC1 tripwires this is a NORMAL passing
// regression guard: it asserts the secure behavior (404) and is expected to
// be GREEN on this branch. It locks the invariant across a parametrized table
// of resource kinds so a future handler that forgets `AND workspace_id = ?`
// turns this test red.
//
// Coverage: agents, crews, credentials are seeded and exercised thoroughly
// across GET/PATCH/DELETE (with a positive control proving the row is real
// and fetchable when correctly scoped). missions/issues/backups/runs are
// documented in a skipped placeholder — see TestCrossWorkspaceIDOR_TODO_OtherKinds.

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// idorSeedTenant creates a user, a workspace, and an OWNER membership so the
// tenant is a fully-formed isolation boundary (FK-clean under foreign_keys=ON).
func idorSeedTenant(t *testing.T, db *sql.DB, wsID, slug, userID, email string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		userID, email, "User "+userID); err != nil {
		t.Fatalf("seed user %s: %v", userID, err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		wsID, "WS "+slug, slug); err != nil {
		t.Fatalf("seed workspace %s: %v", wsID, err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`,
		"m-"+userID, wsID, userID); err != nil {
		t.Fatalf("seed member %s: %v", userID, err)
	}
}

// idorKind is one resource type in the IDOR matrix. seed inserts the resource
// into the given workspace and returns its id; the *Fn closures drive the
// handler method for a single id under whatever context the request carries.
type idorKind struct {
	name      string
	pathParam string
	seed      func(t *testing.T, db *sql.DB, wsID, ownerUserID string) string
	getFn     func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request)
	patchFn   func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request)
	deleteFn  func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request)
}

func idorKinds() []idorKind {
	return []idorKind{
		{
			name:      "agents",
			pathParam: "agentId",
			seed: func(t *testing.T, db *sql.DB, wsID, _ string) string {
				return seedAgentRow(t, db, "ag-victim", wsID, "", "Victim", "victim", "AGENT")
			},
			getFn: func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request) {
				NewAgentHandler(db, logger).Get(rr, req)
			},
			patchFn: func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request) {
				NewAgentHandler(db, logger).Update(rr, req)
			},
			deleteFn: func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request) {
				NewAgentHandler(db, logger).Delete(rr, req)
			},
		},
		{
			name:      "crews",
			pathParam: "crewId",
			seed: func(t *testing.T, db *sql.DB, wsID, _ string) string {
				return seedCrewRow(t, db, "crew-victim", wsID, "Victim", "victim")
			},
			getFn: func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request) {
				NewCrewHandler(db, logger).Get(rr, req)
			},
			patchFn: func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request) {
				NewCrewHandler(db, logger).Update(rr, req)
			},
			deleteFn: func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request) {
				NewCrewHandler(db, logger).Delete(rr, req)
			},
		},
		{
			name:      "credentials",
			pathParam: "credentialId",
			seed: func(t *testing.T, db *sql.DB, wsID, ownerUserID string) string {
				idorSeedCredential(t, db, "cred-victim", wsID, "victim-secret", ownerUserID)
				return "cred-victim"
			},
			getFn: func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request) {
				NewCredentialHandler(db, logger).Get(rr, req)
			},
			patchFn: func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request) {
				NewCredentialHandler(db, logger).Update(rr, req)
			},
			deleteFn: func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request) {
				NewCredentialHandler(db, logger).Delete(rr, req)
			},
		},
	}
}

// idorSeedCredential mirrors seedCredential but lets the caller own the
// created_by FK so the row is valid under whichever tenant owns it.
func idorSeedCredential(t *testing.T, db *sql.DB, id, wsID, name, createdBy string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'enc', 'SECRET', 'NONE', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		id, wsID, name, createdBy)
	if err != nil {
		t.Fatalf("seed credential %s: %v", id, err)
	}
}

const (
	idorWSA    = "ws-attacker"
	idorWSB    = "ws-victim"
	idorUserA  = "user-attacker"
	idorUserB  = "user-victim"
	idorEmailA = "attacker@example.com"
	idorEmailB = "victim@example.com"
)

// idorReq builds an authenticated request: method + path-value + the
// workspace/user/role the caller is authorized for.
func idorReq(method, pathParam, resourceID, wsID, userID, role string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/idor/"+resourceID, nil)
	req.SetPathValue(pathParam, resourceID)
	return withWorkspaceUser(req, userID, wsID, role)
}

// TestCrossWorkspaceIDOR_GetPatchDelete is the T0.1 invariant sweep. For every
// resource kind, a WS-A OWNER (the strongest role A can field) targets a
// WS-B-owned id while supplying their own (authorized) WS-A context. Every
// GET/PATCH/DELETE must 404 — the row is invisible across the tenant boundary,
// so there is no read leak and no mutation. A positive control proves the row
// is genuinely present and fetchable when the request is correctly scoped to
// WS-B, ruling out a false-negative "always 404".
func TestCrossWorkspaceIDOR_GetPatchDelete(t *testing.T) {
	logger := newTestLogger()

	for _, k := range idorKinds() {
		k := k
		t.Run(k.name, func(t *testing.T) {
			// --- positive control: WS-B owner can read the row (proves it exists)
			t.Run("positive_control_get_in_owning_ws", func(t *testing.T) {
				db := setupTestDB(t)
				idorSeedTenant(t, db, idorWSB, "victim", idorUserB, idorEmailB)
				id := k.seed(t, db, idorWSB, idorUserB)

				rr := httptest.NewRecorder()
				req := idorReq("GET", k.pathParam, id, idorWSB, idorUserB, "OWNER")
				k.getFn(db, logger, rr, req)
				if rr.Code != http.StatusOK {
					t.Fatalf("%s GET in owning workspace = %d, want 200 (positive control failed; the negative 404s below would be meaningless). body=%s",
						k.name, rr.Code, rr.Body.String())
				}
			})

			// --- cross-workspace matrix: WS-A caller, WS-B resource id
			methods := []struct {
				method string
				invoke func(db *sql.DB, logger *slog.Logger, rr *httptest.ResponseRecorder, req *http.Request)
			}{
				{"GET", k.getFn},
				{"PATCH", k.patchFn},
				{"DELETE", k.deleteFn},
			}
			for _, m := range methods {
				m := m
				t.Run(m.method, func(t *testing.T) {
					db := setupTestDB(t)
					idorSeedTenant(t, db, idorWSA, "attacker", idorUserA, idorEmailA)
					idorSeedTenant(t, db, idorWSB, "victim", idorUserB, idorEmailB)
					id := k.seed(t, db, idorWSB, idorUserB)

					rr := httptest.NewRecorder()
					req := idorReq(m.method, k.pathParam, id, idorWSA, idorUserA, "OWNER")
					m.invoke(db, logger, rr, req)

					// The one outcome we forbid is a 200 leak/mutation. A 404
					// is the contract; 403 (a stricter pre-scope gate) is also
					// acceptable isolation. Anything 2xx is a cross-tenant breach.
					if rr.Code >= 200 && rr.Code < 300 {
						t.Fatalf("VULN T0.1: WS-A OWNER %s on WS-B %s id %q returned %d (cross-tenant leak/mutation). body=%s",
							m.method, k.name, id, rr.Code, rr.Body.String())
					}
					if rr.Code != http.StatusNotFound && rr.Code != http.StatusForbidden {
						t.Errorf("%s %s cross-workspace = %d, want 404 (or 403); body=%s",
							m.method, k.name, rr.Code, rr.Body.String())
						return
					}
					t.Logf("T0.1 OK: WS-A %s on WS-B %s correctly returned %d", m.method, k.name, rr.Code)

					// For mutations, double-check the WS-B row was not touched.
					if m.method == "DELETE" {
						if deleted := idorRowSoftDeleted(t, db, k.name, id); deleted {
							t.Errorf("VULN T0.1: cross-workspace DELETE soft-deleted WS-B %s row %q", k.name, id)
						}
					}
				})
			}
		})
	}
}

// idorRowSoftDeleted reports whether the resource row carries a deleted_at
// tombstone — used to confirm a rejected cross-workspace DELETE left the
// victim row intact.
func idorRowSoftDeleted(t *testing.T, db *sql.DB, kind, id string) bool {
	t.Helper()
	var table string
	switch kind {
	case "agents":
		table = "agents"
	case "crews":
		table = "crews"
	case "credentials":
		table = "credentials"
	default:
		t.Fatalf("idorRowSoftDeleted: unknown kind %q", kind)
	}
	var deletedAt sql.NullString
	// #nosec G202 -- table is from a fixed in-test whitelist above, not user input.
	if err := db.QueryRow("SELECT deleted_at FROM "+table+" WHERE id = ?", id).Scan(&deletedAt); err != nil {
		t.Fatalf("read %s.deleted_at for %q: %v", table, id, err)
	}
	return deletedAt.Valid
}

// TestCrossWorkspaceIDOR_TODO_OtherKinds documents the resource kinds the T0.1
// sweep does not yet seed (missions/issues, backups, runs). They share the
// same `WHERE ... workspace_id = ?` scoping pattern as the three covered above,
// but seeding a valid missions/issues/backups/runs fixture (NOT-NULL columns,
// journal-backed runs, bundle manifests for backups) is materially heavier than
// the agents/crews/credentials rows. Tracked as a TODO so the matrix can grow.
func TestCrossWorkspaceIDOR_TODO_OtherKinds(t *testing.T) {
	t.Skip("TODO T0.1: extend the IDOR matrix to missions/issues, backups, and runs " +
		"(heavier fixtures: journal-backed runs, bundle manifests). " +
		"agents/crews/credentials are covered in TestCrossWorkspaceIDOR_GetPatchDelete.")
}
