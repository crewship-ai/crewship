package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Runs the shipped CLI against production signup, credentials/session,
// self-identity and membership handlers. Only the NEXT seed phase is stopped,
// so the regression cannot provision images or contact a model/provider.
func seedExistingUsersAcceptance(t *testing.T, wrongPasswords bool) (*sql.DB, string, map[string]string) {
	t.Helper()
	db := testutil.MigratedDB(t).DB
	for _, q := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('seed-owner','owner@seed.invalid','Owner')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('seed-target','Seed target','seed-target'),('seed-other','Original workspace','seed-other')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('seed-owner-member','seed-target','seed-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	hashes := map[string]string{}
	for i, u := range demoUsers {
		password := u.Password
		if wrongPasswords {
			password = "different-original-password"
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("seed-existing-%d", i)
		hashes[id] = string(hash)
		if _, err = db.Exec(`INSERT INTO users(id,email,full_name,hashed_password) VALUES(?,?,?,?)`, id, u.Email, u.FullName, string(hash)); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?, 'seed-other', ?, 'MEMBER')`, "other-"+id, id); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO user_sessions(id,user_id,created_at,expires_at,last_used_at,user_agent,ip) VALUES(?,?,datetime('now'),datetime('now','+1 day'),datetime('now'),'original-fixture-session','127.0.0.1')`, "original-"+id, id); err != nil {
			t.Fatal(err)
		}
	}
	const token = "crewship_cli_seedrecovery000000000000000000"
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('seed-owner-token','seed-owner','acceptance',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithAllowSignup(false))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/api/v1/workspaces/seed-target/pipeline-webhooks" || r.URL.Path == "/api/v1/workspaces/seed-target/pipeline-schedules") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[]`)
			return
		}
		// This owned fixture has no crews, containers or volumes. Supply only
		// the infra teardown boundary; auth and account placement are real.
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/prune-crew-runtimes" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"removed":[],"count":0}`)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/crews" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTeapot)
			io.WriteString(w, `{"error":"fixture stopped after verified RBAC seed phase"}`)
			return
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: seed-target\ntoken: "+token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return db, cfg, hashes
}

func runExistingUsersSeedCLI(t *testing.T, cfg string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var server string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "server: ") {
			server = strings.TrimPrefix(line, "server: ")
		}
	}
	cmd := exec.CommandContext(ctx, buildCrewshipBinary(t), "seed", "--server", server, "--offline-demo", "--nuke", "--yes", "--with-users", "--skip-issues")
	cmd.Dir = t.TempDir() // No workstation .env.local or setup-token files.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "CREWSHIP_CONFIG=" + cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1"}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("fixture should stop at the next seed phase")
	}
	if ctx.Err() != nil {
		t.Fatalf("seed acceptance timed out: %s", out)
	}
	return string(out)
}

func TestAcceptance_SeedExistingGlobalAccountsRecovery(t *testing.T) {
	db, cfg, hashes := seedExistingUsersAcceptance(t, false)
	for attempt := 0; attempt < 2; attempt++ {
		out := runExistingUsersSeedCLI(t, cfg)
		if !strings.Contains(out, "fixture stopped after verified RBAC seed phase") {
			t.Fatalf("existing accounts must reach the next seed phase, attempt=%d:\n%s", attempt, out)
		}
		membersCLI := exec.Command(buildCrewshipBinary(t), "workspace", "member", "list", "-f", "json")
		membersCLI.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "CREWSHIP_CONFIG=" + cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1"}
		membersJSON, err := membersCLI.Output()
		if err != nil {
			t.Fatalf("CLI workspace member list: %v", err)
		}
		var members []workspaceMemberRow
		if err := json.Unmarshal(membersJSON, &members); err != nil {
			t.Fatal(err)
		}
		roles := map[string]string{}
		for _, member := range members {
			roles[member.UserID] = member.Role
		}
		for i, user := range demoUsers {
			id := fmt.Sprintf("seed-existing-%d", i)
			if roles[id] != user.Role {
				t.Fatalf("CLI roster did not show fixture ID and role for %s", id)
			}
			var gotID, hash, role string
			if err := db.QueryRow(`SELECT u.id,u.hashed_password,wm.role FROM users u JOIN workspace_members wm ON wm.user_id=u.id WHERE wm.workspace_id='seed-target' AND u.email=?`, user.Email).Scan(&gotID, &hash, &role); err != nil {
				t.Fatalf("existing account was not placed: %v", err)
			}
			if gotID != id || hash != hashes[id] || role != user.Role {
				t.Fatalf("seed changed account identity/password or role for %s", id)
			}
			var otherRole string
			if err := db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id='seed-other' AND user_id=?`, id).Scan(&otherRole); err != nil || otherRole != "MEMBER" {
				t.Fatalf("seed altered other workspace membership: %v", err)
			}
		}
		var active int
		if err := db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id LIKE 'seed-existing-%' AND user_agent != 'original-fixture-session' AND revoked_at IS NULL`).Scan(&active); err != nil || active != 0 {
			t.Fatalf("transient fixture sessions were not closed: %d %v", active, err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_agent='original-fixture-session' AND revoked_at IS NULL`).Scan(&active); err != nil || active != len(demoUsers) {
			t.Fatalf("seed revoked pre-existing user sessions: %d %v", active, err)
		}
	}
}

func TestAcceptance_SeedExistingAccountsWrongPasswordCannotPlace(t *testing.T) {
	db, cfg, hashes := seedExistingUsersAcceptance(t, true)
	out := runExistingUsersSeedCLI(t, cfg)
	if !strings.Contains(out, "incomplete RBAC fixture") || strings.Contains(out, "fixture stopped after verified RBAC seed phase") {
		t.Fatalf("wrong credentials should refuse placement:\n%s", out)
	}
	var members int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id='seed-target' AND user_id!='seed-owner'`).Scan(&members); err != nil || members != 0 {
		t.Fatalf("wrong credentials placed accounts: %d %v", members, err)
	}
	for id, original := range hashes {
		var hash string
		if err := db.QueryRow(`SELECT hashed_password FROM users WHERE id=?`, id).Scan(&hash); err != nil || hash != original {
			t.Fatalf("seed reset existing password: %v", err)
		}
	}
}

func TestSeedTransientSessionCleanupAfterCancellation(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	if _, err := db.Exec(`INSERT INTO users(id,email,full_name) VALUES('cleanup-user','cleanup@seed.invalid','Cleanup')`); err != nil {
		t.Fatal(err)
	}
	store := sessions.NewDBStore(db)
	original, err := store.Create(t.Context(), "cleanup-user", "original", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	transient, err := store.Create(t.Context(), "cleanup-user", "transient", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator("this-is-a-32-char-test-secret-pad")
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken("cleanup-user", transient.ID, "Cleanup", "cleanup@seed.invalid")
	if err != nil {
		t.Fatal(err)
	}
	handler := api.NewNextAuthHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), validator, store)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/signout", handler.SignOut)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	closeTransientSeedSession(ctx, cli.NewClient(srv.URL, token, "").WithContext(ctx), token)
	for _, expected := range []struct {
		id      string
		revoked bool
	}{{transient.ID, true}, {original.ID, false}} {
		session, err := store.Get(t.Context(), expected.id)
		if err != nil || (session.RevokedAt != nil) != expected.revoked {
			t.Fatalf("cleanup must revoke only its transient session despite cancellation: %v", err)
		}
	}
}
