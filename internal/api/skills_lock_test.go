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
