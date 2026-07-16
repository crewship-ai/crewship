package api

import (
	"bytes"
	"database/sql"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAgentCred_Add_AutoResolvesMatchingEscalation covers issue #1198:
// a human granting an agent's credential need via `credential create` +
// `credential assign` (rather than `escalation resolve --action approve`)
// must close out the matching PENDING escalation, not leave it stuck
// forever.
func TestAgentCred_Add_AutoResolvesMatchingEscalation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	// seedAgentCredEnv names the credential "test-cred" (see seedCredentialEnc
	// call in agent_credentials_test.go).
	//
	// type='CREDENTIAL' + credential_id NULL is exactly what the human
	// create+assign path this feature serves looks like in production: the
	// escalation contract agents are given (orchestrator/exec.go) has them
	// raise credential asks as type='CREDENTIAL', and credential_id is only
	// set when the agent proposed a value inline (v119) — which it did not
	// here. The row previously seeded here omitted `type`, silently
	// defaulting to 'TEXT' (a shape a real credential ask never has).
	escID := "esc-match"
	seedAutoResolveEscalation(t, db, escID, wsID, agentID, "CREDENTIAL",
		"Need test-cred to call the API, I don't have it", "")

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != 201 {
		t.Fatalf("AddCredential status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var status, resolution, resolvedBy string
	if err := db.QueryRow(`SELECT status, COALESCE(resolution,''), COALESCE(resolved_by,'') FROM escalations WHERE id = ?`, escID).
		Scan(&status, &resolution, &resolvedBy); err != nil {
		t.Fatalf("query escalation: %v", err)
	}
	if status != "RESOLVED" {
		t.Errorf("escalation status = %q, want RESOLVED", status)
	}
	if resolvedBy != "system" {
		t.Errorf("resolved_by = %q, want system", resolvedBy)
	}
	if resolution == "" {
		t.Errorf("resolution should be populated, got empty")
	}
}

// TestAgentCred_Add_DoesNotAutoResolveOtherAgentEscalation ensures the
// matching is scoped to the SAME agent the credential was assigned to — a
// PENDING escalation from a different agent (even one that happens to
// mention the same credential name) must not be touched.
func TestAgentCred_Add_DoesNotAutoResolveOtherAgentEscalation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	otherAgentID := "agent-other"
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, 'B', 'b')`, otherAgentID, wsID); err != nil {
		t.Fatalf("seed other agent: %v", err)
	}
	_ = userID

	// Seeded as a genuine credential ask (type='CREDENTIAL', no inline
	// proposal) so the ONLY thing keeping it PENDING is the agent scoping
	// this test is about — a 'TEXT' row would be filtered out earlier and
	// pass vacuously.
	escID := "esc-other-agent"
	seedAutoResolveEscalation(t, db, escID, wsID, otherAgentID, "CREDENTIAL",
		"Need test-cred to call the API", "")

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != 201 {
		t.Fatalf("AddCredential status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM escalations WHERE id = ?`, escID).Scan(&status); err != nil {
		t.Fatalf("query escalation: %v", err)
	}
	if status != "PENDING" {
		t.Errorf("escalation status = %q, want PENDING (different agent, must not auto-resolve)", status)
	}
}

// TestAgentCred_Add_DoesNotAutoResolveUnrelatedEscalation ensures a PENDING
// escalation from the SAME agent that doesn't mention the credential name
// is left untouched — matching requires a whole-word name match, not just
// "same agent has any pending escalation".
func TestAgentCred_Add_DoesNotAutoResolveUnrelatedEscalation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	// type='CREDENTIAL' so the name match is the only thing under test
	// here (a 'TEXT' row would be filtered out before it and pass
	// vacuously).
	escID := "esc-unrelated"
	seedAutoResolveEscalation(t, db, escID, wsID, agentID, "CREDENTIAL",
		"Not sure how to proceed with the task, please advise", "")

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != 201 {
		t.Fatalf("AddCredential status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM escalations WHERE id = ?`, escID).Scan(&status); err != nil {
		t.Fatalf("query escalation: %v", err)
	}
	if status != "PENDING" {
		t.Errorf("escalation status = %q, want PENDING (reason doesn't mention credential name)", status)
	}
}

// TestAgentCred_Add_DoesNotAutoResolveOnGenericShortName is a
// CodeRabbit-flagged security finding: a short, generic credential name
// (e.g. "api", "key") could whole-word-match a PENDING escalation for a
// completely unrelated need the same agent happens to also be waiting on,
// auto-approving the wrong thing. minAutoResolveNameLen guards against this
// — a credential name below the threshold must never trigger auto-resolve,
// even when the (coincidental) whole-word match is real.
func TestAgentCred_Add_DoesNotAutoResolveOnGenericShortName(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID, credID := "agent-1", "cred-1"
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, 'A', 'a')`, agentID, wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	shortName := "api" // 3 chars, well under minAutoResolveNameLen
	seedCredentialEnc(t, db, wsID, userID, credID, shortName, "v")
	h := newAgentHandlerForCred(t, db)

	// type='CREDENTIAL' so minAutoResolveNameLen is the only thing keeping
	// this row PENDING (a 'TEXT' row would be filtered out before the
	// length guard and pass vacuously).
	escID := "esc-generic-name"
	seedAutoResolveEscalation(t, db, escID, wsID, agentID, "CREDENTIAL",
		"Need an api key for a totally different, unrelated integration", "")

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"API"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != 201 {
		t.Fatalf("AddCredential status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM escalations WHERE id = ?`, escID).Scan(&status); err != nil {
		t.Fatalf("query escalation: %v", err)
	}
	if status != "PENDING" {
		t.Errorf("escalation status = %q, want PENDING (credential name %q is too generic to safely auto-resolve)", status, shortName)
	}
}

// seedAutoResolveEscalation inserts a PENDING escalation with explicit
// type/credential_id so the auto-resolve filters can be exercised directly.
// credentialID == "" seeds SQL NULL (the human create+assign path this
// feature exists for); a non-empty value marks the row as carrying a
// structured agent proposal.
func seedAutoResolveEscalation(t *testing.T, db *sql.DB, escID, wsID, agentID, escType, reason, credentialID string) {
	t.Helper()
	var credVal interface{}
	if credentialID != "" {
		credVal = credentialID
	}
	if _, err := db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,credential_id,status,created_at)
		VALUES (?, ?, 'crew-1', 'chat-1', ?, ?, ?, ?, 'PENDING', ?)`,
		escID, wsID, agentID, reason, escType, credVal, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed escalation %s: %v", escID, err)
	}
}

// assignCredForAutoResolve drives the assignment that triggers auto-resolve.
func assignCredForAutoResolve(t *testing.T, h *AgentHandler, wsID, agentID, credID string) {
	t.Helper()
	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != 201 {
		t.Fatalf("AddCredential status = %d, body: %s", rr.Code, rr.Body.String())
	}
}

func escalationStatus(t *testing.T, db *sql.DB, escID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM escalations WHERE id = ?`, escID).Scan(&status); err != nil {
		t.Fatalf("query escalation %s: %v", escID, err)
	}
	return status
}

// autoResolveEnv seeds a workspace + agent + one credential named
// credName, returning the ids needed to drive an assignment.
func autoResolveEnv(t *testing.T, db *sql.DB, credName string) (wsID, agentID, credID string) {
	t.Helper()
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	agentID, credID = "agent-1", "cred-1"
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, 'A', 'a')`, agentID, wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	seedCredentialEnc(t, db, wsID, userID, credID, credName, "v")
	return wsID, agentID, credID
}

// TestAgentCred_Add_DoesNotAutoResolveAmbiguousMatches covers the hole the
// minAutoResolveNameLen guard does NOT close: the guard only rejects short,
// generic names, but a perfectly realistic name (GITHUB_TOKEN, 12 chars)
// sails past it and whole-word-matches EVERY pending escalation that
// mentions it. Two escalations from the same agent naming the same
// credential are genuinely ambiguous — one may be asking for a far broader
// grant than the human actually gave. Auto-approving both would record
// action='approve'/resolved_by='system' against a request no human ever
// granted, and hide it from `escalation list`. Ambiguity must leave the
// rows PENDING for a human.
func TestAgentCred_Add_DoesNotAutoResolveAmbiguousMatches(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	wsID, agentID, credID := autoResolveEnv(t, db, "GITHUB_TOKEN")
	h := newAgentHandlerForCred(t, db)

	// Narrow ask — the one the human actually granted.
	seedAutoResolveEscalation(t, db, "esc-narrow", wsID, agentID, "CREDENTIAL",
		"Need GITHUB_TOKEN with repo:read for crewship-ai/crewship CI", "")
	// Broader ask the human never granted — must NOT ride along.
	seedAutoResolveEscalation(t, db, "esc-broad", wsID, agentID, "CREDENTIAL",
		"GITHUB_TOKEN needs admin:org scope so I can rotate org secrets", "")

	assignCredForAutoResolve(t, h, wsID, agentID, credID)

	for _, escID := range []string{"esc-narrow", "esc-broad"} {
		if got := escalationStatus(t, db, escID); got != "PENDING" {
			t.Errorf("escalation %s status = %q, want PENDING — two escalations naming %q are ambiguous, so neither may be auto-approved", escID, got, "GITHUB_TOKEN")
		}
	}
}

// TestAgentCred_Add_DoesNotAutoResolveNonCredentialEscalation ensures the
// free-form text match can only ever close a CREDENTIAL escalation. A TEXT
// escalation (e.g. the confidence gate's auto-escalation in
// confidence_handler.go, which hardcodes type='TEXT') is not a credential
// request at all — it must never be flipped to action='approve' just
// because its free-form reason happens to name a credential.
func TestAgentCred_Add_DoesNotAutoResolveNonCredentialEscalation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	wsID, agentID, credID := autoResolveEnv(t, db, "GITHUB_TOKEN")
	h := newAgentHandlerForCred(t, db)

	seedAutoResolveEscalation(t, db, "esc-text", wsID, agentID, "TEXT",
		"Low confidence: should I use GITHUB_TOKEN here or ask the user first?", "")

	assignCredForAutoResolve(t, h, wsID, agentID, credID)

	if got := escalationStatus(t, db, "esc-text"); got != "PENDING" {
		t.Errorf("TEXT escalation status = %q, want PENDING — only CREDENTIAL escalations may auto-resolve", got)
	}
}

// TestAgentCred_Add_DoesNotAutoResolveStructuredProposal ensures an
// escalation carrying a structured proposal (credential_id set, per
// migration v119) is left to its own dedicated resolve path, which
// activates that exact PENDING_APPROVAL credential row. Fuzzy-matching it
// by name would mark the escalation approved while leaving the proposed
// credential stuck in PENDING_APPROVAL — approved on paper, never granted.
func TestAgentCred_Add_DoesNotAutoResolveStructuredProposal(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	wsID, agentID, credID := autoResolveEnv(t, db, "GITHUB_TOKEN")
	h := newAgentHandlerForCred(t, db)

	seedAutoResolveEscalation(t, db, "esc-structured", wsID, agentID, "CREDENTIAL",
		"Proposing GITHUB_TOKEN inline for approval", "cred-proposed")

	assignCredForAutoResolve(t, h, wsID, agentID, credID)

	if got := escalationStatus(t, db, "esc-structured"); got != "PENDING" {
		t.Errorf("structured-proposal escalation status = %q, want PENDING — credential_id rows resolve via the approve path, not a name match", got)
	}
}
