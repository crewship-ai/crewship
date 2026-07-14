package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// covICSeedCredScoped inserts a credential with an explicit scope, letting
// tests exercise the WORKSPACE-vs-CREW visibility semantics (#1031) that
// covICSeedAICred's hardcoded scope='WORKSPACE' can't.
func covICSeedCredScoped(t *testing.T, db *sql.DB, wsID, userID, credID, scope string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value,
			type, provider, scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'enc-placeholder', 'AI_CLI_TOKEN', 'ANTHROPIC', ?, 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		credID, wsID, "ai-"+credID, scope, userID); err != nil {
		t.Fatalf("seed scoped credential: %v", err)
	}
}

// TestListCredentials_CrewScope covers #1031's actual leak plus the
// functional break the naive fix introduced: the internal credential
// metadata listing must be filterable to the calling crew — hiding a
// DIFFERENT crew's CREW-scoped credential — WITHOUT hiding a scope=WORKSPACE
// credential that has no agent_credentials/credential_crews row yet. That
// last case matters concretely: an agent's sidecar self-service credential
// create defaults to scope=WORKSPACE with no crew_ids, so the very next
// crew-scoped listing must still contain it (create 201 → immediately
// invisible would be a regression, not a fix).
func TestListCredentials_CrewScope(t *testing.T) {
	h, db, userID, wsID := covICRig(t)

	// Two crews, one agent each.
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-a', ?, 'A', 'crew-a')`, wsID)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-b', ?, 'B', 'crew-b')`, wsID)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('ag-a', ?, 'crew-a', 'AgA', 'ag-a')`, wsID)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('ag-b', ?, 'crew-b', 'AgB', 'ag-b')`, wsID)

	// credWorkspace: scope=WORKSPACE, no agent_credentials / credential_crews
	// row at all — the self-service-create-then-list case (a).
	covICSeedCredScoped(t, db, wsID, userID, "credWorkspace", "WORKSPACE")

	// credCrewA: scope=CREW, scoped to crew-a via credential_crews — crew-a's
	// own CREW-scoped credential, must stay visible (c).
	covICSeedCredScoped(t, db, wsID, userID, "credCrewA", "CREW")
	mustExec(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('credCrewA', 'crew-a')`)

	// credCrewB: scope=CREW, assigned to crew-b's agent via agent_credentials —
	// another crew's CREW-scoped credential, the actual #1031 leak (b).
	covICSeedCredScoped(t, db, wsID, userID, "credCrewB", "CREW")
	mustExec(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at) VALUES ('ac-b', 'ag-b', 'credCrewB', 'B', 0, datetime('now'))`)

	ids := func(q string) map[string]bool {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?"+q, nil)
		req.RemoteAddr = "127.0.0.1:9999" // loopback: irrelevant to the crew filter, keeps the no-crew_id case from tripping the bypass warning
		rec := httptest.NewRecorder()
		h.ListCredentials(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		var out []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := map[string]bool{}
		for _, c := range out {
			got[c.ID] = true
		}
		return got
	}

	t.Run("crew-scoped listing includes workspace-scoped credential with no assignment yet", func(t *testing.T) {
		got := ids("workspace_id=" + wsID + "&crew_id=crew-a")
		if !got["credWorkspace"] {
			t.Errorf("crew-a should see the just-created workspace-scoped credential, got %v", got)
		}
	})

	t.Run("crew-scoped listing includes own crew's CREW-scoped credential", func(t *testing.T) {
		got := ids("workspace_id=" + wsID + "&crew_id=crew-a")
		if !got["credCrewA"] {
			t.Errorf("crew-a should see its own crew-scoped credential, got %v", got)
		}
	})

	t.Run("crew-scoped listing hides another crew's CREW-scoped credential", func(t *testing.T) {
		got := ids("workspace_id=" + wsID + "&crew_id=crew-a")
		if got["credCrewB"] {
			t.Errorf("crew-a must NOT see crew-b's crew-scoped credential, got %v", got)
		}
	})

	// No crew_id → unchanged workspace-wide behaviour (TokenSyncer / legacy).
	t.Run("no crew_id returns all (backward compatible)", func(t *testing.T) {
		got := ids("workspace_id=" + wsID)
		if !got["credWorkspace"] || !got["credCrewA"] || !got["credCrewB"] {
			t.Errorf("workspace-wide should see all three, got %v", got)
		}
	})
}

// TestListCredentials_CrewBoundContextScopes covers #1159: when the request
// carries a crew-bound internal token, the middleware puts the
// cryptographically-bound crew in context, and ListCredentials scopes to THAT
// crew — not the forgeable ?crew_id query. This closes the fail-open hole
// where a workspace-bound-token holder could omit crew_id (workspace-wide) or
// forge a sibling crew's id to enumerate every crew's credential metadata.
func TestListCredentials_CrewBoundContextScopes(t *testing.T) {
	h, db, userID, wsID := covICRig(t)

	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-a', ?, 'A', 'crew-a')`, wsID)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-b', ?, 'B', 'crew-b')`, wsID)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('ag-b', ?, 'crew-b', 'AgB', 'ag-b')`, wsID)

	covICSeedCredScoped(t, db, wsID, userID, "credWorkspace", "WORKSPACE")
	covICSeedCredScoped(t, db, wsID, userID, "credCrewA", "CREW")
	mustExec(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('credCrewA', 'crew-a')`)
	covICSeedCredScoped(t, db, wsID, userID, "credCrewB", "CREW")
	mustExec(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at) VALUES ('ac-b', 'ag-b', 'credCrewB', 'B', 0, datetime('now'))`)

	// ctxCrew is the crew a crew-bound token would resolve to. The query is
	// what the (possibly malicious) caller supplies.
	idsWithCtx := func(ctxCrew, query string) map[string]bool {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?workspace_id="+wsID+query, nil)
		req.RemoteAddr = "203.0.113.7:5555" // non-loopback: a real crew sidecar
		if ctxCrew != "" {
			req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenCrew, ctxCrew))
		}
		rec := httptest.NewRecorder()
		h.ListCredentials(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		var out []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := map[string]bool{}
		for _, c := range out {
			got[c.ID] = true
		}
		return got
	}

	t.Run("crew-bound context scopes even when query omits crew_id", func(t *testing.T) {
		got := idsWithCtx("crew-a", "")
		if !got["credWorkspace"] || !got["credCrewA"] {
			t.Errorf("crew-a should see workspace + own crew creds, got %v", got)
		}
		if got["credCrewB"] {
			t.Errorf("crew-a must NOT enumerate crew-b's credential via an omitted crew_id, got %v", got)
		}
	})

	t.Run("crew-bound context overrides a forged query crew_id", func(t *testing.T) {
		// Even if a forged ?crew_id=crew-b slipped past the middleware, the
		// context crew is authoritative — the listing stays scoped to crew-a.
		got := idsWithCtx("crew-a", "&crew_id=crew-b")
		if got["credCrewB"] {
			t.Errorf("forged query crew_id must not widen the listing; got %v", got)
		}
		if !got["credCrewA"] {
			t.Errorf("crew-a should still see its own crew creds, got %v", got)
		}
	})
}

// TestListCredentials_NonLoopbackWithoutCrewID_Warns covers the #1031
// hardening: the crew scoping is opt-in and fail-open — a caller that omits
// crew_id still gets the full workspace-wide listing (no crew-bound internal
// tokens exist yet to enforce it). A legitimate non-loopback caller (the
// sidecar) always attaches its own bound crew_id, so a non-loopback call
// WITHOUT one is either a stale/misconfigured sidecar or a bypass attempt —
// either way it must be visible in ops via a WARN log. A loopback caller
// (the in-process TokenSyncer, which is legitimately crew-less) must NOT
// trip the warning.
func TestListCredentials_NonLoopbackWithoutCrewID_Warns(t *testing.T) {
	h, db, userID, wsID := covICRig(t)
	var buf bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	covICSeedAICred(t, db, wsID, userID, "cred1", "enc-placeholder", nil)

	const warnSubstr = "workspace-wide listing (no crew_id) from non-loopback caller"

	t.Run("non-loopback without crew_id warns", func(t *testing.T) {
		buf.Reset()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?workspace_id="+wsID, nil)
		req.RemoteAddr = "203.0.113.7:54321" // public, non-loopback
		rec := httptest.NewRecorder()
		h.ListCredentials(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(buf.String(), warnSubstr) {
			t.Errorf("expected bypass-visibility WARN log, got: %s", buf.String())
		}
	})

	t.Run("loopback without crew_id does not warn", func(t *testing.T) {
		buf.Reset()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?workspace_id="+wsID, nil)
		req.RemoteAddr = "127.0.0.1:54321"
		rec := httptest.NewRecorder()
		h.ListCredentials(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(buf.String(), warnSubstr) {
			t.Errorf("loopback caller without crew_id should not warn, got: %s", buf.String())
		}
	})

	t.Run("non-loopback with crew_id does not warn", func(t *testing.T) {
		buf.Reset()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?workspace_id="+wsID+"&crew_id=crew-x", nil)
		req.RemoteAddr = "203.0.113.7:54321"
		rec := httptest.NewRecorder()
		h.ListCredentials(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(buf.String(), warnSubstr) {
			t.Errorf("caller that supplied crew_id should not warn, got: %s", buf.String())
		}
	})
}
