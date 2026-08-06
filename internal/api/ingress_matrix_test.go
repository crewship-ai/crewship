package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

var ingressPlaceholder = regexp.MustCompile(`\{[a-zA-Z0-9_]+\}`)

type ingressIdentity struct {
	name  string
	user  string
	role  string
	creds []ingressCredential
}

type ingressCredential struct {
	name  string
	token string
}

// TestIngressAuthorizationMatrix exercises the ingress chokepoint, not the
// business handlers. Every recorded mutation route and every workspace-bound
// read route is mounted in a synthetic ServeMux with the exact production
// authentication/workspace/role middleware. The terminal handler is a
// sentinel, so this matrix cannot launch an agent, stream a journal, or leave
// background work behind.
//
// Four identities × three real credential forms are used:
// OWNER, ADMIN, MEMBER, and a user belonging only to another workspace;
// session JWT, unrestricted CLI token, and wildcard-scoped CLI token. A
// foreign identity must stop at RequireWorkspace with 403. In-workspace
// identities may be denied later by the declared role, but must never fail
// authentication with 401.
func TestIngressAuthorizationMatrix(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	wsA := seedTestWorkspace(t, db, owner)
	admin := "ingress-admin"
	member := "ingress-member"
	foreign := "ingress-foreign"
	wsB := "ingress-workspace-b"
	adminToken := seedRoleMemberToken(t, db, wsA, admin, "ADMIN", "ingress-admin")
	memberToken := seedRoleMemberToken(t, db, wsA, member, "MEMBER", "ingress-member")
	mustExec(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, foreign, foreign+"@example.com", "Foreign")
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, wsB, "Ingress B", "ingress-b")
	mustExec(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`, "wm-ingress-b", wsB, foreign)

	const secret = "this-is-a-32-char-test-secret-pad"
	r, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatalf("NewJWTValidator: %v", err)
	}
	store := sessions.NewDBStore(db)

	identities := []ingressIdentity{
		{name: "OWNER", user: owner, role: "OWNER", creds: []ingressCredential{
			{"session", issueIngressSession(t, store, validator, owner, "owner")},
			{"cli-unscoped", mintTokenFor(t, db, owner, "ingress-owner")},
			{"cli-scoped", mintIngressScopedToken(t, db, owner, "owner", "*")},
		}},
		{name: "ADMIN", user: admin, role: "ADMIN", creds: []ingressCredential{
			{"session", issueIngressSession(t, store, validator, admin, "admin")},
			{"cli-unscoped", adminToken},
			{"cli-scoped", mintIngressScopedToken(t, db, admin, "admin", "*")},
		}},
		{name: "MEMBER", user: member, role: "MEMBER", creds: []ingressCredential{
			{"session", issueIngressSession(t, store, validator, member, "member")},
			{"cli-unscoped", memberToken},
			{"cli-scoped", mintIngressScopedToken(t, db, member, "member", "*")},
		}},
		{name: "FOREIGN", user: foreign, role: "OWNER", creds: []ingressCredential{
			{"session", issueIngressSession(t, store, validator, foreign, "foreign")},
			{"cli-unscoped", mintTokenFor(t, db, foreign, "ingress-foreign")},
			{"cli-scoped", mintIngressScopedToken(t, db, foreign, "foreign", "*")},
		}},
	}

	routes := syntheticIngressRoutes(t, r, wsA)
	if len(routes) < 300 {
		t.Fatalf("synthetic ingress matrix enumerated only %d routes; route table coverage likely drifted", len(routes))
	}

	for _, route := range routes {
		route := route
		for _, identity := range identities {
			identity := identity
			for _, credential := range identity.creds {
				credential := credential
				t.Run(identity.name+"/"+credential.name+"/"+route.key, func(t *testing.T) {
					req := httptest.NewRequest(route.method, route.path+"?workspace_id="+wsA, nil)
					req.Header.Set("Authorization", "Bearer "+credential.token)
					rr := httptest.NewRecorder()
					route.handler.ServeHTTP(rr, req)
					if rr.Code == http.StatusUnauthorized {
						t.Fatalf("%s with %s returned 401; minted credential did not authenticate: %s", route.key, credential.name, strings.TrimSpace(rr.Body.String()))
					}
					if identity.name == "FOREIGN" && rr.Code != http.StatusForbidden {
						t.Fatalf("%s with foreign %s returned %d, want RequireWorkspace 403", route.key, credential.name, rr.Code)
					}
				})
			}
		}
	}
}

type syntheticIngressRoute struct {
	key     string
	method  string
	path    string
	handler http.Handler
}

func syntheticIngressRoutes(t *testing.T, r *Router, workspaceID string) []syntheticIngressRoute {
	t.Helper()
	const marker = "INGRESS_SENTINEL"
	sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(marker)) })
	mux := http.NewServeMux()
	routes := make([]syntheticIngressRoute, 0, len(r.mutationRoutes))
	seen := map[string]bool{}
	add := func(method, pattern string, handler http.Handler) {
		key := method + " " + pattern
		if seen[key] {
			return
		}
		seen[key] = true
		path := ingressConcretePath(pattern, workspaceID)
		mux.Handle(method+" "+path, handler)
		routes = append(routes, syntheticIngressRoute{key: key, method: method, path: path, handler: mux})
	}

	for _, route := range r.mutationRoutes {
		h := r.authMw.RequireAuth(r.authMw.RequireWorkspace(
			r.requireRoleScopeMW(route.Role, route.Scope, sentinel),
		))
		add(route.Method, route.Pattern, h)
	}
	for _, route := range scanReadRoutes(t) {
		if route.adminWrapped {
			add(route.verb, route.path, r.authMw.RequireAuth(r.authMw.RequireWorkspace(
				r.requireRoleScopeMW(roleManage, scopeSelf, sentinel),
			)))
			continue
		}
		if strings.Contains(route.tail, "wsCtx(") {
			add(route.verb, route.path, r.authMw.RequireAuth(r.authMw.RequireWorkspace(sentinel)))
		}
	}
	return routes
}

func ingressConcretePath(pattern, workspaceID string) string {
	pattern = strings.ReplaceAll(pattern, "{workspaceId}", workspaceID)
	return ingressPlaceholder.ReplaceAllString(pattern, "ingress-missing")
}

func issueIngressSession(t *testing.T, store sessions.Store, validator *auth.JWTValidator, userID, suffix string) string {
	t.Helper()
	sess, err := store.Create(t.Context(), userID, "ingress-"+suffix, "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create ingress session: %v", err)
	}
	token, err := validator.IssueAccessToken(userID, sess.ID, userID, userID+"@example.com")
	if err != nil {
		t.Fatalf("issue ingress session token: %v", err)
	}
	return token
}

func mintIngressScopedToken(t *testing.T, db *sql.DB, userID, suffix, scope string) string {
	t.Helper()
	plain := "crewship_cli_ingress_scoped_" + suffix
	sum := sha256.Sum256([]byte(plain))
	scopes, _ := json.Marshal([]string{scope})
	_, err := db.Exec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, scopes, created_at) VALUES (?, ?, ?, ?, ?, datetime('now'))`,
		"clt-ingress-scoped-"+suffix, userID, "ingress-scoped-"+suffix, hex.EncodeToString(sum[:]), string(scopes))
	if err != nil {
		t.Fatalf("mint scoped ingress token: %v", err)
	}
	return plain
}
