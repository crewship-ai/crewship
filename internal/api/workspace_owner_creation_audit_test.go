package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/journal"
)

type ownerCreationFixture struct {
	db        *sql.DB
	router    *Router
	key       []byte
	token     string
	door      string
	before    func()
	body      string
	published atomic.Int64
}

func newOwnerCreationFixture(t *testing.T, door string) *ownerCreationFixture {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ac", 32))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
	t.Setenv("CREWSHIP_RATELIMIT_DISABLED", "true")
	f := &ownerCreationFixture{db: setupTestDB(t), key: []byte("owner-creation-configured-chain-key"), door: door}
	if _, err := f.db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('owner-audit-calibration','Calibration','owner-audit-calibration')`); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{ChainKey: f.key, FlushInterval: time.Hour})
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "owner-audit-calibration", Type: journal.EntryAuditEntityCreated, ActorType: journal.ActorSystem, Summary: "Configured writer calibration"}); err != nil {
		t.Fatal(err)
	}
	proof, err := journal.VerifyChainWithKey(t.Context(), f.db, "owner-audit-calibration", f.key)
	if err != nil || !proof.OK {
		t.Fatalf("SETUP: configured native journal did not verify: %v", err)
	}
	writer.AddCommitObserver(func(entries []journal.Entry) {
		for _, e := range entries {
			if e.Type == journal.EntryMemberOwnerCreated && e.Payload["action"] == "owner_created" {
				f.published.Add(1)
			}
		}
	})
	const secret = "owner-creation-authenticated-router-secret"
	f.router, err = NewRouter(f.db, secret, newTestLogger(), WithJournal(writer), WithAllowSignup(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.router.Shutdown)
	if door == "create" {
		if _, err := f.db.Exec(`INSERT INTO users(id,email,full_name) VALUES('owner-create-actor','owner-create@example.invalid','Owner creator')`); err != nil {
			t.Fatal(err)
		}
		session, err := sessions.NewDBStore(f.db).Create(t.Context(), "owner-create-actor", "fixture", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		validator, err := auth.NewJWTValidator(secret)
		if err != nil {
			t.Fatal(err)
		}
		f.token, err = validator.IssueAccessToken("owner-create-actor", session.ID, "Owner creator", "owner-create@example.invalid")
		if err != nil {
			t.Fatal(err)
		}
	}
	if door == "bootstrap" {
		armBootstrapForTest(t, f.router.authHandler)
	}
	return f
}

func (f *ownerCreationFixture) mutate() *httptest.ResponseRecorder {
	path := "/api/v1/workspaces"
	body := `{"name":"Owner target","slug":"owner-target"}`
	if f.door != "create" {
		path = "/api/v1/auth/signup"
		if f.door == "bootstrap" {
			path = "/api/v1/bootstrap"
		}
		body = `{"full_name":"Owner target","email":"owner-target@example.invalid","password":"owner-creation-test-password"}`
	}
	if f.body != "" {
		body = f.body
	}
	req := httptest.NewRequest("POST", path, &authorizationAuditBody{reader: strings.NewReader(body), before: f.before})
	req.Header.Set("Content-Type", "application/json")
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func ownerCreationCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{"users", "workspaces", "workspace_members", "cli_tokens", "instance_audit_logs", "user_sessions", "journal_entries"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	return counts
}

func TestOwnerCreationConfiguredJournalCalibration(t *testing.T) {
	f := newOwnerCreationFixture(t, "bootstrap")
	if got := ownerCreationCounts(t, f.db); got["users"] != 0 || got["journal_entries"] != 1 {
		t.Fatalf("SETUP: bootstrap is not fresh or native journal was not stored: %v", got)
	}
}

func TestOwnerCreationCanonicalAudit(t *testing.T) {
	for _, door := range []string{"create", "signup", "bootstrap"} {
		t.Run(door, func(t *testing.T) {
			f := newOwnerCreationFixture(t, door)
			rr := f.mutate()
			want := 201
			if door == "signup" {
				want = 202
			}
			if rr.Code != want {
				t.Fatalf("SETUP: %s mutation returned HTTP %d, want %d", door, rr.Code, want)
			}
			var workspace, member, user, role, mode string
			var capabilities sql.NullString
			var created, updated string
			var revision int64
			if err := f.db.QueryRow(`SELECT w.id,m.id,m.user_id,m.role,m.access_mode,m.capabilities,m.created_at,m.updated_at,m.access_revision FROM workspaces w JOIN workspace_members m ON m.workspace_id=w.id WHERE w.id!='owner-audit-calibration'`).Scan(&workspace, &member, &user, &role, &mode, &capabilities, &created, &updated, &revision); err != nil || role != "OWNER" || mode != "trusted" || capabilities.Valid {
				t.Fatalf("SETUP: actual initial OWNER not committed: role=%s mode=%s caps_present=%v error=%v", role, mode, capabilities.Valid, err)
			}
			t.Logf("CALIBRATED: %s committed initial OWNER via actual router, HTTP %d", door, rr.Code)
			var count int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type='member.owner_created' AND json_extract(payload,'$.action')='owner_created'`, workspace).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("missing canonical owner audit after committed %s: count=%d", door, count)
			}
			var actor, payload string
			if err := f.db.QueryRow(`SELECT actor_id,payload FROM journal_entries WHERE workspace_id=? AND entry_type='member.owner_created'`, workspace).Scan(&actor, &payload); err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal([]byte(payload), &value); err != nil {
				t.Fatal(err)
			}
			next, ok := value["new_membership"].(map[string]any)
			path := map[string]string{"create": "workspace_create", "signup": "signup_personal", "bootstrap": "bootstrap"}[door]
			if !ok || value["old_membership"] != nil || value["creation_path"] != path || actor != user || value["subject_user_id"] != user || value["member_id"] != member || next["id"] != member || next["user_id"] != user || next["role"] != "OWNER" || next["access_mode"] != mode || next["capabilities"] != nil || next["workspace_id"] != workspace || next["access_revision"] != float64(revision) || next["created_at"] != created || next["updated_at"] != updated {
				t.Fatal("initial membership audit does not match the actual stored tuple")
			}
			expectedCaps := FallbackCapabilitiesForRole("OWNER")
			expectedCaps[CapabilityCredentialReveal] = struct{}{}
			wantCaps := capsAsSortedSlice(expectedCaps)
			encoded, err := json.Marshal(wantCaps)
			if err != nil {
				t.Fatal(err)
			}
			var wantJSON any
			if err := json.Unmarshal(encoded, &wantJSON); err != nil || !reflect.DeepEqual(next["effective_capabilities"], wantJSON) {
				t.Fatal("initial OWNER audit lost effective capability fallback")
			}
			proof, err := journal.VerifyChainWithKey(t.Context(), f.db, workspace, f.key)
			if err != nil || !proof.OK || f.published.Load() != 1 {
				t.Fatalf("owner audit key/commit observer mismatch: verified=%v notifications=%d error=%v", proof.OK, f.published.Load(), err)
			}
		})
	}
}

func TestOwnerCreationAuditFailureRollsBack(t *testing.T) {
	for _, door := range []string{"create", "signup", "bootstrap"} {
		for _, fault := range []string{"ABORT", "IGNORE"} {
			t.Run(door+"/"+fault, func(t *testing.T) {
				f := newOwnerCreationFixture(t, door)
				before := ownerCreationCounts(t, f.db)
				statement := "RAISE(IGNORE)"
				if fault == "ABORT" {
					statement = "RAISE(ABORT,'owner audit unavailable')"
				}
				if _, err := f.db.Exec(`CREATE TRIGGER reject_owner_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.owner_created' AND json_extract(NEW.payload,'$.action')='owner_created' BEGIN SELECT ` + statement + `; END`); err != nil {
					t.Fatal(err)
				}
				rr := f.mutate()
				after := ownerCreationCounts(t, f.db)
				if rr.Code != 500 || !reflect.DeepEqual(after, before) || f.published.Load() != 0 {
					t.Fatalf("audit failure did not abort %s/%s: HTTP %d before=%v after=%v notifications=%d", door, fault, rr.Code, before, after, f.published.Load())
				}
			})
		}
	}
}

func TestOwnerCreationFinalWriteFences(t *testing.T) {
	for _, door := range []string{"create", "signup", "bootstrap"} {
		for _, fault := range []string{"membership_ignore", "audit_delete", "owner_undo", "deferred_commit"} {
			t.Run(door+"/"+fault, func(t *testing.T) {
				f := newOwnerCreationFixture(t, door)
				before := ownerCreationCounts(t, f.db)
				var fk int
				if err := f.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
					t.Fatalf("SETUP: foreign keys are not enabled: %d %v", fk, err)
				}
				query := map[string]string{
					"membership_ignore": `CREATE TRIGGER ignore_owner BEFORE INSERT ON workspace_members BEGIN SELECT RAISE(IGNORE); END`,
					"audit_delete":      `CREATE TRIGGER undo_owner_audit AFTER INSERT ON journal_entries WHEN NEW.entry_type='member.owner_created' AND json_extract(NEW.payload,'$.action')='owner_created' BEGIN DELETE FROM journal_entries WHERE id=NEW.id; END`,
					"owner_undo":        `CREATE TRIGGER undo_owner_membership AFTER INSERT ON journal_entries WHEN NEW.entry_type='member.owner_created' AND json_extract(NEW.payload,'$.action')='owner_created' BEGIN UPDATE workspace_members SET role='MEMBER' WHERE id=json_extract(NEW.payload,'$.member_id'); END`,
					"deferred_commit":   `CREATE TABLE owner_commit_parent(id TEXT PRIMARY KEY); CREATE TABLE owner_commit_child(id TEXT REFERENCES owner_commit_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER owner_commit_failure AFTER INSERT ON workspace_members BEGIN INSERT INTO owner_commit_child(id) VALUES('missing-parent'); END`,
				}[fault]
				if _, err := f.db.Exec(query); err != nil {
					t.Fatal(err)
				}
				rr := f.mutate()
				after := ownerCreationCounts(t, f.db)
				if rr.Code != 500 || !reflect.DeepEqual(after, before) || f.published.Load() != 0 {
					t.Fatalf("final write fence did not abort %s/%s: HTTP %d before=%v after=%v notifications=%d", door, fault, rr.Code, before, after, f.published.Load())
				}
				if fault == "deferred_commit" {
					var leaked int
					if err := f.db.QueryRow(`SELECT COUNT(*) FROM owner_commit_child`).Scan(&leaked); err != nil || leaked != 0 {
						t.Fatalf("native deferred commit did not roll back: count=%d error=%v", leaked, err)
					}
				}
			})
		}
	}
}

func TestOwnerCreationRelatedContracts(t *testing.T) {
	t.Run("signup_existing_email", func(t *testing.T) {
		f := newOwnerCreationFixture(t, "signup")
		fresh := f.mutate()
		if fresh.Code != 202 {
			t.Fatalf("fresh signup: HTTP %d", fresh.Code)
		}
		before := ownerCreationCounts(t, f.db)
		existing := f.mutate()
		if existing.Code != fresh.Code || existing.Body.String() != fresh.Body.String() || len(existing.Result().Cookies()) != len(fresh.Result().Cookies()) || !reflect.DeepEqual(before, ownerCreationCounts(t, f.db)) || f.published.Load() != 1 {
			t.Fatal("signup existence became distinguishable or repeated signup changed authority")
		}
	})
	t.Run("signup_presented_invitation", func(t *testing.T) {
		f := newOwnerCreationFixture(t, "signup")
		inviter, workspace := seedInviter(t, f.db)
		seedInvitation(t, f.db, "owner-invite", workspace, inviter, "owner-target@example.invalid", "MANAGER", time.Now().Add(time.Hour), nil)
		f.body = `{"full_name":"Owner target","email":"owner-target@example.invalid","password":"owner-creation-test-password","invitation_token":"tok-owner-invite"}`
		if rr := f.mutate(); rr.Code != 202 {
			t.Fatalf("invited signup: HTTP %d", rr.Code)
		}
		var invitations, owners, roleRows int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='invitation.accepted' AND workspace_id=?`, workspace).Scan(&invitations); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='member.owner_created' AND json_extract(payload,'$.action')='owner_created'`).Scan(&owners); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE u.email='owner-target@example.invalid' AND m.workspace_id=? AND m.role='MANAGER'`, workspace).Scan(&roleRows); err != nil {
			t.Fatal(err)
		}
		proof, err := journal.VerifyChainWithKey(t.Context(), f.db, workspace, f.key)
		if err != nil || !proof.OK || invitations != 1 || owners != 0 || roleRows != 1 || ownedWorkspaces(t, f.db, "owner-target@example.invalid") != 0 || f.published.Load() != 0 {
			t.Fatal("invitation signup lost its existing authority or created a personal owner audit")
		}
	})
	for _, pending := range []bool{false, true} {
		name := "bootstrap_singleton"
		if pending {
			name = "bootstrap_session_pending"
		}
		t.Run(name, func(t *testing.T) {
			f := newOwnerCreationFixture(t, "bootstrap")
			if pending {
				f.router.authHandler.sessions = nil
			}
			rr := f.mutate()
			want := 201
			if pending {
				want = 200
			}
			if rr.Code != want {
				t.Fatalf("bootstrap: HTTP %d, want %d", rr.Code, want)
			}
			if pending {
				var body struct {
					SessionPending bool `json:"session_pending"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || !body.SessionPending {
					t.Fatal("bootstrap lost committed session-pending response")
				}
			}
			var tokens, audits int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens WHERE tier='STANDARD' AND revoked_at IS NULL`).Scan(&tokens); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action='auth.cli_token_created' AND chain_seq IS NOT NULL AND entry_hash IS NOT NULL`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			before := ownerCreationCounts(t, f.db)
			if repeat := f.mutate(); repeat.Code != 410 || tokens != 1 || audits != 1 || f.published.Load() != 1 || !reflect.DeepEqual(before, ownerCreationCounts(t, f.db)) {
				t.Fatal("bootstrap lost singleton, instance token audit or post-commit owner notification")
			}
		})
	}
}

func TestOwnerCreationRechecksCreator(t *testing.T) {
	for _, state := range []string{"suspended", "restricted_elsewhere"} {
		t.Run(state, func(t *testing.T) {
			f := newOwnerCreationFixture(t, "create")
			f.before = func() {
				query := `UPDATE users SET suspended_at=datetime('now') WHERE id='owner-create-actor'`
				if state == "restricted_elsewhere" {
					query = `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('late-guest','owner-audit-calibration','owner-create-actor','MEMBER','restricted')`
				}
				if _, err := f.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			rr := f.mutate()
			var created int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE slug='owner-target'`).Scan(&created); err != nil {
				t.Fatal(err)
			}
			if rr.Code != 403 || created != 0 || f.published.Load() != 0 {
				t.Fatalf("stale creator authority accepted: HTTP %d workspaces=%d notifications=%d", rr.Code, created, f.published.Load())
			}
		})
	}
}
