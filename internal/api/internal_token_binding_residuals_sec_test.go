package api

// PR-F24 hardening, round 2 — residual closure for the workspace-bound
// X-Internal-Token (findings R-1, R-2).
//
// R-1: RecordMCPToolCall reads body.workspace_id and inserts an audit
// row without consulting the token binding — a ws-A bound token could
// write mcp_tool_calls rows attributed to ws-B.
//
// R-2: the cross-crew messaging surface (SendMessage / ListMessages /
// ReadFile / WriteFile) authorizes purely via active crew_connections
// rows and never consults the binding — a captured ws-A bound token
// could read/send messages and read/write shared files for crews of a
// foreign workspace, as long as those crews are connected to each
// other.
//
// Same exploit shape as the F-1…F-6 suite: seed two workspaces, drive
// the handler through the real requireInternal chain with a ws-A-bound
// token, and assert the ws-B row/file is unreachable — while the
// legitimate same-workspace sidecar path keeps working.
//
// The R-1/R-2 matrix is expressed as a single table-driven suite: each
// case carries its own request builder and assertion closure so the
// shared seed/dispatch path lives in one place while the per-scenario
// side-effect checks (no DB row, no file on disk, content not leaked)
// stay explicit.

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// seedCrewPairs adds a second crew to each workspace plus active
// bidirectional connections (A↔A2 in ws-A, B↔B2 in ws-B) so the
// pre-fix canCommunicate gate passes for both tenants.
func seedCrewPairs(t *testing.T, h *InternalHandler, ids scopeIDs) (crewA2, crewB2 string) {
	t.Helper()
	crewA2, crewB2 = "crew_a2", "crew_b2"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := h.db.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("seed exec failed: %v\nquery: %s", err, q)
		}
	}
	for _, c := range []struct{ id, ws, slug string }{
		{crewA2, ids.wsA, "crewa2"}, {crewB2, ids.wsB, "crewb2"},
	} {
		exec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, ?, ?, 'PRE')`,
			c.id, c.ws, c.id, c.slug)
	}
	for _, conn := range []struct{ id, ws, from, to string }{
		{"conn_a", ids.wsA, ids.crewA, crewA2},
		{"conn_b", ids.wsB, ids.crewB, crewB2},
	} {
		exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at, updated_at)
		      VALUES (?, ?, ?, ?, 'bidirectional', 'active', datetime('now'), datetime('now'))`,
			conn.id, conn.ws, conn.from, conn.to)
	}
	return crewA2, crewB2
}

// residualEnv bundles everything a residual case needs to build its
// request, dispatch it, and inspect side effects.
type residualEnv struct {
	h       *InternalHandler
	ids     scopeIDs
	crewA2  string
	crewB2  string
	storage string // crew-messaging file root (empty for non-file cases)
	mh      *CrewMessagingHandler
}

type residualCase struct {
	name string
	// pairs requests the crew-pair + connection seed (R-2 needs it; R-1
	// does not).
	pairs bool
	// files requests a crew-messaging file root (ReadFile / WriteFile).
	files bool
	// seed runs extra per-case fixture setup (seed a ws-B message/file).
	seed func(t *testing.T, env *residualEnv)
	// build returns the handler under test plus the request to drive
	// through requireInternal.
	build func(t *testing.T, env *residualEnv) (http.HandlerFunc, *http.Request)
	// wantCode is the expected HTTP status.
	wantCode int
	// check asserts side effects (no leak, no row, no file) after the
	// response is recorded.
	check func(t *testing.T, env *residualEnv, rr *httptest.ResponseRecorder)
}

// multipartFile builds a multipart body with a requester_crew_id, a path,
// and a single file part, returning the body bytes and the content type.
func multipartFile(requesterCrew, path, filename string, content []byte) ([]byte, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("requester_crew_id", requesterCrew)
	_ = mw.WriteField("path", path)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(content)
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

func residualCases() []residualCase {
	return []residualCase{
		// ---- R-1 MEDIUM — RecordMCPToolCall body-workspace binding ----
		{
			name: "R1_MCPToolCallForeignBody403",
			build: func(t *testing.T, env *residualEnv) (http.HandlerFunc, *http.Request) {
				body, _ := json.Marshal(map[string]string{
					"workspace_id": env.ids.wsB, "agent_id": env.ids.agentB, "crew_id": env.ids.crewB,
					"mcp_server_id": "mcp_x", "tool_name": "exec", "status": "success",
				})
				req := boundReq(http.MethodPost, "/x", body, scopeMaster, env.ids.wsA)
				return env.h.RecordMCPToolCall, req
			},
			wantCode: http.StatusForbidden,
			check: func(t *testing.T, env *residualEnv, _ *httptest.ResponseRecorder) {
				var n int
				if err := env.h.db.QueryRow(`SELECT COUNT(*) FROM mcp_tool_calls WHERE workspace_id = ?`, env.ids.wsB).Scan(&n); err != nil {
					t.Fatalf("count mcp_tool_calls: %v", err)
				}
				if n != 0 {
					t.Fatalf("ws-B mcp_tool_calls row written cross-tenant: got %d rows, want 0", n)
				}
			},
		},
		{
			name: "R1_MCPToolCallOwnWorkspace201",
			build: func(t *testing.T, env *residualEnv) (http.HandlerFunc, *http.Request) {
				body, _ := json.Marshal(map[string]string{
					"workspace_id": env.ids.wsA, "agent_id": env.ids.agentA, "crew_id": env.ids.crewA,
					"mcp_server_id": "mcp_x", "tool_name": "exec", "status": "success",
				})
				req := boundReq(http.MethodPost, "/x", body, scopeMaster, env.ids.wsA)
				return env.h.RecordMCPToolCall, req
			},
			wantCode: http.StatusCreated,
		},

		// ---- R-2 MEDIUM — crew messaging surface binding ----
		{
			name:  "R2_SendMessageForeignCrew403",
			pairs: true,
			build: func(t *testing.T, env *residualEnv) (http.HandlerFunc, *http.Request) {
				body, _ := json.Marshal(map[string]string{
					"from_crew_id": env.ids.crewB, "to_crew_id": env.crewB2,
					"workspace_id": env.ids.wsB, "content": "forged",
				})
				req := boundReq(http.MethodPost, "/x", body, scopeMaster, env.ids.wsA)
				return env.mh.SendMessage, req
			},
			wantCode: http.StatusForbidden,
			check: func(t *testing.T, env *residualEnv, _ *httptest.ResponseRecorder) {
				var n int
				if err := env.h.db.QueryRow(`SELECT COUNT(*) FROM crew_messages WHERE workspace_id = ?`, env.ids.wsB).Scan(&n); err != nil {
					t.Fatalf("count crew_messages: %v", err)
				}
				if n != 0 {
					t.Fatalf("ws-B crew_messages row written cross-tenant: got %d rows, want 0", n)
				}
			},
		},
		{
			name:  "R2_SendMessageSameWorkspaceOK",
			pairs: true,
			build: func(t *testing.T, env *residualEnv) (http.HandlerFunc, *http.Request) {
				body, _ := json.Marshal(map[string]string{
					"from_crew_id": env.ids.crewA, "to_crew_id": env.crewA2,
					"workspace_id": env.ids.wsA, "content": "hello neighbor",
				})
				req := boundReq(http.MethodPost, "/x", body, scopeMaster, env.ids.wsA)
				return env.mh.SendMessage, req
			},
			wantCode: http.StatusCreated,
		},
		{
			name:  "R2_ListMessagesForeignCrew403",
			pairs: true,
			seed: func(t *testing.T, env *residualEnv) {
				if _, err := env.h.db.Exec(`
					INSERT INTO crew_messages (id, workspace_id, from_crew_id, to_crew_id, content, created_at)
					VALUES ('msg_b1', ?, ?, ?, 'ws-b-secret-payload', datetime('now'))`,
					env.ids.wsB, env.ids.crewB, env.crewB2); err != nil {
					t.Fatalf("seed ws-B message: %v", err)
				}
			},
			build: func(t *testing.T, env *residualEnv) (http.HandlerFunc, *http.Request) {
				req := boundReq(http.MethodGet, "/x?crew_id="+env.crewB2, nil, scopeMaster, env.ids.wsA)
				return env.mh.ListMessages, req
			},
			wantCode: http.StatusForbidden,
			check: func(t *testing.T, env *residualEnv, rr *httptest.ResponseRecorder) {
				if bytes.Contains(rr.Body.Bytes(), []byte("ws-b-secret-payload")) {
					t.Fatalf("ws-B message content leaked across tenant boundary: %s", rr.Body.String())
				}
			},
		},
		{
			name:  "R2_ListMessagesSameWorkspaceOK",
			pairs: true,
			seed: func(t *testing.T, env *residualEnv) {
				if _, err := env.h.db.Exec(`
					INSERT INTO crew_messages (id, workspace_id, from_crew_id, to_crew_id, content, created_at)
					VALUES ('msg_a1', ?, ?, ?, 'hello-a2', datetime('now'))`,
					env.ids.wsA, env.ids.crewA, env.crewA2); err != nil {
					t.Fatalf("seed ws-A message: %v", err)
				}
			},
			build: func(t *testing.T, env *residualEnv) (http.HandlerFunc, *http.Request) {
				req := boundReq(http.MethodGet, "/x?crew_id="+env.crewA2, nil, scopeMaster, env.ids.wsA)
				return env.mh.ListMessages, req
			},
			wantCode: http.StatusOK,
			check: func(t *testing.T, env *residualEnv, rr *httptest.ResponseRecorder) {
				if !bytes.Contains(rr.Body.Bytes(), []byte("hello-a2")) {
					t.Fatalf("own-workspace message missing from list: %s", rr.Body.String())
				}
			},
		},
		{
			name:  "R2_ReadFileForeignCrew403",
			pairs: true,
			files: true,
			seed: func(t *testing.T, env *residualEnv) {
				sharedDir := filepath.Join(env.storage, "crews", env.crewB2, "shared")
				if err := os.MkdirAll(sharedDir, 0o755); err != nil {
					t.Fatalf("mkdir shared dir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(sharedDir, "secret.txt"), []byte("ws-b-file-secret"), 0o644); err != nil {
					t.Fatalf("write seed file: %v", err)
				}
			},
			build: func(t *testing.T, env *residualEnv) (http.HandlerFunc, *http.Request) {
				req := setPathValue(
					boundReq(http.MethodGet, "/x?path=secret.txt&requester_crew_id="+env.ids.crewB, nil, scopeMaster, env.ids.wsA),
					"crewId", env.crewB2)
				return env.mh.ReadFile, req
			},
			wantCode: http.StatusForbidden,
			check: func(t *testing.T, env *residualEnv, rr *httptest.ResponseRecorder) {
				if bytes.Contains(rr.Body.Bytes(), []byte("ws-b-file-secret")) {
					t.Fatalf("ws-B file content leaked across tenant boundary: %s", rr.Body.String())
				}
			},
		},
		{
			name:  "R2_WriteFileForeignCrew403",
			pairs: true,
			files: true,
			seed: func(t *testing.T, env *residualEnv) {
				if err := os.MkdirAll(filepath.Join(env.storage, "crews", env.crewB2, "shared"), 0o755); err != nil {
					t.Fatalf("mkdir shared dir: %v", err)
				}
			},
			build: func(t *testing.T, env *residualEnv) (http.HandlerFunc, *http.Request) {
				payload, ct := multipartFile(env.ids.crewB, "payload.txt", "payload.txt", []byte("planted"))
				req := setPathValue(
					boundReq(http.MethodPost, "/x", payload, scopeMaster, env.ids.wsA),
					"crewId", env.crewB2)
				req.Header.Set("Content-Type", ct)
				return env.mh.WriteFile, req
			},
			wantCode: http.StatusForbidden,
			check: func(t *testing.T, env *residualEnv, _ *httptest.ResponseRecorder) {
				planted := filepath.Join(env.storage, "crews", env.crewB2, "shared", "incoming", env.ids.crewB, "payload.txt")
				if _, err := os.Stat(planted); err == nil {
					t.Fatalf("cross-tenant file write landed on disk: %s", planted)
				}
			},
		},
		{
			// Same-workspace shared-file round-trip with a bound token:
			// crew A writes into connected crew A2's incoming dir, then
			// reads it back. The read-back is the assertion.
			name:  "R2_FileSameWorkspaceRoundTripOK",
			pairs: true,
			files: true,
			seed: func(t *testing.T, env *residualEnv) {
				if err := os.MkdirAll(filepath.Join(env.storage, "crews", env.crewA2, "shared"), 0o755); err != nil {
					t.Fatalf("mkdir shared dir: %v", err)
				}
			},
			build: func(t *testing.T, env *residualEnv) (http.HandlerFunc, *http.Request) {
				payload, ct := multipartFile(env.ids.crewA, "report.txt", "report.txt", []byte("quarterly numbers"))
				req := setPathValue(
					boundReq(http.MethodPost, "/x", payload, scopeMaster, env.ids.wsA),
					"crewId", env.crewA2)
				req.Header.Set("Content-Type", ct)
				return env.mh.WriteFile, req
			},
			wantCode: http.StatusCreated,
			check: func(t *testing.T, env *residualEnv, _ *httptest.ResponseRecorder) {
				rr := httptest.NewRecorder()
				req := setPathValue(
					boundReq(http.MethodGet, "/x?path=incoming/"+env.ids.crewA+"/report.txt&requester_crew_id="+env.ids.crewA,
						nil, scopeMaster, env.ids.wsA),
					"crewId", env.crewA2)
				env.h.requireInternal(http.HandlerFunc(env.mh.ReadFile)).ServeHTTP(rr, req)
				if rr.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 for same-workspace read-back; body=%s", rr.Code, rr.Body.String())
				}
				if !bytes.Contains(rr.Body.Bytes(), []byte("quarterly numbers")) {
					t.Fatalf("same-workspace read-back returned wrong content: %s", rr.Body.String())
				}
			},
		},
	}
}

func TestSecBinding_Residuals(t *testing.T) {
	for _, tc := range residualCases() {
		t.Run(tc.name, func(t *testing.T) {
			h, ids := seedScope(t)
			env := &residualEnv{h: h, ids: ids}
			if tc.pairs {
				env.crewA2, env.crewB2 = seedCrewPairs(t, h, ids)
			}
			if tc.files {
				env.storage = t.TempDir()
				env.mh = NewCrewMessagingHandler(h.db, env.storage, testLogger())
			} else if tc.pairs {
				env.mh = NewCrewMessagingHandler(h.db, t.TempDir(), testLogger())
			}
			if tc.seed != nil {
				tc.seed(t, env)
			}

			handler, req := tc.build(t, env)
			rr := httptest.NewRecorder()
			h.requireInternal(handler).ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if tc.check != nil {
				tc.check(t, env, rr)
			}
		})
	}
}
