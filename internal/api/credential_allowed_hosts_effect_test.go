package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/provider"
)

// hostsPut drives SetAllowedHosts directly with an explicit auth kind so each
// refusal can be attributed to the rule under test rather than to the router
// rejecting an unauthenticated request.
func hostsPut(t *testing.T, h *CredentialHandler, userID, wsID, kind string, scopes stringSet, internalWS, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/credentials/hb-cred/allowed-hosts", strings.NewReader(body))
	req.SetPathValue("credentialId", "hb-cred")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	ctx := context.WithValue(req.Context(), ctxAuthKind, kind)
	if scopes != nil {
		ctx = context.WithValue(ctx, ctxTokenScopes, scopes)
	}
	if internalWS != "" {
		ctx = context.WithValue(ctx, ctxInternalTokenWS, internalWS)
	}
	rr := httptest.NewRecorder()
	h.SetAllowedHosts(rr, req.WithContext(ctx))
	return rr
}

// A run token is only meaningful as proof of an invocation. Presenting a real,
// correctly signed one must not turn any request into a human operator, and the
// refusal must come from the human-only rule (not from authentication).
func TestSetAllowedHosts_RunTokenIsNotHumanAuthority(t *testing.T) {
	db, wsID, ownerID := seedHostBoundRig(t, "restricted", `["api.github.com"]`, "")
	h := NewCredentialHandler(db, newTestLogger())
	key := internaltoken.DeriveAgentRunKey("master-secret-for-test", wsID, "hb-crew")
	runTok := internaltoken.DeriveAgentRunToken(key, wsID, "hb-agent", "run-1")
	if _, _, _, ok := internaltoken.ValidateAgentRunToken(key, runTok); !ok {
		t.Fatal("test run token does not validate")
	}
	hdr := map[string]string{"X-Crewship-Run-Token": runTok}
	const body = `{"allowed_hosts":["api.github.com"]}`
	const humanOnly = "signed-in person or an unrestricted operator token"

	refused := map[string]struct {
		kind     string
		scopes   stringSet
		internal string
	}{
		"no auth kind (internal-style request)": {kind: ""},
		"internal token":                        {kind: AuthKindSession, internal: wsID},
		"scoped token":                          {kind: AuthKindCLIToken, scopes: stringSet{"credentials:read": {}}},
		"unknown auth kind":                     {kind: "run_token"},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			rr := hostsPut(t, h, ownerID, wsID, tc.kind, tc.scopes, tc.internal, body, hdr)
			if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), humanOnly) {
				t.Fatalf("got %d %s, want the human-only 403", rr.Code, rr.Body.String())
			}
			if got := storedHosts(t, db); got != "[]" {
				t.Fatalf("hosts changed by a refused caller: %s", got)
			}
		})
	}

	// Control: the same header on a real human session is simply ignored.
	rr := hostsPut(t, h, ownerID, wsID, AuthKindSession, nil, "", body, hdr)
	if rr.Code != http.StatusOK {
		t.Fatalf("human session with a run-token header: %d %s", rr.Code, rr.Body.String())
	}
}

func auditRows(t *testing.T, db *sql.DB) []map[string]any {
	t.Helper()
	rows, err := db.Query(`SELECT metadata_json FROM credential_audit WHERE credential_id = 'hb-cred' AND event_type = ? ORDER BY occurred_at, rowid`,
		string(AuditEventAllowedHosts))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestSetAllowedHosts_WritesAuditRows(t *testing.T) {
	db, wsID, ownerID := seedHostBoundRig(t, "restricted", `["api.github.com"]`, "")
	h := NewCredentialHandler(db, newTestLogger())
	put := func(body string) {
		t.Helper()
		if rr := hostsPut(t, h, ownerID, wsID, AuthKindSession, nil, "", body, nil); rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, rr.Code, rr.Body.String())
		}
	}
	put(`{"allowed_hosts":["api.github.com"]}`)
	put(`{"allowed_hosts":["api.github.com"]}`) // no change, no row
	put(`{"allowed_hosts":["api.github.com","uploads.github.com"]}`)
	put(`{"allowed_hosts":[]}`)

	rows := auditRows(t, db)
	want := []struct {
		action   string
		prev, to int
	}{{"set", 0, 1}, {"changed", 1, 2}, {"cleared", 2, 0}}
	if len(rows) != len(want) {
		t.Fatalf("audit rows = %d (%v), want %d", len(rows), rows, len(want))
	}
	for i, w := range want {
		m := rows[i]
		prev, _ := m["previous"].([]any)
		to, _ := m["allowed_hosts"].([]any)
		if m["action"] != w.action || len(prev) != w.prev || len(to) != w.to || m["changed_by"] != ownerID {
			t.Errorf("row %d = %v, want action=%s previous=%d allowed_hosts=%d changed_by=%s", i, m, w.action, w.prev, w.to, ownerID)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'credential.allowed_hosts' AND entity_id = 'hb-cred' AND user_id = ?`, ownerID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("audit_logs rows = %d (%v), want 3", n, err)
	}
}

// Tightening must take effect on running containers: the file copy is removed
// for a crew the credential is no longer deliverable to, and left alone for a
// crew whose boundary still satisfies the new list.
func TestSetAllowedHosts_TighteningRemovesFilesFromRunningCrew(t *testing.T) {
	cases := []struct {
		name, body string
		wantExec   bool
	}{
		{"still covered keeps file", `{"allowed_hosts":["api.github.com"]}`, false},
		{"no longer covered removes file", `{"allowed_hosts":["evil.example.net"]}`, true},
		{"clearing keeps file", `{"allowed_hosts":[]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, wsID, ownerID := seedHostBoundRig(t, "restricted", `["api.github.com"]`, "")
			var calls []provider.ExecConfig
			h := NewCredentialHandler(db, newTestLogger())
			h.SetContainer(newRecordingCtr(&calls, nil))
			if rr := hostsPut(t, h, ownerID, wsID, AuthKindSession, nil, "", tc.body, nil); rr.Code != http.StatusOK {
				t.Fatalf("%d %s", rr.Code, rr.Body.String())
			}
			if got := len(calls) > 0; got != tc.wantExec {
				t.Fatalf("file removal exec = %v (%d calls), want %v", got, len(calls), tc.wantExec)
			}
			if tc.wantExec && !strings.Contains(calls[0].Cmd[2], "rm -f '/secrets/hb-a/GH_TOKEN'") {
				t.Fatalf("script = %q", calls[0].Cmd[2])
			}
		})
	}
}

// The sidecar reaper keeps only what this listing returns, so a host-bound
// credential the crew no longer satisfies must drop out of it (and an
// unaffected credential must not).
func TestListCredentials_HostBoundDroppedForCrewScopedReap(t *testing.T) {
	for _, tc := range hostBoundCases {
		t.Run(tc.name, func(t *testing.T) {
			db, wsID, userID := seedHostBoundRig(t, tc.mode, tc.domains, tc.hosts)
			execOrFatal(t, db, `UPDATE credentials SET type = 'API_KEY', provider = 'ANTHROPIC' WHERE id = 'hb-cred'`)
			seedCredentialEnc(t, db, wsID, userID, "plain-cred", "PLAIN_KEY", "v")
			execOrFatal(t, db, `UPDATE credentials SET type = 'API_KEY', provider = 'ANTHROPIC' WHERE id = 'plain-cred'`)
			h := covCfgHandler(db)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?workspace_id="+wsID+"&crew_id=hb-crew", nil)
			req.RemoteAddr = "127.0.0.1:9999"
			rec := httptest.NewRecorder()
			h.ListCredentials(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d %s", rec.Code, rec.Body.String())
			}
			var out []struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, c := range out {
				got[c.ID] = true
			}
			if !got["plain-cred"] {
				t.Fatalf("unbound credential vanished from the listing: %v", got)
			}
			if got["hb-cred"] != tc.deliver {
				t.Fatalf("hb-cred listed = %v, want %v", got["hb-cred"], tc.deliver)
			}
		})
	}
}

// GET answers under the same visibility as reading the credential itself: a
// member who cannot see a crew-scoped credential must not learn its hosts.
func TestGetAllowedHosts_FollowsCredentialVisibility(t *testing.T) {
	db, wsID, ownerID := seedHostBoundRig(t, "restricted", `["api.github.com"]`, `["api.github.com"]`)
	ownerTok := mintTokenFor(t, db, ownerID, "visowner0000000000000000")
	memberTok := seedRoleMemberToken(t, db, wsID, "vis-member", "MEMBER", "vismember000000000000000")
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/credentials/hb-cred/allowed-hosts"

	if res := hostsReq(t, r, "GET", memberTok, path, ""); res.code != http.StatusOK {
		t.Fatalf("workspace-scoped credential should be readable by a member: %d %s", res.code, res.body)
	}
	execOrFatal(t, db, `UPDATE credentials SET scope = 'CREW' WHERE id = 'hb-cred'`)
	res := hostsReq(t, r, "GET", memberTok, path, "")
	if res.code != http.StatusNotFound || strings.Contains(res.body, "api.github.com") {
		t.Fatalf("member read hosts of a credential it cannot see: %d %s", res.code, res.body)
	}
	if res := hostsReq(t, r, "GET", ownerTok, path, ""); res.code != http.StatusOK {
		t.Fatalf("owner must still read it: %d %s", res.code, res.body)
	}
}
