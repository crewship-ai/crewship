package api

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestPaginationClamp_OverLimitClampsToMax pins the shared list-pagination
// contract from parsePagination (helpers.go): a ?limit above the handler's
// ceiling is clamped DOWN to that ceiling, while missing, zero, negative or
// non-numeric limits fall back to the handler's default. Before this, the
// handlers below parsed ?limit inline and silently reset an over-limit
// request to the default (e.g. ?limit=999 on eval runs returned 50 rows, not
// 200), so a client asking for "as many as allowed" got fewer than allowed.
//
// The four handlers here echo the effective limit in their response, so the
// clamp is observable without seeding hundreds of rows.
func TestPaginationClamp_OverLimitClampsToMax(t *testing.T) {
	type limitCase struct {
		query string
		want  int
	}
	cases := func(def, max int) []limitCase {
		return []limitCase{
			{"", def},
			{"?limit=abc", def},
			{"?limit=0", def},
			{"?limit=-5", def},
			{"?limit=1", 1},
			{"?limit=" + strconv.Itoa(max), max},
			{"?limit=" + strconv.Itoa(max+1), max},
			{"?limit=999999", max},
		}
	}

	t.Run("eval runs default 50 max 200", func(t *testing.T) {
		db := setupTestDB(t)
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)
		h := NewEvalHandler(db, newTestLogger())
		for _, tc := range cases(50, 200) {
			req := httptest.NewRequest("GET", "/api/v1/eval/runs"+tc.query, nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.ListRuns(rr, req)
			assertEchoedLimit(t, "eval "+tc.query, rr, tc.want, func(b []byte) (int, error) {
				var resp struct {
					Limit int `json:"limit"`
				}
				err := json.Unmarshal(b, &resp)
				return resp.Limit, err
			})
		}
	})

	t.Run("paymaster top spenders default 10 max 100", func(t *testing.T) {
		h := newPaymasterTestHandler(t)
		userID := seedTestUser(t, h.db)
		wsID := seedTestWorkspace(t, h.db, userID)
		for _, tc := range cases(10, 100) {
			req := httptest.NewRequest("GET", "/api/v1/paymaster/top-spenders"+tc.query, nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.TopSpenders(rr, req)
			assertEchoedLimit(t, "paymaster "+tc.query, rr, tc.want, func(b []byte) (int, error) {
				var resp struct {
					Limit int `json:"limit"`
				}
				err := json.Unmarshal(b, &resp)
				return resp.Limit, err
			})
		}
	})

	t.Run("runs default 50 max 100", func(t *testing.T) {
		f := newRunsTestFixture(t)
		for _, tc := range cases(50, 100) {
			req := httptest.NewRequest("GET", "/api/v1/runs"+tc.query, nil)
			req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
			rr := httptest.NewRecorder()
			f.h.List(rr, req)
			assertEchoedLimit(t, "runs "+tc.query, rr, tc.want, func(b []byte) (int, error) {
				var resp runListResponse
				err := json.Unmarshal(b, &resp)
				return resp.Pagination.Limit, err
			})
		}
	})

	t.Run("audit default 50 max 100", func(t *testing.T) {
		h, db := newAuditHandler(t)
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)
		seedAuditLog(t, db, wsID, userID, "user.login", "user")
		for _, tc := range cases(50, 100) {
			req := httptest.NewRequest("GET", "/api/v1/audit"+tc.query, nil)
			req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
			rr := httptest.NewRecorder()
			h.List(rr, req)
			assertEchoedLimit(t, "audit "+tc.query, rr, tc.want, func(b []byte) (int, error) {
				var resp auditListResponse
				err := json.Unmarshal(b, &resp)
				return resp.Pagination.Limit, err
			})
		}
	})
}

// TestPaginationClamp_HandlersUseSharedHelper guards the handlers that do not
// echo their effective limit: each must read ?limit through parsePagination
// so the over-limit clamp above applies to them too, instead of a hand-rolled
// strconv.Atoi(r.URL.Query().Get("limit")) that resets to the default.
func TestPaginationClamp_HandlersUseSharedHelper(t *testing.T) {
	targets := []struct{ file, recv, fn string }{
		{"keeper_log.go", "KeeperLogHandler", "List"},
		{"cartographer_handler.go", "CartographerHandler", "List"},
		{"approvals_handler.go", "ApprovalsHandler", "List"},
		{"eval_handler.go", "EvalHandler", "ListRuns"},
		{"inbox_handler.go", "InboxHandler", "List"},
		{"paymaster_handler.go", "PaymasterHandler", "TopSpenders"},
		{"runs.go", "RunHandler", "List"},
		{"audit.go", "AuditHandler", "List"},
		{"agent_persona.go", "PersonaHandler", "GetAgentPersonaHistory"},
	}
	fset := token.NewFileSet()
	for _, tg := range targets {
		file, err := parser.ParseFile(fset, tg.file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", tg.file, err)
		}
		var body *ast.BlockStmt
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != tg.fn || fd.Recv == nil || len(fd.Recv.List) != 1 {
				continue
			}
			if star, ok := fd.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok && id.Name == tg.recv {
					body = fd.Body
				}
			}
		}
		if body == nil {
			t.Errorf("%s: (*%s).%s not found", tg.file, tg.recv, tg.fn)
			continue
		}
		usesHelper, readsLimitInline := false, false
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "parsePagination" {
				usesHelper = true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Get" && len(call.Args) == 1 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Value == `"limit"` {
					readsLimitInline = true
				}
			}
			return true
		})
		if !usesHelper {
			t.Errorf("%s: (*%s).%s does not call parsePagination", tg.file, tg.recv, tg.fn)
		}
		if readsLimitInline {
			t.Errorf("%s: (*%s).%s still reads ?limit inline", tg.file, tg.recv, tg.fn)
		}
	}
}

func assertEchoedLimit(t *testing.T, label string, rr *httptest.ResponseRecorder, want int, decode func([]byte) (int, error)) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Errorf("%s: status = %d, body=%s", label, rr.Code, rr.Body.String())
		return
	}
	got, err := decode(rr.Body.Bytes())
	if err != nil {
		t.Errorf("%s: decode: %v body=%s", label, err, rr.Body.String())
		return
	}
	if got != want {
		t.Errorf("%s: limit = %d, want %d", label, got, want)
	}
}
