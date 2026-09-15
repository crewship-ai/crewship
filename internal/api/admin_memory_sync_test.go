package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/memory"
)

// #1702 — the operator's "run it now" for the two daily memory sweeps.
//
// Until this route the only way to observe the operator-model sweep was to
// be awake at 05:00 UTC (04:00 for peer cards), and everything about its
// failure is silent by design. These tests drive the admin path with a
// fake extractor: the sweep itself is production code from the route down,
// so what is pinned here is the gate, the scoping and the promise that a
// dry run leaves no trace.

// memorySyncFixture seeds two workspaces, each with one crew, one agent and
// one operator whose chat crosses the interaction threshold — so both
// sweeps have exactly one candidate per workspace and a real sweep writes
// one file per workspace.
func memorySyncFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	start := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Format(time.RFC3339)
	seed := []string{
		`INSERT INTO users (id, email, full_name) VALUES ('admin-1', 'admin@example.com', 'Admin')`,
		`INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com'), ('u2', 'u2@example.com')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS One', 'ws-one'), ('ws2', 'WS Two', 'ws-two')`,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm1', 'ws1', 'admin-1', 'OWNER')`,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1', 'ws1', 'Ops', 'ops'), ('cr2', 'ws2', 'Other', 'other')`,
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES
		   ('a1', 'ws1', 'cr1', 'Dev', 'dev', 'AGENT'), ('a2', 'ws2', 'cr2', 'Ops', 'ops', 'AGENT')`,
		`INSERT INTO chats (id, agent_id, workspace_id, created_by, message_count, started_at, ended_at) VALUES
		   ('ch1', 'a1', 'ws1', 'u1', 20, '` + start + `', '` + end + `'),
		   ('ch2', 'a2', 'ws2', 'u2', 20, '` + start + `', '` + end + `')`,
	}
	for _, q := range seed {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
}

type fixedUserModelExtractor struct{ body string }

func (f fixedUserModelExtractor) Extract(context.Context, consolidate.UserModelCandidate, string) (string, error) {
	return f.body, nil
}

type fixedPeerExtractor struct{ body string }

func (f fixedPeerExtractor) Extract(context.Context, consolidate.PeerCandidate) (string, error) {
	return f.body, nil
}

func newMemorySyncTestHandler(t *testing.T) (*AdminMemorySyncHandler, *sql.DB, string) {
	t.Helper()
	db := setupTestDB(t)
	memorySyncFixture(t, db)
	base := t.TempDir()
	h := NewAdminMemorySyncHandler(db, newTestLogger(), base).
		WithUserModelExtractor(fixedUserModelExtractor{body: "- tone: terse"}).
		WithPeerExtractor(fixedPeerExtractor{body: "prefers short answers"})
	return h, db, base
}

// memorySyncReq builds a request in the shape RequireWorkspace + authedMut
// leave it: role, user and workspace on the context.
func memorySyncReq(role, target, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest("POST", target, nil)
	} else {
		r = httptest.NewRequest("POST", target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	ctx := context.WithValue(r.Context(), ctxRole, role)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "admin-1"})
	ctx = context.WithValue(ctx, ctxWorkspaceID, "ws1")
	return r.WithContext(ctx)
}

func decodeMemorySync(t *testing.T, rr *httptest.ResponseRecorder) memorySyncResponse {
	t.Helper()
	var out memorySyncResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return out
}

// The two sweeps, described once so every scenario below runs against both.
type memorySweepCase struct {
	name      string
	path      string
	sweep     string
	serve     func(h *AdminMemorySyncHandler) http.HandlerFunc
	onDisk    func(base, wsID string) string // "" when nothing was written
	indexRows func(t *testing.T, db *sql.DB, wsID string) int
}

func memorySweepCases() []memorySweepCase {
	return []memorySweepCase{
		{
			name:  "user-model",
			path:  "/api/v1/admin/memory/user-model-sync",
			sweep: "user_model",
			serve: func(h *AdminMemorySyncHandler) http.HandlerFunc { return h.UserModelSync },
			onDisk: func(base, wsID string) string {
				crew := map[string]string{"ws1": "cr1", "ws2": "cr2"}[wsID]
				user := map[string]string{"ws1": "u1", "ws2": "u2"}[wsID]
				paths := memory.UserModelPaths{SharedDir: filepath.Join(base, "crews", crew, "shared", ".memory")}
				body, _ := memory.LoadUserModel(paths, user, wsID)
				return body
			},
			indexRows: func(t *testing.T, db *sql.DB, wsID string) int {
				return memorySyncCount(t, db, `SELECT COUNT(*) FROM user_models WHERE workspace_id = ?`, wsID)
			},
		},
		{
			name:  "peer-cards",
			path:  "/api/v1/admin/memory/peer-card-sync",
			sweep: "peer_card",
			serve: func(h *AdminMemorySyncHandler) http.HandlerFunc { return h.PeerCardSync },
			onDisk: func(base, wsID string) string {
				crew := map[string]string{"ws1": "cr1", "ws2": "cr2"}[wsID]
				agent := map[string]string{"ws1": "dev", "ws2": "ops"}[wsID]
				user := map[string]string{"ws1": "u1", "ws2": "u2"}[wsID]
				paths := memory.PeerPaths{AgentDir: filepath.Join(base, "crews", crew, "agents", agent, ".memory")}
				body, _ := memory.LoadPeerCard(paths, user, wsID)
				return body
			},
			indexRows: func(t *testing.T, db *sql.DB, wsID string) int {
				return memorySyncCount(t, db, `SELECT COUNT(*) FROM peer_cards WHERE workspace_id = ?`, wsID)
			},
		},
	}
}

func memorySyncCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// An admin with no body gets what the 05:00 sweep does: every active
// workspace, for real, with the per-workspace summary the worker only ever
// logged.
func TestAdminMemorySync_AdminRunsEveryWorkspaceAndWrites(t *testing.T) {
	for _, tc := range memorySweepCases() {
		t.Run(tc.name, func(t *testing.T) {
			h, db, base := newMemorySyncTestHandler(t)

			rr := httptest.NewRecorder()
			tc.serve(h)(rr, memorySyncReq("ADMIN", tc.path, ""))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			out := decodeMemorySync(t, rr)
			if out.Sweep != tc.sweep {
				t.Errorf("sweep = %q, want %q", out.Sweep, tc.sweep)
			}
			if out.DryRun {
				t.Errorf("a real run reported dry_run=true")
			}
			if len(out.Workspaces) != 2 {
				t.Fatalf("workspaces = %d, want both active ones: %+v", len(out.Workspaces), out.Workspaces)
			}
			for _, ws := range out.Workspaces {
				if ws.Candidates != 1 || ws.Writes != 1 || ws.Errors != 0 || ws.Error != "" {
					t.Errorf("workspace %s: %+v, want 1 candidate / 1 write", ws.WorkspaceID, ws)
				}
				if tc.onDisk(base, ws.WorkspaceID) == "" {
					t.Errorf("workspace %s: nothing on disk after a real run", ws.WorkspaceID)
				}
				if n := tc.indexRows(t, db, ws.WorkspaceID); n != 1 {
					t.Errorf("workspace %s: %d index rows, want 1", ws.WorkspaceID, n)
				}
			}
			if out.Totals.Candidates != 2 || out.Totals.Writes != 2 {
				t.Errorf("totals = %+v, want 2 candidates / 2 writes", out.Totals)
			}
		})
	}
}

// The same status the other /admin/* mutations return to a member — the
// route is registered through authedMut(roleManage), and the handler's own
// check is the belt to that middleware's braces.
func TestAdminMemorySync_NonAdminIsRefused(t *testing.T) {
	for _, tc := range memorySweepCases() {
		for _, role := range []string{"MEMBER", "MANAGER", "VIEWER", ""} {
			t.Run(tc.name+"/"+role, func(t *testing.T) {
				h, db, base := newMemorySyncTestHandler(t)

				rr := httptest.NewRecorder()
				tc.serve(h)(rr, memorySyncReq(role, tc.path, ""))
				if rr.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
				}
				if tc.onDisk(base, "ws1") != "" || tc.indexRows(t, db, "ws1") != 0 {
					t.Errorf("a refused request still ran the sweep")
				}
			})
		}
	}
}

// A dry run reports exactly what a real run would have done and leaves no
// trace: no file, no index row, no audit row. Both spellings — JSON body and
// query string — mean the same thing, because the CLI sends one and a curl
// from the runbook sends the other.
func TestAdminMemorySync_DryRunReportsWithoutWriting(t *testing.T) {
	for _, tc := range memorySweepCases() {
		for _, form := range []struct{ name, target, body string }{
			{"body", tc.path, `{"dry_run": true}`},
			{"query", tc.path + "?dry_run=true", ""},
		} {
			t.Run(tc.name+"/"+form.name, func(t *testing.T) {
				h, db, base := newMemorySyncTestHandler(t)

				rr := httptest.NewRecorder()
				tc.serve(h)(rr, memorySyncReq("OWNER", form.target, form.body))
				if rr.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
				}
				out := decodeMemorySync(t, rr)
				if !out.DryRun {
					t.Errorf("dry_run not echoed: %+v", out)
				}
				if out.Totals.Candidates != 2 || out.Totals.Writes != 2 {
					t.Errorf("totals = %+v, want the 2 writes a real run would make", out.Totals)
				}
				for _, ws := range []string{"ws1", "ws2"} {
					if got := tc.onDisk(base, ws); got != "" {
						t.Errorf("workspace %s: dry run wrote %q", ws, got)
					}
					if n := tc.indexRows(t, db, ws); n != 0 {
						t.Errorf("workspace %s: dry run left %d index rows", ws, n)
					}
				}
				if n := memorySyncCount(t, db, `SELECT COUNT(*) FROM peer_card_audit`); n != 0 {
					t.Errorf("dry run emitted %d audit rows", n)
				}
			})
		}
	}
}

// workspace_id in the body narrows the run to one workspace, by id or slug;
// the others are not touched and not reported. An unknown workspace is a
// 404, not an empty success. And ?workspace_id= is NOT a filter: it is the
// session workspace RequireWorkspace reads and the CLI client injects on
// every request, so its presence must leave the run instance-wide.
func TestAdminMemorySync_WorkspaceFilter(t *testing.T) {
	for _, tc := range memorySweepCases() {
		t.Run(tc.name, func(t *testing.T) {
			h, db, base := newMemorySyncTestHandler(t)

			rr := httptest.NewRecorder()
			tc.serve(h)(rr, memorySyncReq("OWNER", tc.path+"?workspace_id=ws1", `{"workspace_id": "ws-two"}`))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			out := decodeMemorySync(t, rr)
			if len(out.Workspaces) != 1 || out.Workspaces[0].WorkspaceID != "ws2" {
				t.Fatalf("workspaces = %+v, want only ws2", out.Workspaces)
			}
			if out.Workspaces[0].Writes != 1 {
				t.Errorf("ws2 summary = %+v, want 1 write", out.Workspaces[0])
			}
			if tc.onDisk(base, "ws2") == "" {
				t.Errorf("ws2: nothing on disk")
			}
			if tc.onDisk(base, "ws1") != "" || tc.indexRows(t, db, "ws1") != 0 {
				t.Errorf("ws1 was swept although the request named ws2")
			}

			rr = httptest.NewRecorder()
			tc.serve(h)(rr, memorySyncReq("OWNER", tc.path, `{"workspace_id": "nope"}`))
			if rr.Code != http.StatusNotFound {
				t.Errorf("unknown workspace: status = %d, want 404; body=%s", rr.Code, rr.Body.String())
			}

			// The session selector alone: every active workspace, as the
			// scheduled sweep would (a dry run here, so the assertions
			// above about ws1 still hold).
			rr = httptest.NewRecorder()
			tc.serve(h)(rr, memorySyncReq("OWNER", tc.path+"?workspace_id=ws1&dry_run=true", ""))
			if rr.Code != http.StatusOK {
				t.Fatalf("session-only: status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			if out := decodeMemorySync(t, rr); len(out.Workspaces) != 2 {
				t.Errorf("?workspace_id= narrowed the run to %+v; it is the session workspace, not a filter", out.Workspaces)
			}
		})
	}
}

// "Every workspace" means every ACTIVE workspace — the worker's own
// definition, shared with it rather than re-derived here, so a soft-deleted
// tenant is neither swept nor reported.
func TestAdminMemorySync_SoftDeletedWorkspaceIsNotSwept(t *testing.T) {
	h, db, _ := newMemorySyncTestHandler(t)
	if _, err := db.Exec(`UPDATE workspaces SET deleted_at = ? WHERE id = 'ws1'`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("soft-delete ws1: %v", err)
	}
	rr := httptest.NewRecorder()
	h.UserModelSync(rr, memorySyncReq("OWNER", "/api/v1/admin/memory/user-model-sync", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	out := decodeMemorySync(t, rr)
	if len(out.Workspaces) != 1 || out.Workspaces[0].WorkspaceID != "ws2" {
		t.Errorf("workspaces = %+v, want only the active ws2", out.Workspaces)
	}
}

// No storage root means the sweep cannot write anywhere; the worker refuses
// to start in that case, and the route says so instead of running a sweep
// whose every candidate fails.
func TestAdminMemorySync_NoBasePathIs503(t *testing.T) {
	db := setupTestDB(t)
	memorySyncFixture(t, db)
	h := NewAdminMemorySyncHandler(db, newTestLogger(), "")
	rr := httptest.NewRecorder()
	h.PeerCardSync(rr, memorySyncReq("OWNER", "/api/v1/admin/memory/peer-card-sync", ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminMemorySync_MalformedBodyIs400(t *testing.T) {
	h, _, _ := newMemorySyncTestHandler(t)
	rr := httptest.NewRecorder()
	h.UserModelSync(rr, memorySyncReq("OWNER", "/api/v1/admin/memory/user-model-sync", `{"dry_run": "yes"`))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}
