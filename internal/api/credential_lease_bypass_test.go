package api

// Explicit attempt-to-bypass tests for the credential-lease TTL invariant
// (#1486, #1373).
//
// The invariant in one line: a grant that carries an expires_at stops
// delivering the secret at that instant, and nothing short of a fresh
// authorised decision puts it back.
//
// Existing coverage (credential_lease_mint_test.go, credential_lease_delivery_test.go,
// internal/sidecar/credstore_lease_test.go) asserts that a lease is minted
// correctly and that an expired one is refused. What is written here is the
// other direction: each test is an attacker plan for getting a lapsed or
// unauthorised lease to work, and asserts the plan fails. Per #1486's addition,
// the paths that turn a red (denied) into a green (allowed) — re-mint on a
// fresh decision, and escalation approve — get their own tests, because a
// repair an attacker can trigger is a bypass with better branding.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// ---------------------------------------------------------------------------
// 1. The TTL cannot be widened by the caller who asks for it.
// ---------------------------------------------------------------------------

// TestAddCredential_TTLCannotBeWidenedPastTheLeaseCap: the manual assignment
// endpoint takes the TTL straight from the request body, so it is the one place
// a caller names their own expiry. A "lease" of a hundred years is a standing
// grant wearing a lease's clothes — it defeats the ephemerality guarantee while
// still reporting as leased in every UI and audit row.
//
// The overflow case is the interesting one: `time.Duration(ttl) * time.Second`
// overflows int64 for large ttl and wraps to a NEGATIVE duration, which would
// write an expires_at in the PAST — or, one wrap further, an absurd future.
// Either way the cap must reject it before the arithmetic happens.
func TestAddCredential_TTLCannotBeWidenedPastTheLeaseCap(t *testing.T) {
	cases := []struct {
		name string
		ttl  int64
	}{
		{"one second past the 30-day cap", maxCredentialLeaseSeconds + 1},
		{"a hundred years", 100 * 365 * 24 * 60 * 60},
		{"int64 max — overflows time.Duration arithmetic", 1<<63 - 1},
		{"large enough to wrap time.Duration to a negative", 1 << 62},
		{"negative — an expiry in the past, i.e. an instantly-dead grant", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			agentID, credID := seedLeaseCapFixture(t, db, wsID)

			h := NewAgentHandler(db, newTestLogger())
			body, err := json.Marshal(map[string]any{
				"credential_id": credID,
				"env_var_name":  "TOKEN",
				"ttl_seconds":   tc.ttl,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
			req.SetPathValue("agentId", agentID)
			req = req.WithContext(withWorkspace(
				withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
			rr := httptest.NewRecorder()
			h.AddCredential(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("ttl_seconds=%d accepted with status %d (body %s) — the lease cap is the only "+
					"thing stopping a caller from minting an effectively permanent grant",
					tc.ttl, rr.Code, rr.Body.String())
			}
			// And nothing may have been written on the way to the rejection.
			var n int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
				agentID, credID).Scan(&n); err != nil {
				t.Fatalf("count grants: %v", err)
			}
			if n != 0 {
				t.Errorf("the rejected request still created %d grant row(s)", n)
			}
		})
	}
}

// TestAddCredential_ReassignCannotSilentlyRefreshALapsedLease: the operator path
// is INSERT-only, so re-POSTing an (agent, credential) pair whose lease has
// already lapsed must conflict rather than quietly issue a new expiry. If it
// upserted, "refresh any expired lease" would be a single unaudited call away
// and the DELETE-then-reassign trail — the thing that makes a re-grant visible —
// would never be written.
func TestAddCredential_ReassignCannotSilentlyRefreshALapsedLease(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID, credID := seedLeaseCapFixture(t, db, wsID)

	lapsed := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	execOrFatal(t, db, `
		INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at, expires_at, lease_source)
		VALUES (?, ?, ?, 'TOKEN', 0, ?, ?, ?)`,
		generateCUID(), agentID, credID, time.Now().UTC().Format(time.RFC3339), lapsed, leaseSourceManual)

	h := NewAgentHandler(db, newTestLogger())
	body, err := json.Marshal(map[string]any{
		"credential_id": credID,
		"env_var_name":  "TOKEN",
		"ttl_seconds":   3600,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.SetPathValue("agentId", agentID)
	req = req.WithContext(withWorkspace(
		withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)

	if rr.Code == http.StatusOK || rr.Code == http.StatusCreated {
		t.Fatalf("re-assigning an existing grant succeeded (%d) — an expired lease can be refreshed "+
			"in place with no revocation record", rr.Code)
	}
	var got string
	if err := db.QueryRow(
		`SELECT expires_at FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
		agentID, credID).Scan(&got); err != nil {
		t.Fatalf("read expiry: %v", err)
	}
	if got != lapsed {
		t.Errorf("expires_at = %q, want the lapsed %q — the rejected re-assign moved the expiry anyway", got, lapsed)
	}
}

// ---------------------------------------------------------------------------
// 2. A lapsed lease cannot be revived except by a decision that would have
//    granted it in the first place.
// ---------------------------------------------------------------------------

// TestIssueCredentialLease_LapsedLeaseNotRevivedWithoutEligibility: re-minting
// on a fresh ALLOW is deliberate (cmd_keeper_auto_lease.go:32-35 — "ask again,
// which mints a fresh lease"), and it is also the single mechanism that turns a
// dead grant back into a live one. So the eligibility gate on that mechanism is
// load-bearing, and this walks every way it can be missing.
//
// Each case starts from an ALREADY-EXPIRED grant — the state an attacker is
// trying to escape — and asks for a lease under conditions that must not
// qualify. The assertion is not merely `issued == false`: it is that the grant
// is still expired afterwards, which is the property that actually matters.
func TestIssueCredentialLease_LapsedLeaseNotRevivedWithoutEligibility(t *testing.T) {
	cases := []struct {
		name  string
		mutIn func(*leaseIssueInput)
	}{
		{
			// The workspace has not opted in. Auto-issuance off must mean off,
			// including for a grant that is begging to be renewed.
			name:  "auto-lease off (ttl 0)",
			mutIn: func(in *leaseIssueInput) { in.TTLSeconds = 0 },
		},
		{
			// A negative TTL would compute an expiry in the past. It must be
			// refused outright rather than "issued" as an already-dead lease,
			// which would overwrite lease_source and launder the provenance.
			name:  "negative ttl",
			mutIn: func(in *leaseIssueInput) { in.TTLSeconds = -3600 },
		},
		{
			// Below L3 the credential is boot-delivered self-service. An
			// attacker who can influence the reported security level must not
			// be able to use the L1/L2 tier as a renewal channel for a grant
			// that was leased as L3.
			name:  "credential reported below L3",
			mutIn: func(in *leaseIssueInput) { in.SecurityLevel = int(keeper.SecurityLevelL2) },
		},
		{
			name:  "no agent id",
			mutIn: func(in *leaseIssueInput) { in.AgentID = "" },
		},
		{
			name:  "no credential id",
			mutIn: func(in *leaseIssueInput) { in.CredentialID = "" },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
			setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL3))

			lapsed := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
			if _, err := db.Exec(
				`UPDATE agent_credentials SET expires_at = ?, lease_source = ? WHERE agent_id = ? AND credential_id = ?`,
				lapsed, leaseSourceKeeperAllow, agentID, credID); err != nil {
				t.Fatalf("seed lapsed lease: %v", err)
			}

			in := leaseIssueInput{
				WorkspaceID:   wsID,
				CrewID:        crewID,
				AgentID:       agentID,
				CredentialID:  credID,
				SecurityLevel: int(keeper.SecurityLevelL3),
				Source:        leaseSourceKeeperAllow,
				TTLSeconds:    900,
			}
			tc.mutIn(&in)

			if _, issued := issueCredentialLease(
				context.Background(), db, leaseTestLogger(), noopEmitter{}, in); issued {
				t.Fatalf("an ineligible request (%s) revived a lapsed lease", tc.name)
			}

			exp, _, _ := readGrantLease(t, db, agentID, credID)
			if exp != lapsed {
				t.Fatalf("expires_at moved from %q to %q on an ineligible request — the grant is live again", lapsed, exp)
			}
			// The strongest form of the assertion: the delivery gate still says no.
			if leaseGatePasses(t, db, agentID, credID) {
				t.Fatal("the delivery gate now accepts a grant that was never legitimately re-leased")
			}
		})
	}
}

// TestIssueCredentialLease_CannotConvertALeaseIntoAStandingGrant: NULL
// expires_at means "never expires". Nothing in the mint path may produce it —
// otherwise the cheapest attack on the whole feature is to trigger a lease
// issuance with a TTL that clears the column instead of setting it.
func TestIssueCredentialLease_CannotConvertALeaseIntoAStandingGrant(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL3))

	soon := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	if _, err := db.Exec(
		`UPDATE agent_credentials SET expires_at = ? WHERE agent_id = ? AND credential_id = ?`,
		soon, agentID, credID); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	for _, ttl := range []int{0, -1, -86400} {
		if _, issued := issueCredentialLease(context.Background(), db, leaseTestLogger(), noopEmitter{},
			leaseIssueInput{
				WorkspaceID:   wsID,
				CrewID:        crewID,
				AgentID:       agentID,
				CredentialID:  credID,
				SecurityLevel: int(keeper.SecurityLevelL3),
				Source:        leaseSourceKeeperAllow,
				TTLSeconds:    ttl,
			}); issued {
			t.Errorf("ttl=%d was reported as issuing a lease", ttl)
		}
		var expires sql.NullString
		if err := db.QueryRow(
			`SELECT expires_at FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
			agentID, credID).Scan(&expires); err != nil {
			t.Fatalf("read expiry: %v", err)
		}
		if !expires.Valid {
			t.Fatalf("ttl=%d cleared expires_at — the leased grant is now standing (never expires)", ttl)
		}
		if expires.String != soon {
			t.Errorf("ttl=%d rewrote expires_at to %q, want the untouched %q", ttl, expires.String, soon)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. The format invariant the whole TTL check silently depends on.
// ---------------------------------------------------------------------------

// TestCredentialLease_EveryWriterEmitsAComparableTimestamp: the enforcement
// predicate is `ac.expires_at > ?` — a TEXT comparison. That is a valid ordering
// ONLY while every writer emits the same fixed-width RFC3339 UTC form
// (credential_lease.go:303-309 says so explicitly). A writer that ever emitted a
// local offset ("+02:00"), fractional seconds, or a lowercase 'z' would sort
// unpredictably against the others, and the failure mode is silent and
// one-directional: expired leases start passing the gate.
//
// This is the "prove the property the check rests on" test rather than the
// "prove the check runs" test — the latter already exists, and would keep
// passing while this broke.
func TestCredentialLease_EveryWriterEmitsAComparableTimestamp(t *testing.T) {
	want := len(time.Now().UTC().Format(time.RFC3339)) // 20: 2006-01-02T15:04:05Z
	check := func(t *testing.T, writer, got string) {
		t.Helper()
		if got == "" {
			t.Fatalf("%s: wrote an empty expires_at", writer)
		}
		if len(got) != want {
			t.Errorf("%s: expires_at %q is %d chars, want %d — a variable-width timestamp breaks the "+
				"TEXT ordering the lease gate compares with", writer, got, len(got), want)
		}
		if got[len(got)-1] != 'Z' {
			t.Errorf("%s: expires_at %q is not UTC-suffixed; an offset form sorts against UTC values "+
				"lexicographically, not chronologically", writer, got)
		}
		if _, err := time.Parse(time.RFC3339, got); err != nil {
			t.Errorf("%s: expires_at %q does not parse as RFC3339: %v", writer, got, err)
		}
	}

	// Writer 1: the operator path (AddCredential).
	t.Run("AddCredential", func(t *testing.T) {
		db := setupTestDB(t)
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)
		agentID, credID := seedLeaseCapFixture(t, db, wsID)

		h := NewAgentHandler(db, newTestLogger())
		body, err := json.Marshal(map[string]any{
			"credential_id": credID, "env_var_name": "TOKEN", "ttl_seconds": 3600,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req.SetPathValue("agentId", agentID)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.AddCredential(rr, req)
		if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
			t.Fatalf("AddCredential: status %d body %s", rr.Code, rr.Body.String())
		}
		var manual string
		if err := db.QueryRow(
			`SELECT expires_at FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
			agentID, credID).Scan(&manual); err != nil {
			t.Fatalf("read manual expiry: %v", err)
		}
		check(t, "AddCredential", manual)
	})

	// Writer 2: the auto-issuer (issueCredentialLease).
	t.Run("issueCredentialLease", func(t *testing.T) {
		db := setupTestDB(t)
		wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
		setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL3))
		minted, issued := issueCredentialLease(context.Background(), db, leaseTestLogger(), noopEmitter{},
			leaseIssueInput{
				WorkspaceID:   wsID,
				CrewID:        crewID,
				AgentID:       agentID,
				CredentialID:  credID,
				SecurityLevel: int(keeper.SecurityLevelL3),
				Source:        leaseSourceKeeperAllow,
				TTLSeconds:    900,
			})
		if !issued {
			t.Fatal("issueCredentialLease did not mint — fixture problem, the format assertion would be vacuous")
		}
		check(t, "issueCredentialLease (return value)", minted)
		stored, _, _ := readGrantLease(t, db, agentID, credID)
		check(t, "issueCredentialLease (stored column)", stored)
	})

	// Writer 3: the comparison value itself, which has to sort in the same space.
	t.Run("leaseComparisonNow", func(t *testing.T) {
		check(t, "leaseComparisonNow", leaseComparisonNow())
	})
}

// ---------------------------------------------------------------------------
// 4. The recovery path: approve is what turns a red into a green.
// ---------------------------------------------------------------------------

// TestEscalationApprove_BlockedSelfApprovalMintsNoLease is the #1486 addition
// applied to leases. Escalation approve is literally the mechanism that turns a
// PENDING_APPROVAL credential into an ACTIVE, LEASED grant — the exact shape the
// issue says to test: "given a mechanism that turns a red into a green, can an
// attacker reach that state deliberately?"
//
// The four-eyes rule (#1084) is what stands in the way when the approver is the
// initiating agent's own owner. It is checked BEFORE any mutation, and the
// existing coverage (escalation_segregation_test.go) asserts the 403. What it
// does not assert — because those fixtures carry no credential_id — is that the
// blocked call also mints NO LEASE. A 403 that still left a live leased grant
// behind would be the whole control defeated while reporting as enforced.
func TestEscalationApprove_BlockedSelfApprovalMintsNoLease(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)

	// The lead agent is owned by the very user who will try to approve.
	execOrFatal(t, h.db, `UPDATE agents SET created_by_user_id = ? WHERE id = ?`, userID, leadID)
	escID, credID := seedCredentialEscalation(t, h.db, userID, wsID, crewID, leadID)

	if err := governance.Upsert(context.Background(), h.db, wsID, governance.Settings{
		DenyNotifyMinRisk:     7,
		AutoLeaseSeconds:      900,
		RequireSecondApprover: true,
	}, ""); err != nil {
		t.Fatalf("enable four-eyes + auto-lease: %v", err)
	}

	rr := approveEscalation(t, h, userID, wsID, escID)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("self-approval returned %d, want 403 (body %s)", rr.Code, rr.Body.String())
	}

	// No grant at all — not a standing one, and certainly not a leased one.
	var grants int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
		leadID, credID).Scan(&grants); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if grants != 0 {
		t.Errorf("the blocked self-approval still created %d grant(s) — the four-eyes rule reports "+
			"enforced while the credential is delivered anyway", grants)
	}

	// And the credential itself must still be waiting for a second human.
	var status string
	if err := h.db.QueryRow(`SELECT status FROM credentials WHERE id = ?`, credID).Scan(&status); err != nil {
		t.Fatalf("read credential status: %v", err)
	}
	if status != "PENDING_APPROVAL" {
		t.Errorf("credential status = %q after a blocked approval, want PENDING_APPROVAL", status)
	}

	// The escalation must still be open, so a genuine second approver can act.
	var escStatus string
	if err := h.db.QueryRow(`SELECT status FROM escalations WHERE id = ?`, escID).Scan(&escStatus); err != nil {
		t.Fatalf("read escalation status: %v", err)
	}
	if escStatus != "PENDING" {
		t.Errorf("escalation status = %q after a blocked approval, want PENDING", escStatus)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// seedLeaseCapFixture inserts a minimal agent + credential pair in wsID with no
// grant between them, so the assignment endpoint has something to assign.
func seedLeaseCapFixture(t *testing.T, db *sql.DB, wsID string) (agentID, credID string) {
	t.Helper()
	crewID := generateCUID()
	agentID = generateCUID()
	credID = generateCUID()
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew', ?)`,
		crewID, wsID, "crew-"+crewID)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES (?, ?, ?, 'Agent', ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
		agentID, wsID, crewID, "agent-"+agentID)
	execOrFatal(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type,
		provider, scope, security_level, status, created_by)
		VALUES (?, ?, ?, 'v1:aW52YWxpZA==', 'SECRET', 'NONE', 'WORKSPACE', 3, 'ACTIVE', 'test-user-id')`,
		credID, wsID, "cred-"+credID)
	return agentID, credID
}

// leaseGatePasses evaluates the canonical delivery predicate against a grant.
// Using credentialLeaseGateSQL itself (rather than re-spelling the comparison)
// is deliberate: a test that hand-rolls the predicate would keep passing if the
// production one were weakened.
func leaseGatePasses(t *testing.T, db *sql.DB, agentID, credID string) bool {
	t.Helper()
	var n int
	q := fmt.Sprintf(
		`SELECT COUNT(*) FROM agent_credentials ac WHERE ac.agent_id = ? AND ac.credential_id = ? AND %s`,
		credentialLeaseGateSQL)
	if err := db.QueryRow(q, agentID, credID, leaseComparisonNow()).Scan(&n); err != nil {
		t.Fatalf("evaluate lease gate: %v", err)
	}
	return n > 0
}
