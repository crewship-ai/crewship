package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Shared rig for the reveal test files. Every reveal test builds its own
// workspace/user IDs rather than reusing seedTestWorkspace's fixed
// "test-workspace-id"/"test-user-id": the capability cache is a
// process-global keyed by (workspace, user) with a 30 s TTL, so two tests
// granting different capabilities to the same pair would silently read each
// other's answer. Distinct ids plus an explicit Invalidate on every write
// keeps the gate under test, not the cache.

// withAuthKind stamps the auth kind RequireAuth would have set. Test-only,
// mirroring withUser / withWorkspace in router_test.go — production code has
// exactly one writer for this key and it is RequireAuth.
func withAuthKind(ctx context.Context, kind string) context.Context {
	return context.WithValue(ctx, ctxAuthKind, kind)
}

// revealTestLogger keeps handler warnings out of the test output. The deny
// paths log at Warn by design (that is the audit trail), and asserting on
// status codes is the contract — the log line is not.
func revealTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// revealSyncEmitter is a journal.SyncEmitter that captures what a handler
// wrote and can be told to fail. It exists so the fail-closed branch (L4)
// is exercised for real rather than assumed: `failWith` makes EmitSync
// return an error exactly the way a wedged DB would.
type revealSyncEmitter struct {
	mu       sync.Mutex
	entries  []journal.Entry
	failWith error
}

func (e *revealSyncEmitter) Emit(_ context.Context, entry journal.Entry) (string, error) {
	return e.record(entry)
}

func (e *revealSyncEmitter) EmitSync(_ context.Context, entry journal.Entry) (string, error) {
	return e.record(entry)
}

func (e *revealSyncEmitter) record(entry journal.Entry) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failWith != nil {
		return "", e.failWith
	}
	if entry.ID == "" {
		entry.ID = "jrn-test-" + entry.Summary
	}
	e.entries = append(e.entries, entry)
	return entry.ID, nil
}

// all returns a copy of the captured entries.
func (e *revealSyncEmitter) all() []journal.Entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]journal.Entry, len(e.entries))
	copy(out, e.entries)
	return out
}

// ofType returns the captured entries of one type.
func (e *revealSyncEmitter) ofType(t journal.EntryType) []journal.Entry {
	var out []journal.Entry
	for _, entry := range e.all() {
		if entry.Type == t {
			out = append(out, entry)
		}
	}
	return out
}

func (e *revealSyncEmitter) Flush(context.Context) error { return nil }

// revealRig is one fully wired reveal handler plus the DB behind it.
type revealRig struct {
	h  *CredentialRevealHandler
	db *sql.DB
	j  *revealSyncEmitter
}

func newRevealRig(t *testing.T) *revealRig {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	j := &revealSyncEmitter{}
	h := NewCredentialRevealHandler(db, revealTestLogger())
	h.SetJournal(j)
	return &revealRig{h: h, db: db, j: j}
}

// seedWorkspace inserts a workspace with the reveal switch in a known
// state. revealEnabled=false is the production default; passing it
// explicitly at every call site keeps the L1 precondition visible in the
// test rather than buried in a helper.
func (r *revealRig) seedWorkspace(t *testing.T, wsID string, revealEnabled bool) {
	t.Helper()
	enabled := 0
	if revealEnabled {
		enabled = 1
	}
	if _, err := r.db.Exec(
		`INSERT INTO workspaces (id, name, slug, credential_reveal_enabled) VALUES (?, ?, ?, ?)`,
		wsID, "WS "+wsID, wsID, enabled); err != nil {
		t.Fatalf("seed workspace %s: %v", wsID, err)
	}
}

// seedMember inserts a user + membership with an explicit capability set.
// caps==nil leaves workspace_members.capabilities NULL, which is the
// role-derived-fallback path — the exact shape a workspace that has never
// used the capability UI has, and therefore the shape T-R2 must deny.
func (r *revealRig) seedMember(t *testing.T, wsID, userID, role string, caps []string) {
	t.Helper()
	if _, err := r.db.Exec(
		`INSERT OR IGNORE INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		userID, userID+"@example.com", userID); err != nil {
		t.Fatalf("seed user %s: %v", userID, err)
	}
	var capsCol any
	if caps != nil {
		set := map[string]struct{}{}
		for _, c := range caps {
			set[c] = struct{}{}
		}
		capsCol = SerializeCapabilities(set)
	}
	if _, err := r.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES (?, ?, ?, ?, ?)`,
		"mem-"+wsID+"-"+userID, wsID, userID, role, capsCol); err != nil {
		t.Fatalf("seed member %s/%s: %v", wsID, userID, err)
	}
	InvalidateCapabilityCache(wsID, userID)
}

// seedCredential inserts an ACTIVE workspace-scoped credential with an
// encrypted value and an explicit classification.
func (r *revealRig) seedCredential(t *testing.T, wsID, ownerID, credID, name, value, sensitivity string) {
	t.Helper()
	enc, err := encryption.Encrypt(value)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := r.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status,
			sensitivity, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'SECRET', 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, ?, datetime('now'), datetime('now'))`,
		credID, wsID, name, enc, sensitivity, ownerID); err != nil {
		t.Fatalf("seed credential %s: %v", credID, err)
	}
}

// scopeToCrew flips a credential to CREW scope and attaches it to crewID,
// creating the crew if needed. Used to build the "outside my crew" case.
func (r *revealRig) scopeToCrew(t *testing.T, wsID, credID, crewID string) {
	t.Helper()
	if _, err := r.db.Exec(
		`INSERT OR IGNORE INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
		crewID, wsID, crewID, crewID); err != nil {
		t.Fatalf("seed crew %s: %v", crewID, err)
	}
	if _, err := r.db.Exec(`UPDATE credentials SET scope = 'CREW' WHERE id = ?`, credID); err != nil {
		t.Fatalf("scope credential: %v", err)
	}
	if _, err := r.db.Exec(
		`INSERT INTO credential_crews (credential_id, crew_id) VALUES (?, ?)`, credID, crewID); err != nil {
		t.Fatalf("credential_crews: %v", err)
	}
}

func (r *revealRig) joinCrew(t *testing.T, crewID, userID string) {
	t.Helper()
	if _, err := r.db.Exec(
		`INSERT INTO crew_members (crew_id, user_id) VALUES (?, ?)`, crewID, userID); err != nil {
		t.Fatalf("crew_members: %v", err)
	}
}

// validRevealReason is a reason that clears the substance check, so tests
// probing a different gate are never accidentally testing L3.3.
const validRevealReason = "Rotating the deploy key by hand for incident INC-4412"

// revealReq builds a POST /credentials/{id}/reveal request carrying an
// interactive-session identity. authKind defaults to AuthKindSession —
// the auth-path tests override it.
func (r *revealRig) revealReq(credID, wsID, userID, role, reason string) *http.Request {
	body, _ := json.Marshal(map[string]string{"reason": reason})
	req := httptest.NewRequest("POST", "/api/v1/credentials/"+credID+"/reveal", strings.NewReader(string(body)))
	ctx := withUser(req.Context(), &AuthUser{ID: userID, Email: userID + "@example.com", SessionID: "sess-" + userID})
	ctx = withWorkspace(ctx, wsID, role)
	ctx = withAuthKind(ctx, AuthKindSession)
	req = req.WithContext(ctx)
	req.SetPathValue("credentialId", credID)
	req.RemoteAddr = "203.0.113.7:52344"
	return req
}

// doReveal runs the handler and returns the recorder.
func (r *revealRig) doReveal(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.h.Reveal(rec, req)
	return rec
}

// revealValue extracts the "value" field from a reveal response body,
// returning "" when absent. Tests assert on this rather than on a substring
// so a value nested anywhere else in the body still counts as leaked (see
// bodyMentions).
func revealValue(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		return ""
	}
	return out.Value
}

// bodyMentions reports whether the raw response body contains needle
// anywhere. Deliberately blunt: a fail-closed path must not emit the secret
// in ANY field, including an error message that helpfully echoes it back.
func bodyMentions(rec *httptest.ResponseRecorder, needle string) bool {
	return strings.Contains(rec.Body.String(), needle)
}
