package api

// The answer to a credential ask is a grant, not a value (#2376).
//
// Every test here is a guard on one sink the value could reach. The one that
// matters most has no assertion of its own: the agent-facing wait body is
// checked for the ABSENCE of a resolution key, because the value is never on
// that path at all — not scrubbed, not redacted, absent.

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
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// supplyFixture is covEscFixture plus the two capture points the sink tests
// need: a journal that records what it was handed and a logger that keeps
// everything at DEBUG, so "the value is not in the logs" is checked against
// the most talkative configuration rather than the quietest.
type supplyFixture struct {
	h       *QueryHandler
	userID  string
	wsID    string
	crewID  string
	agentID string
	journal *recordingEmitter
	logs    *bytes.Buffer
}

func newSupplyFixture(t *testing.T) supplyFixture {
	t.Helper()
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "sup-crew", wsID, "Crew", "sup-crew")
	agentID := seedAgentRow(t, db, "sup-agent", wsID, crewID, "Agent", "sup-agent", "AGENT")
	seedChatRow(t, db, "sup-chat", agentID, wsID)
	logs := &bytes.Buffer{}
	h := NewQueryHandler(db, nil, nil, "", slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	rec := &recordingEmitter{}
	h.SetJournal(rec)
	return supplyFixture{h: h, userID: userID, wsID: wsID, crewID: crewID, agentID: agentID, journal: rec, logs: logs}
}

func seedChatRow(t *testing.T, db *sql.DB, chatID, agentID, wsID string) {
	t.Helper()
	execOrFatal(t, db, `INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?,?,?,'CHAT','ACTIVE')`,
		chatID, agentID, wsID)
}

// ask raises a CREDENTIAL escalation with no value — the agent's side of the
// flow — and returns the escalation and staged credential ids.
func (f supplyFixture) ask(t *testing.T, metadata string) (escID, credID string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/escalations", jsonBody(map[string]string{
		"from_slug": "sup-agent", "reason": "need the postgres password for db.internal", "crew_id": f.crewID,
		"workspace_id": f.wsID, "chat_id": "sup-chat", "type": "CREDENTIAL", "metadata": metadata,
	}))
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, f.wsID))
	rr := httptest.NewRecorder()
	f.h.CreateEscalation(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("ask: status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		EscalationID string `json:"escalation_id"`
		CredentialID string `json:"credential_id"`
		Requested    bool   `json:"credential_requested"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return created.EscalationID, created.CredentialID
}

func (f supplyFixture) supply(t *testing.T, escID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/", jsonBody(body))
	req.SetPathValue("escalationId", escID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: f.userID}), f.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	f.h.SupplyEscalationCredential(rr, req)
	return rr
}

func (f supplyFixture) resolve(t *testing.T, escID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/", jsonBody(body))
	req.SetPathValue("escalationId", escID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: f.userID}), f.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	f.h.ResolveEscalation(rr, req)
	return rr
}

const askMetadata = `{"name":"PG_PASSWORD","type":"SECRET","security_level":"3","purpose":"read the orders table for the weekly report","hosts":["DB.internal "," ","db-replica.internal"]}`

// --- parsing -----------------------------------------------------------------

func TestParseCredentialProposal_Ask(t *testing.T) {
	p, ok := parseCredentialProposal(askMetadata)
	if !ok {
		t.Fatal("an ask (no value) must parse as a proposal")
	}
	if !p.IsAsk() {
		t.Error("IsAsk() = false for a proposal without a value")
	}
	if p.SecurityLevel != 3 {
		t.Errorf("security_level = %d, want 3 (numeric string accepted)", p.SecurityLevel)
	}
	if got := strings.Join(p.Hosts, ","); got != "db.internal,db-replica.internal" {
		t.Errorf("hosts = %q, want normalised and blanks dropped", got)
	}
	if p.Purpose != "read the orders table for the weekly report" {
		t.Errorf("purpose = %q", p.Purpose)
	}
	if !strings.Contains(p.redactedMetadata("c1"), `"requested":true`) {
		t.Errorf("redacted metadata must say it is an ask: %s", p.redactedMetadata("c1"))
	}
	if _, ok := parseCredentialProposal(`{"type":"SECRET"}`); ok {
		t.Error("a proposal without a name must not parse")
	}
	if p, _ := parseCredentialProposal(`{"name":"X","security_level":"9"}`); p.SecurityLevel != 0 {
		t.Errorf("out-of-range security_level = %d, want 0 (unset)", p.SecurityLevel)
	}
}

// --- the ask stages a REQUESTED row -------------------------------------------

func TestCreateEscalation_CredentialAsk_StagesRequestedRow(t *testing.T) {
	f := newSupplyFixture(t)
	escID, credID := f.ask(t, askMetadata)
	if credID == "" {
		t.Fatal("ask must stage a credential and return its id")
	}

	var status, desc, enc string
	var handleOnly, level int
	if err := f.h.db.QueryRow(`SELECT status, description, encrypted_value, handle_only, security_level
		FROM credentials WHERE id = ?`, credID).Scan(&status, &desc, &enc, &handleOnly, &level); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if status != credentialStatusRequested {
		t.Errorf("status = %q, want REQUESTED", status)
	}
	if handleOnly != 1 {
		t.Error("an asked-for credential must be handle_only")
	}
	if level != 3 {
		t.Errorf("security_level = %d, want the agent's proposal 3", level)
	}
	if desc != "read the orders table for the weekly report" {
		t.Errorf("description = %q, want the purpose", desc)
	}
	if dec, err := encryption.Decrypt(enc); err != nil || dec != pendingSentinelRequested {
		t.Errorf("a REQUESTED row must hold the requested sentinel, got (%q, %v)", dec, err)
	}

	var linked sql.NullString
	if err := f.h.db.QueryRow(`SELECT credential_id FROM escalations WHERE id = ?`, escID).Scan(&linked); err != nil {
		t.Fatalf("read escalation: %v", err)
	}
	if linked.String != credID {
		t.Errorf("escalation.credential_id = %q, want %q", linked.String, credID)
	}

	var title, payload string
	if err := f.h.db.QueryRow(`SELECT title, payload_json FROM inbox_items WHERE source_id = ?`, escID).Scan(&title, &payload); err != nil {
		t.Fatalf("read inbox item: %v", err)
	}
	if title != "Credential requested: PG_PASSWORD" {
		t.Errorf("inbox title = %q", title)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(payload), &p)
	if p["needs_credential_value"] != true {
		t.Errorf("inbox payload must flag needs_credential_value: %s", payload)
	}
	if p["has_pending_credential"] == true {
		t.Error("an ask is not a one-click approval; has_pending_credential must not be set")
	}
	if hosts, _ := p["credential_hosts"].([]any); len(hosts) != 2 {
		t.Errorf("credential_hosts = %v, want the two declared hosts", p["credential_hosts"])
	}

	// A second agent asking for the same name while the first ask is open is a
	// conflict — never two REQUESTED rows racing one name.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", jsonBody(map[string]string{
		"from_slug": "sup-agent", "reason": "again", "crew_id": f.crewID, "workspace_id": f.wsID,
		"chat_id": "sup-chat", "type": "CREDENTIAL", "metadata": askMetadata,
	}))
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, f.wsID))
	f.h.CreateEscalation(rr, req)
	var n int
	_ = f.h.db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND name = 'PG_PASSWORD'`, f.wsID).Scan(&n)
	if n != 1 {
		t.Errorf("credentials named PG_PASSWORD = %d, want 1", n)
	}
}

// --- /resolve takes no text on a CREDENTIAL escalation -------------------------

func TestResolveEscalation_Credential_RefusesText(t *testing.T) {
	f := newSupplyFixture(t)
	escID, credID := f.ask(t, askMetadata)
	const canary = "hunter2-do-not-store-me" //gitleaks:allow — test fixture asserting the value is refused

	for _, action := range []string{"approve", "reject"} {
		rr := f.resolve(t, escID, map[string]any{"action": action, "resolution": canary})
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s with text: status = %d, want 400; body=%s", action, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "supply") {
			t.Errorf("%s refusal must name the supply endpoint: %s", action, rr.Body.String())
		}
	}
	var status string
	_ = f.h.db.QueryRow(`SELECT status FROM escalations WHERE id = ?`, escID).Scan(&status)
	if status != escalationStatusPending {
		t.Errorf("escalation status = %q after refused resolves, want PENDING", status)
	}
	_ = f.h.db.QueryRow(`SELECT status FROM credentials WHERE id = ?`, credID).Scan(&status)
	if status != credentialStatusRequested {
		t.Errorf("credential status = %q, want still REQUESTED", status)
	}
	assertValueNowhere(t, f, canary)

	// Approving an ask without a value is not a decision either.
	rr := f.resolve(t, escID, map[string]any{"action": "approve"})
	if rr.Code != http.StatusConflict {
		t.Errorf("approve on a REQUESTED credential: status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	// Rejecting it is, and it needs no text.
	rr = f.resolve(t, escID, map[string]any{"action": "reject"})
	if rr.Code != http.StatusOK {
		t.Fatalf("reject without text: status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var res sql.NullString
	_ = f.h.db.QueryRow(`SELECT resolution FROM escalations WHERE id = ?`, escID).Scan(&res)
	if res.Valid {
		t.Errorf("a CREDENTIAL escalation must store NULL resolution, got %q", res.String)
	}
	_ = f.h.db.QueryRow(`SELECT status FROM credentials WHERE id = ?`, credID).Scan(&status)
	if status != "REJECTED" {
		t.Errorf("rejected ask must dispose of the REQUESTED row, status = %q", status)
	}
}

// A proposal (agent-supplied value) approved through /resolve: no text needed,
// nothing stored in resolution, and the waiting agent gets a handle.
func TestResolveEscalation_CredentialProposal_ApproveAnswersWithHandle(t *testing.T) {
	f := newSupplyFixture(t)
	escID, credID := f.ask(t, `{"name":"REDIS_URL","type":"SECRET","security_level":3,"value":"redis://:p@h:6379/0"}`)
	enableAutoLease(t, f.h.db, f.wsID, 900)
	ch := f.h.registerEscalationWaiter(escID)

	rr := f.resolve(t, escID, map[string]any{"action": "approve"})
	if rr.Code != http.StatusOK {
		t.Fatalf("approve: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	select {
	case res := <-ch:
		if res.Resolution != "" {
			t.Errorf("waiter Resolution = %q, want empty on a CREDENTIAL answer", res.Resolution)
		}
		if res.Credential == nil || res.Credential.ID != credID || res.Credential.Name != "REDIS_URL" {
			t.Fatalf("waiter must receive the credential handle, got %+v", res.Credential)
		}
		if !res.Credential.Granted {
			t.Error("approve with auto-lease on grants the proposer; handle must say granted")
		}
		if got := string(supplyJSON(t, resolvedEscalationBody(res))); strings.Contains(got, `"resolution"`) || strings.Contains(got, "redis://") {
			t.Errorf("wire body must carry neither a resolution key nor the value: %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter was not notified")
	}
	assertValueNowhere(t, f, "redis://:p@h:6379/0")
}

// --- supply: the full flow --------------------------------------------------------

func TestSupplyEscalationCredential_FullFlow(t *testing.T) {
	f := newSupplyFixture(t)
	escID, credID := f.ask(t, askMetadata)
	enableAutoLease(t, f.h.db, f.wsID, 900)
	ch := f.h.registerEscalationWaiter(escID)
	const canary = "pg-s3cret-value-7f3a9c" //gitleaks:allow — test fixture asserting the value reaches only the vault

	rr := f.supply(t, escID, map[string]any{"value": canary})
	if rr.Code != http.StatusOK {
		t.Fatalf("supply: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Status     string `json:"status"`
		Credential struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			HandleOnly bool   `json:"handle_only"`
			Granted    bool   `json:"granted"`
			Use        string `json:"use"`
			Lease      string `json:"lease_expires_at"`
		} `json:"credential"`
		AgentStillWaiting bool `json:"agent_still_waiting"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Credential.ID != credID || resp.Credential.Name != "PG_PASSWORD" || !resp.Credential.HandleOnly ||
		!resp.Credential.Granted || resp.Credential.Use != "keeper_execute" {
		t.Errorf("response handle = %+v", resp.Credential)
	}
	if resp.Credential.Lease == "" {
		t.Error("with auto-lease on, the grant must be leased (lease_expires_at set)")
	}
	if strings.Contains(rr.Body.String(), canary) {
		t.Fatal("the supply response echoed the value")
	}

	// The vault holds it, and only the vault.
	var status, enc, approvedBy string
	var handleOnly int
	if err := f.h.db.QueryRow(`SELECT status, encrypted_value, handle_only, COALESCE(approved_by_user_id,'')
		FROM credentials WHERE id = ?`, credID).Scan(&status, &enc, &handleOnly, &approvedBy); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if status != "ACTIVE" || handleOnly != 1 || approvedBy != f.userID {
		t.Errorf("credential after supply: status=%s handle_only=%d approved_by=%s", status, handleOnly, approvedBy)
	}
	if dec, err := encryption.Decrypt(enc); err != nil || dec != canary {
		t.Errorf("vault value = (%q, %v), want the supplied value", dec, err)
	}

	// The grant is the answer.
	var envVar string
	var expires sql.NullString
	if err := f.h.db.QueryRow(`SELECT env_var_name, expires_at FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
		f.agentID, credID).Scan(&envVar, &expires); err != nil {
		t.Fatalf("the asking agent must be granted the credential: %v", err)
	}
	if envVar != "PG_PASSWORD" || !expires.Valid {
		t.Errorf("grant = (%s, lease=%v), want PG_PASSWORD leased", envVar, expires.Valid)
	}

	// The escalation is resolved with nothing in resolution.
	var escStatus, action string
	var res sql.NullString
	if err := f.h.db.QueryRow(`SELECT status, COALESCE(action,''), resolution FROM escalations WHERE id = ?`, escID).
		Scan(&escStatus, &action, &res); err != nil {
		t.Fatalf("read escalation: %v", err)
	}
	if escStatus != escalationStatusResolved || action != "approve" || res.Valid {
		t.Errorf("escalation = (%s, %s, resolution valid=%v), want RESOLVED/approve/NULL", escStatus, action, res.Valid)
	}
	var inboxState string
	_ = f.h.db.QueryRow(`SELECT state FROM inbox_items WHERE source_id = ?`, escID).Scan(&inboxState)
	if inboxState != "resolved" {
		t.Errorf("inbox item state = %q, want resolved", inboxState)
	}

	// The waiting agent gets the handle, not the value.
	select {
	case got := <-ch:
		if got.Credential == nil || got.Credential.Name != "PG_PASSWORD" || got.Resolution != "" {
			t.Errorf("waiter result = %+v", got)
		}
		body := string(supplyJSON(t, resolvedEscalationBody(got)))
		if strings.Contains(body, `"resolution"`) {
			t.Errorf("wire body must not carry a resolution key: %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter was not notified")
	}

	// And a later poll (the agent's retry, or its next run) reads the same
	// answer back from the row.
	code, wire := escWaitAuthzCall(t, f.h, escID, f.wsID, "")
	if code != http.StatusOK || !strings.Contains(wire, `"credential"`) || strings.Contains(wire, `"resolution"`) {
		t.Errorf("wait on the resolved row: code=%d body=%s", code, wire)
	}

	// Supplying twice is refused — the row is no longer waiting.
	if rr := f.supply(t, escID, map[string]any{"value": "another"}); rr.Code != http.StatusConflict {
		t.Errorf("second supply: status = %d, want 409", rr.Code)
	}

	assertValueNowhere(t, f, canary)
}

// A legacy ask — prose, no staged row — is supplied by naming the credential.
func TestSupplyEscalationCredential_LegacyAskNeedsName(t *testing.T) {
	f := newSupplyFixture(t)
	escID, credID := f.ask(t, "")
	if credID != "" {
		t.Fatal("a prose ask stages nothing")
	}
	const canary = "legacy-value-0c1d2e" //gitleaks:allow — test fixture

	if rr := f.supply(t, escID, map[string]any{"value": canary}); rr.Code != http.StatusBadRequest {
		t.Errorf("supply without a name on a legacy ask: status = %d, want 400", rr.Code)
	}
	if rr := f.supply(t, escID, map[string]any{"value": canary, "name": "not a var name!"}); rr.Code != http.StatusBadRequest {
		t.Errorf("supply with an illegal name: status = %d, want 400", rr.Code)
	}
	rr := f.supply(t, escID, map[string]any{"value": canary, "name": "gh-token", "type": "cli_token", "security_level": 2})
	if rr.Code != http.StatusOK {
		t.Fatalf("supply legacy: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var name, typ, status string
	var handleOnly, level int
	var linked sql.NullString
	if err := f.h.db.QueryRow(`SELECT c.name, c.type, c.status, c.handle_only, c.security_level, e.credential_id
		FROM escalations e JOIN credentials c ON c.id = e.credential_id WHERE e.id = ?`, escID).
		Scan(&name, &typ, &status, &handleOnly, &level, &linked); err != nil {
		t.Fatalf("the supplied credential must be created and linked: %v", err)
	}
	if name != "GH_TOKEN" || typ != "CLI_TOKEN" || status != "ACTIVE" || handleOnly != 1 || level != 2 {
		t.Errorf("legacy credential = %s/%s/%s handle_only=%d level=%d", name, typ, status, handleOnly, level)
	}
	var n int
	_ = f.h.db.QueryRow(`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`, f.agentID, linked.String).Scan(&n)
	if n != 1 {
		t.Error("the asking agent must be granted the supplied credential")
	}
	assertValueNowhere(t, f, canary)

	// A live namesake is a conflict, never an overwrite.
	esc2, _ := f.ask(t, "")
	if rr := f.supply(t, esc2, map[string]any{"value": "x", "name": "GH_TOKEN"}); rr.Code != http.StatusConflict {
		t.Errorf("supply under a live name: status = %d, want 409", rr.Code)
	}
}

func TestSupplyEscalationCredential_Guards(t *testing.T) {
	f := newSupplyFixture(t)

	// Not a CREDENTIAL escalation.
	execOrFatal(t, f.h.db, `INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, created_at)
		VALUES ('sup-text', ?, ?, 'sup-chat', ?, 'q', 'TEXT', 'PENDING', datetime('now'))`, f.wsID, f.crewID, f.agentID)
	if rr := f.supply(t, "sup-text", map[string]any{"value": "v"}); rr.Code != http.StatusBadRequest {
		t.Errorf("TEXT escalation: status = %d, want 400", rr.Code)
	}
	// Empty and oversized values.
	escID, _ := f.ask(t, askMetadata)
	if rr := f.supply(t, escID, map[string]any{"value": ""}); rr.Code != http.StatusBadRequest {
		t.Errorf("empty value: status = %d, want 400", rr.Code)
	}
	if rr := f.supply(t, escID, map[string]any{"value": strings.Repeat("a", maxCredentialValueLen+1)}); rr.Code != http.StatusBadRequest {
		t.Errorf("oversized value: status = %d, want 400", rr.Code)
	}
	if rr := f.supply(t, escID, map[string]any{"value": "v", "security_level": 7}); rr.Code != http.StatusBadRequest {
		t.Errorf("bad security_level: status = %d, want 400", rr.Code)
	}
	// A proposal (agent-supplied value) is not supplied, it is approved.
	prop, _ := f.ask(t, `{"name":"PROPOSED","type":"SECRET","value":"v"}`)
	if rr := f.supply(t, prop, map[string]any{"value": "v"}); rr.Code != http.StatusConflict {
		t.Errorf("supply on a proposal: status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	// Terminal is terminal.
	if rr := f.resolve(t, escID, map[string]any{"action": "reject"}); rr.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rr.Code, rr.Body.String())
	}
	if rr := f.supply(t, escID, map[string]any{"value": "v"}); rr.Code != http.StatusConflict {
		t.Errorf("supply on a rejected escalation: status = %d, want 409", rr.Code)
	}
	// Unknown id.
	if rr := f.supply(t, "ghost", map[string]any{"value": "v"}); rr.Code != http.StatusNotFound {
		t.Errorf("unknown escalation: status = %d, want 404", rr.Code)
	}
	// Role gate: a MEMBER cannot supply.
	esc3, _ := f.ask(t, `{"name":"OTHER","type":"SECRET"}`)
	req := httptest.NewRequest("POST", "/", jsonBody(map[string]any{"value": "v"}))
	req.SetPathValue("escalationId", esc3)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: f.userID}), f.wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	f.h.SupplyEscalationCredential(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MEMBER supply: status = %d, want 403", rr.Code)
	}
}

// Supplying the value is the decision, so the four-eyes rule applies to it
// exactly as to approve: the owner of the asking agent may not be the one who
// types the value when the workspace opted in.
func TestSupplyEscalationCredential_FourEyes(t *testing.T) {
	f := newSupplyFixture(t)
	execOrFatal(t, f.h.db, `UPDATE agents SET created_by_user_id = ? WHERE id = ?`, f.userID, f.agentID)
	if err := governance.Upsert(context.Background(), f.h.db, f.wsID, governance.Settings{RequireSecondApprover: true}, f.userID); err != nil {
		t.Fatalf("enable require_second_approver: %v", err)
	}
	escID, credID := f.ask(t, askMetadata)

	rr := f.supply(t, escID, map[string]any{"value": "v"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("owner supplying their own agent's ask: status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	var status string
	_ = f.h.db.QueryRow(`SELECT status FROM credentials WHERE id = ?`, credID).Scan(&status)
	if status != credentialStatusRequested {
		t.Errorf("a refused supply must leave the row REQUESTED, got %q", status)
	}
	blocked := false
	for _, e := range f.journal.entries {
		if e.Type == journal.EntryKeeperDecision && e.Payload["action"] == "supply" {
			blocked = true
		}
	}
	if !blocked {
		t.Error("the blocked self-supply must be journaled as a segregation-of-duties event")
	}

	// A second manager can.
	other := seedTestUserWithPassword(t, f.h.db, "second-approver@example.com", "pw-second-approver")
	execOrFatal(t, f.h.db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-other', ?, ?, 'MANAGER')`, f.wsID, other)
	req := httptest.NewRequest("POST", "/", jsonBody(map[string]any{"value": "v"}))
	req.SetPathValue("escalationId", escID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: other}), f.wsID, "MANAGER"))
	rr = httptest.NewRecorder()
	f.h.SupplyEscalationCredential(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("second approver supply: status = %d; body=%s", rr.Code, rr.Body.String())
	}
}

// A settled CREDENTIAL row read back through the wait endpoint answers with the
// handle; the resolution column — a historical marker, or NULL — is never sent.
func TestWaitForEscalation_CredentialRow_NeverSendsResolution(t *testing.T) {
	f := newSupplyFixture(t)
	escID, credID := f.ask(t, askMetadata)
	if rr := f.supply(t, escID, map[string]any{"value": "v"}); rr.Code != http.StatusOK {
		t.Fatalf("supply: %d %s", rr.Code, rr.Body.String())
	}
	// Pretend this row predates #2376 and carries the marker the migration
	// wrote — the read path must not forward it either.
	execOrFatal(t, f.h.db, `UPDATE escalations SET resolution = '[credential submitted]' WHERE id = ?`, escID)

	code, body := escWaitAuthzCall(t, f.h, escID, f.wsID, "")
	if code != http.StatusOK {
		t.Fatalf("wait: %d %s", code, body)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(body), &got)
	if _, has := got["resolution"]; has {
		t.Errorf("resolution key present on a CREDENTIAL answer: %s", body)
	}
	cred, _ := got["credential"].(map[string]any)
	if cred["id"] != credID || cred["name"] != "PG_PASSWORD" || cred["use"] != "keeper_execute" {
		t.Errorf("credential handle = %v", cred)
	}
}

// --- disposal covers REQUESTED ------------------------------------------------------

func TestDisposeStagedCredential_CoversRequested(t *testing.T) {
	f := newSupplyFixture(t)
	_, credID := f.ask(t, askMetadata)
	f.h.disposeStagedCredential(context.Background(), f.wsID, credID, "escalation cancelled by operator")
	var status string
	var deleted sql.NullString
	_ = f.h.db.QueryRow(`SELECT status, deleted_at FROM credentials WHERE id = ?`, credID).Scan(&status, &deleted)
	if status != "REJECTED" || !deleted.Valid {
		t.Errorf("REQUESTED row after disposal = (%s, deleted=%v), want REJECTED and soft-deleted", status, deleted.Valid)
	}
}

// --- helpers ------------------------------------------------------------------------

func supplyJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// assertValueNowhere is the sink sweep: every durable table a decision
// touches, every journal entry the fixture recorded, and every log line the
// handler wrote. The credentials table is checked too — the value is allowed
// there only as ciphertext, and a ciphertext never contains its plaintext.
func assertValueNowhere(t *testing.T, f supplyFixture, value string) {
	t.Helper()
	for _, table := range []string{"escalations", "inbox_items", "journal_entries", "credential_events", "agent_credentials", "credentials", "keeper_requests"} {
		rows, err := f.h.db.Query(`SELECT * FROM ` + table)
		if err != nil {
			// A table this schema does not have is not a sink.
			continue
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			for i, v := range vals {
				var s string
				switch x := v.(type) {
				case string:
					s = x
				case []byte:
					s = string(x)
				default:
					continue
				}
				if strings.Contains(s, value) {
					t.Errorf("value found in %s.%s", table, cols[i])
				}
			}
		}
		rows.Close()
	}
	for _, e := range f.journal.entries {
		if strings.Contains(e.Summary, value) || strings.Contains(string(supplyJSON(t, e.Payload)), value) {
			t.Errorf("value found in journal entry %s", e.Type)
		}
	}
	if strings.Contains(f.logs.String(), value) {
		t.Error("value found in the server log")
	}
}
