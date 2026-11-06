package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func humanLockCtx(req *http.Request, userID, wsID, role string) *http.Request {
	req = withWorkspaceUser(req, userID, wsID, role)
	return req.WithContext(context.WithValue(req.Context(), ctxAuthKind, AuthKindSession))
}

func lockEntries(rec *recordingEmitter, typ journal.EntryType) int {
	n := 0
	for _, e := range rec.entries {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// Only an interactive human with the manage role can lock or unlock.
func TestSkillLockUnlock_Authority(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		mutate   func(*http.Request) *http.Request
		wantCode int
		wantLock bool
	}{
		{"owner session locks", "OWNER", nil, http.StatusOK, true},
		{"admin cli token locks", "ADMIN", func(r *http.Request) *http.Request {
			return r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
		}, http.StatusOK, true},
		{"member refused", "MEMBER", nil, http.StatusForbidden, false},
		{"run token refused", "OWNER", func(r *http.Request) *http.Request {
			return r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, "run"))
		}, http.StatusForbidden, false},
		{"internal sidecar token refused", "OWNER", func(r *http.Request) *http.Request {
			return r.WithContext(context.WithValue(r.Context(), ctxInternalTokenCrew, "crew-1"))
		}, http.StatusForbidden, false},
		{"no auth kind refused", "OWNER", func(r *http.Request) *http.Request {
			return r.WithContext(context.WithValue(r.Context(), ctxAuthKind, ""))
		}, http.StatusForbidden, false},
		{"unknown auth kind refused", "OWNER", func(r *http.Request) *http.Request {
			return r.WithContext(context.WithValue(r.Context(), ctxAuthKind, "agent"))
		}, http.StatusForbidden, false},
	}
	for _, tc := range tests {
		for _, action := range []string{"lock", "unlock"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				db := setupTestDB(t)
				userID := seedTestUser(t, db)
				wsID := seedTestWorkspace(t, db, userID)
				seedSkillForTest(t, db, "sk-l", "lockme", "CODING")
				seedCrewRow(t, db, "crew-a", wsID, "Crew A", "crew-a")
				agent := seedAgentRow(t, db, "agent-a", wsID, "crew-a", "Ava", "ava", "AGENT")
				execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-a', ?, 'sk-l', 1)`, agent)
				if action == "unlock" {
					execOrFatal(t, db, `UPDATE skills SET locked = 1 WHERE id = 'sk-l'`)
				}
				h := NewSkillHandler(db, newTestLogger())
				rec := &recordingEmitter{}
				h.SetJournal(rec)
				req := httptest.NewRequest("POST", "/x", nil)
				req.SetPathValue("skillId", "sk-l")
				req = humanLockCtx(req, userID, wsID, tc.role)
				if tc.mutate != nil {
					req = tc.mutate(req)
				}
				rr := httptest.NewRecorder()
				if action == "lock" {
					h.Lock(rr, req)
				} else {
					h.Unlock(rr, req)
				}
				if rr.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
				}
				var locked int
				_ = db.QueryRow(`SELECT locked FROM skills WHERE id = 'sk-l'`).Scan(&locked)
				wantLocked := 0
				if action == "unlock" {
					wantLocked = 1 // started locked
				}
				if tc.wantLock { // the call was permitted and flips the state
					wantLocked = 1 - wantLocked
				}
				if locked != wantLocked {
					t.Errorf("locked = %d, want %d", locked, wantLocked)
				}
				wantEntry := 0
				if tc.wantLock {
					wantEntry = 1
				}
				if got := len(rec.entries); got != wantEntry {
					t.Errorf("journal entries = %d, want %d", got, wantEntry)
				}
			})
		}
	}
}

func TestSkillLock_ShownInListAndGet(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedSkillForTest(t, db, "sk-l", "lockme", "CODING")
	seedCrewRow(t, db, "crew-a", wsID, "Crew A", "crew-a")
	agent := seedAgentRow(t, db, "agent-a", wsID, "crew-a", "Ava", "ava", "AGENT")
	execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-a', ?, 'sk-l', 1)`, agent)
	h := NewSkillHandler(db, newTestLogger())
	h.SetJournal(&recordingEmitter{})

	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("skillId", "sk-l")
	rr := httptest.NewRecorder()
	h.Lock(rr, humanLockCtx(req, userID, wsID, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("lock: %d %s", rr.Code, rr.Body.String())
	}

	greq := httptest.NewRequest("GET", "/x", nil)
	greq.SetPathValue("skillId", "sk-l")
	grr := httptest.NewRecorder()
	h.Get(grr, withWorkspaceUser(greq, userID, wsID, "OWNER"))
	var detail struct {
		Locked   bool    `json:"locked"`
		LockedAt *string `json:"locked_at"`
	}
	_ = json.Unmarshal(grr.Body.Bytes(), &detail)
	if !detail.Locked || detail.LockedAt == nil {
		t.Fatalf("detail = %s", grr.Body.String())
	}

	lrr := httptest.NewRecorder()
	h.List(lrr, withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "OWNER"))
	var list []struct {
		Locked bool `json:"locked"`
	}
	_ = json.Unmarshal(lrr.Body.Bytes(), &list)
	if len(list) != 1 || !list[0].Locked {
		t.Fatalf("list = %s", lrr.Body.String())
	}
}

const lockedImportMD = "---\nname: lockme\ndescription: Use when testing locks.\ncategory: CODING\n---\n# Rewritten\n\n## Procedure\n1. Do it.\n"

func assertLockedRefusal(t *testing.T, rr *httptest.ResponseRecorder, rec *recordingEmitter) {
	t.Helper()
	if rr.Code != http.StatusLocked {
		t.Fatalf("status = %d, want 423: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "locked") || !strings.Contains(rr.Body.String(), "lockme") {
		t.Errorf("error must name the lock and slug: %s", rr.Body.String())
	}
	if lockEntries(rec, journal.EntrySkillWriteBlocked) != 1 {
		t.Errorf("want one skill.write_blocked journal entry, got %+v", rec.entries)
	}
}

func TestSkillImport_LockedSkillRefused(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedSkillForTest(t, db, "sk-l", "lockme", "CODING")
	execOrFatal(t, db, `UPDATE skills SET locked = 1 WHERE id = 'sk-l'`)
	h := NewSkillHandler(db, newTestLogger())
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	body, _ := json.Marshal(map[string]string{"content": lockedImportMD})
	req := humanLockCtx(httptest.NewRequest("POST", "/x", bytes.NewReader(body)), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	assertLockedRefusal(t, rr, rec)
	var content string
	_ = db.QueryRow(`SELECT content FROM skills WHERE id = 'sk-l'`).Scan(&content)
	if content != "# Test" {
		t.Errorf("content changed: %q", content)
	}
}

func TestSkillProposedApprove_LockedSkillRefused(t *testing.T) {
	h, db, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "lock-approve-crew")
	h.SetStorageBasePath(t.TempDir())
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	// Stage while unlocked, then a curator locks the skill before approval.
	staged := `{"content":` + lockTestJSONString(lockedImportMD) + `}`
	rr := httptest.NewRecorder()
	h.Author(rr, withWorkspaceUser(authorRequestFor(crewID, staged), userID, wsID, "AGENT"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("author: %d %s", rr.Code, rr.Body.String())
	}
	var authored struct {
		FileName string `json:"file_name"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &authored)
	seedSkillForTest(t, db, "sk-l", "lockme", "CODING")
	execOrFatal(t, db, `UPDATE skills SET locked = 1 WHERE id = 'sk-l'`)

	rec.entries = nil
	ab, _ := json.Marshal(map[string]string{"crew_id": crewID, "file_name": authored.FileName})
	arr := httptest.NewRecorder()
	h.Approve(arr, withWorkspaceUser(httptest.NewRequest("POST", "/x", bytes.NewReader(ab)), userID, wsID, "MANAGER"))
	assertLockedRefusal(t, arr, rec)
	var content string
	_ = db.QueryRow(`SELECT content FROM skills WHERE id = 'sk-l'`).Scan(&content)
	if content != "# Test" {
		t.Errorf("content changed: %q", content)
	}
}

// The sidecar author tool cannot even queue a rewrite of a locked skill.
func TestSkillAuthor_LockedSlugRefused(t *testing.T) {
	h, db, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "lock-author-crew")
	h.SetStorageBasePath(t.TempDir())
	rec := &recordingEmitter{}
	h.SetJournal(rec)
	seedSkillForTest(t, db, "sk-l", "lockme", "CODING")
	execOrFatal(t, db, `UPDATE skills SET locked = 1 WHERE id = 'sk-l'`)

	rr := httptest.NewRecorder()
	h.Author(rr, withWorkspaceUser(authorRequestFor(crewID, `{"content":`+lockTestJSONString(lockedImportMD)+`}`), userID, wsID, "AGENT"))
	assertLockedRefusal(t, rr, rec)
}

// Generation (public and sidecar-staged arms share the pre-check) refuses
// before any model call is made.
func TestSkillGenerate_LockedSlugRefused(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedSkillForTest(t, db, "sk-l", "lockme", "CODING")
	execOrFatal(t, db, `UPDATE skills SET locked = 1 WHERE id = 'sk-l'`)
	h := NewSkillGenerateHandler(db, newTestLogger())
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	req := humanLockCtx(httptest.NewRequest("POST", "/x", strings.NewReader(`{"slug":"lockme","prompt":"rewrite it"}`)), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Generate(rr, req)
	assertLockedRefusal(t, rr, rec)
}

func lockTestJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The catalog row is shared by every workspace: a bundled skill or one held
// only by another workspace's agents cannot be locked or unlocked here.
func TestSkillLockUnlock_SharedCatalogOwnership(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		holder   string // "", "own", "other", "both"
		wantCode int
	}{
		{"unassigned custom has no owner", "CUSTOM", "", http.StatusForbidden},
		{"held by own workspace", "CUSTOM", "own", http.StatusOK},
		{"held by another workspace only", "CUSTOM", "other", http.StatusForbidden},
		{"held by both has no single owner", "CUSTOM", "both", http.StatusForbidden},
		{"bundled", "BUNDLED", "own", http.StatusForbidden},
	}
	for _, tc := range tests {
		for _, action := range []string{"lock", "unlock"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				db := setupTestDB(t)
				userID := seedTestUser(t, db)
				wsA := seedTestWorkspace(t, db, userID)
				wsB := "ws-lock-other"
				execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other-lock')`, wsB)
				seedCrewRow(t, db, "crew-a", wsA, "Crew A", "crew-a")
				seedCrewRow(t, db, "crew-b", wsB, "Crew B", "crew-b")
				agA := seedAgentRow(t, db, "agent-a", wsA, "crew-a", "Ava", "ava", "AGENT")
				agB := seedAgentRow(t, db, "agent-b", wsB, "crew-b", "Ben", "ben", "AGENT")
				seedSkillForTest(t, db, "sk-l", "lockme", "CODING")
				execOrFatal(t, db, `UPDATE skills SET source = ? WHERE id = 'sk-l'`, tc.source)
				if tc.holder == "own" || tc.holder == "both" {
					execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-a', ?, 'sk-l', 1)`, agA)
				}
				if tc.holder == "other" || tc.holder == "both" {
					execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-b', ?, 'sk-l', 1)`, agB)
				}
				if action == "unlock" {
					execOrFatal(t, db, `UPDATE skills SET locked = 1 WHERE id = 'sk-l'`)
				}
				h := NewSkillHandler(db, newTestLogger())
				h.SetJournal(&recordingEmitter{})
				req := httptest.NewRequest("POST", "/x", nil)
				req.SetPathValue("skillId", "sk-l")
				req = humanLockCtx(req, userID, wsA, "OWNER")
				rr := httptest.NewRecorder()
				if action == "lock" {
					h.Lock(rr, req)
				} else {
					h.Unlock(rr, req)
				}
				if rr.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
				}
				var locked int
				_ = db.QueryRow(`SELECT locked FROM skills WHERE id = 'sk-l'`).Scan(&locked)
				wantLocked := action == "unlock"
				if tc.wantCode == http.StatusOK {
					wantLocked = action == "lock"
				}
				if (locked == 1) != wantLocked {
					t.Fatalf("locked = %d, want %v", locked, wantLocked)
				}
			})
		}
	}
}

// The workspace that placed a lock is the only one that can lift it, even if
// another workspace later assigns the skill. A holder that no longer exists or
// no longer holds the skill leaves the lock to the sole remaining assignee, so
// assigning, locking and unassigning cannot strand it.
func TestSkillLock_HolderWorkspaceOwnsTheLock(t *testing.T) {
	tests := []struct {
		name     string
		holder   string // locked_workspace_id: "a", "b", "gone" or "soft"
		agents   string // assigned to: "a", "b", "both", or "a-b-deleted" (b's agent soft-deleted)
		action   string
		wantCode int
		wantHeld bool
	}{
		{"holder unlocks", "a", "a", "unlock", http.StatusOK, false},
		{"holder unlocks a skill now shared", "a", "both", "unlock", http.StatusOK, false},
		{"holder unlocks after unassigning", "a", "b", "unlock", http.StatusOK, false},
		{"foreign workspace cannot unlock a holder's lock", "b", "both", "unlock", http.StatusForbidden, true},
		{"foreign workspace cannot relock a holder's lock", "b", "both", "lock", http.StatusForbidden, true},
		{"holder relock is idempotent", "a", "both", "lock", http.StatusOK, true},
		{"holder gave the skill up, sole assignee unlocks", "b", "a", "unlock", http.StatusOK, false},
		{"holder gave the skill up, sole assignee takes the lock", "b", "a", "lock", http.StatusOK, true},
		{"missing holder, sole assignee unlocks", "gone", "a", "unlock", http.StatusOK, false},
		{"soft-deleted holder, sole assignee unlocks", "soft", "a", "unlock", http.StatusOK, false},
		{"missing holder, shared skill stays locked", "gone", "both", "unlock", http.StatusForbidden, true},
		{"deleted agent does not hold for its workspace", "b", "a-b-deleted", "unlock", http.StatusOK, false},
		{"soft-deleted holder with a leftover agent has no claim", "soft", "a-soft", "unlock", http.StatusOK, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsA := seedTestWorkspace(t, db, userID)
			wsB := "ws-lock-other"
			execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other-lock')`, wsB)
			seedCrewRow(t, db, "crew-a", wsA, "Crew A", "crew-a")
			seedCrewRow(t, db, "crew-b", wsB, "Crew B", "crew-b")
			agA := seedAgentRow(t, db, "agent-a", wsA, "crew-a", "Ava", "ava", "AGENT")
			agB := seedAgentRow(t, db, "agent-b", wsB, "crew-b", "Ben", "ben", "AGENT")
			seedSkillForTest(t, db, "sk-l", "lockme", "CODING")
			execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug, deleted_at) VALUES ('ws-soft', 'Soft', 'soft-lock', '2026-11-01T00:00:00Z')`)
			execOrFatal(t, db, `UPDATE skills SET source = 'CUSTOM', locked = 1, locked_workspace_id = ? WHERE id = 'sk-l'`,
				map[string]string{"a": wsA, "b": wsB, "gone": "ws-missing", "soft": "ws-soft"}[tc.holder])
			if tc.agents != "b" {
				execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-a', ?, 'sk-l', 1)`, agA)
			}
			if tc.agents == "a-soft" {
				seedCrewRow(t, db, "crew-s", "ws-soft", "Crew S", "crew-s")
				agS := seedAgentRow(t, db, "agent-s", "ws-soft", "crew-s", "Sam", "sam", "AGENT")
				execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-s', ?, 'sk-l', 1)`, agS)
				execOrFatal(t, db, `DELETE FROM agent_skills WHERE id = 'as-b'`)
			} else if tc.agents != "a" {
				execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-b', ?, 'sk-l', 1)`, agB)
			}
			if tc.agents == "a-b-deleted" {
				execOrFatal(t, db, `UPDATE agents SET deleted_at = '2026-11-01T00:00:00Z' WHERE id = ?`, agB)
			}
			h := NewSkillHandler(db, newTestLogger())
			h.SetJournal(&recordingEmitter{})
			req := httptest.NewRequest("POST", "/x", nil)
			req.SetPathValue("skillId", "sk-l")
			req = humanLockCtx(req, userID, wsA, "OWNER")
			rr := httptest.NewRecorder()
			if tc.action == "lock" {
				h.Lock(rr, req)
			} else {
				h.Unlock(rr, req)
			}
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			var locked int
			_ = db.QueryRow(`SELECT locked FROM skills WHERE id = 'sk-l'`).Scan(&locked)
			if (locked == 1) != tc.wantHeld {
				t.Fatalf("locked = %d, want %v", locked, tc.wantHeld)
			}
			if tc.name == "holder gave the skill up, sole assignee takes the lock" {
				var got string
				_ = db.QueryRow(`SELECT locked_workspace_id FROM skills WHERE id = 'sk-l'`).Scan(&got)
				if got != wsA {
					t.Fatalf("locked_workspace_id = %q, want the new holder %q", got, wsA)
				}
			}
		})
	}
}

// Locking records the workspace, and unlocking clears it.
func TestSkillLock_RecordsTheHolderWorkspace(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsA := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-a", wsA, "Crew A", "crew-a")
	agA := seedAgentRow(t, db, "agent-a", wsA, "crew-a", "Ava", "ava", "AGENT")
	seedSkillForTest(t, db, "sk-l", "lockme", "CODING")
	execOrFatal(t, db, `UPDATE skills SET source = 'CUSTOM' WHERE id = 'sk-l'`)
	execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-a', ?, 'sk-l', 1)`, agA)
	h := NewSkillHandler(db, newTestLogger())
	h.SetJournal(&recordingEmitter{})
	call := func(f http.HandlerFunc) {
		t.Helper()
		req := httptest.NewRequest("POST", "/x", nil)
		req.SetPathValue("skillId", "sk-l")
		rr := httptest.NewRecorder()
		f(rr, humanLockCtx(req, userID, wsA, "OWNER"))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
	}
	holder := func() string {
		t.Helper()
		var got string
		if err := db.QueryRow(`SELECT COALESCE(locked_workspace_id, '') FROM skills WHERE id = 'sk-l'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	call(h.Lock)
	if got := holder(); got != wsA {
		t.Fatalf("locked_workspace_id = %q, want %q", got, wsA)
	}
	call(h.Unlock)
	if got := holder(); got != "" {
		t.Fatalf("locked_workspace_id = %q after unlock, want empty", got)
	}
}

// Delete must honour the lock, otherwise delete plus re-import bypasses it.
func TestSkillDelete_LockedSkillRefused(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedSkillForTest(t, db, "sk-l", "lockme", "CODING")
	execOrFatal(t, db, `UPDATE skills SET locked = 1 WHERE id = 'sk-l'`)
	h := NewSkillHandler(db, newTestLogger())
	rec := &recordingEmitter{}
	h.SetJournal(rec)
	req := httptest.NewRequest("DELETE", "/x", nil)
	req.SetPathValue("skillId", "sk-l")
	req = humanLockCtx(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	assertLockedRefusal(t, rr, rec)
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM skills WHERE id = 'sk-l'`).Scan(&n)
	if n != 1 {
		t.Fatalf("locked skill was deleted")
	}
}
