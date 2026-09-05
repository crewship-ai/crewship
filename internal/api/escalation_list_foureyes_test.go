package api

// escalation_list_foureyes_test.go — issue #1559.
//
// The four-eyes rule is decided at resolve time from two inputs the escalation
// list did not carry: the workspace toggle and the tier of the linked
// credential (keeper.TierPolicy.SecondApprover, which forces the rule on the
// top tier whatever the toggle says). The console therefore rendered an
// Approve button that would 403, and the first thing that taught an operator
// otherwise was the refusal.
//
// These cases pin that the list reports the same answer ResolveEscalation will
// give, for the same reasons — including the two that make it NOT apply (a
// non-credential escalation, and an agent with no recorded owner, which is the
// identity the rule compares against).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

type listedEscalation struct {
	ID                        string `json:"id"`
	Type                      string `json:"type"`
	SecondApproverRequired    bool   `json:"second_approver_required"`
	SecondApproverByWorkspace bool   `json:"second_approver_by_workspace"`
	SecondApproverByTier      bool   `json:"second_approver_by_tier"`
	SecurityLevelLabel        string `json:"security_level_label"`
}

func listEscalations(t *testing.T, h *QueryHandler, userID, wsID, crewID string) []listedEscalation {
	t.Helper()
	req := httptest.NewRequest("GET", "/?limit=10", nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ListEscalations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out []listedEscalation
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v (%s)", err, rr.Body.String())
	}
	return out
}

func TestListEscalations_ReportsWhyFourEyesApplies(t *testing.T) {
	tierFloor, ok := keeper.MinSecondApproverLevel()
	if !ok {
		t.Fatal("no tier forces a second approver — the control this test exists for is gone, which is a failure, not a reason to skip")
	}
	lowest := keeper.SecurityLevels()[0]

	cases := []struct {
		name          string
		escType       string
		level         keeper.SecurityLevel // 0 = no linked credential
		toggleOn      bool
		ownedAgent    bool
		wantRequired  bool
		wantWorkspace bool
		wantTier      bool
		wantLabel     string
	}{
		{
			name:    "toggle off, top-tier credential: the tier alone forces it",
			escType: "CREDENTIAL", level: tierFloor, ownedAgent: true,
			wantRequired: true, wantTier: true, wantLabel: tierFloor.Label(),
		},
		{
			name:    "toggle off, lowest-tier credential: nothing forces it",
			escType: "CREDENTIAL", level: lowest, ownedAgent: true,
			wantLabel: lowest.Label(),
		},
		{
			name:    "toggle on, lowest-tier credential: the workspace forces it",
			escType: "CREDENTIAL", level: lowest, toggleOn: true, ownedAgent: true,
			wantRequired: true, wantWorkspace: true, wantLabel: lowest.Label(),
		},
		{
			name:    "toggle on, top-tier credential: both reasons hold independently",
			escType: "CREDENTIAL", level: tierFloor, toggleOn: true, ownedAgent: true,
			wantRequired: true, wantWorkspace: true, wantTier: true, wantLabel: tierFloor.Label(),
		},
		{
			// The rule compares the approver against the agent's recorded owner.
			// With no owner there is nothing to compare, and ResolveEscalation
			// lets the resolve through — so the row must not claim otherwise.
			name:    "no recorded owner: the rule cannot be enforced, so it is not claimed",
			escType: "CREDENTIAL", level: tierFloor, toggleOn: true, ownedAgent: false,
			wantLabel: tierFloor.Label(),
		},
		{
			name:    "TEXT escalation: out of scope even with the toggle on",
			escType: "TEXT", toggleOn: true, ownedAgent: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			ownerID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, ownerID)
			crewID := seedCrewRow(t, db, "fe-crew", wsID, "Crew", "fe-crew")

			agentID := "fe-agent"
			if tc.ownedAgent {
				seedOwnedAgent(t, db, agentID, wsID, crewID, ownerID)
			} else {
				execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug)
					VALUES (?, ?, ?, 'Agent', ?)`, agentID, wsID, crewID, agentID)
			}

			credID := ""
			if tc.level != 0 {
				credID = "fe-cred"
				execOrFatal(t, db, `INSERT INTO credentials
					(id, workspace_id, name, encrypted_value, type, provider, scope, security_level, status, created_by)
					VALUES (?, ?, 'Deploy Key', 'v1:aW52YWxpZA==', 'SECRET', 'NONE', 'WORKSPACE', ?, 'PENDING_APPROVAL', ?)`,
					credID, wsID, int(tc.level), ownerID)
			}
			var credArg any
			if credID != "" {
				credArg = credID
			}
			execOrFatal(t, db, `INSERT INTO escalations
				(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, credential_id, status, created_at)
				VALUES ('fe-esc', ?, ?, 'fe-chat', ?, 'need a key', ?, ?, 'PENDING', datetime('now'))`,
				wsID, crewID, agentID, tc.escType, credArg)

			if tc.toggleOn {
				if err := governance.Upsert(context.Background(), db, wsID,
					governance.Settings{RequireSecondApprover: true}, ownerID); err != nil {
					t.Fatalf("enable require_second_approver: %v", err)
				}
			}

			h := NewQueryHandler(db, nil, nil, "", newTestLogger())
			items := listEscalations(t, h, ownerID, wsID, crewID)
			if len(items) != 1 {
				t.Fatalf("got %d escalations, want 1", len(items))
			}
			got := items[0]
			if got.SecondApproverRequired != tc.wantRequired {
				t.Errorf("second_approver_required = %v, want %v", got.SecondApproverRequired, tc.wantRequired)
			}
			if got.SecondApproverByWorkspace != tc.wantWorkspace {
				t.Errorf("second_approver_by_workspace = %v, want %v", got.SecondApproverByWorkspace, tc.wantWorkspace)
			}
			if got.SecondApproverByTier != tc.wantTier {
				t.Errorf("second_approver_by_tier = %v, want %v", got.SecondApproverByTier, tc.wantTier)
			}
			if got.SecurityLevelLabel != tc.wantLabel {
				t.Errorf("security_level_label = %q, want %q", got.SecurityLevelLabel, tc.wantLabel)
			}
		})
	}
}

// seedFourEyesFixture builds one crew + one agent + one PENDING escalation and
// returns the handler and the owning user. level 0 links no credential row;
// credIDOverride links a credential_id that has no row behind it.
func seedFourEyesFixture(t *testing.T, escType string, level keeper.SecurityLevel,
	credIDOverride string, toggleOn, ownedAgent bool) (*QueryHandler, string, string, string) {
	t.Helper()
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	crewID := seedCrewRow(t, db, "fe2-crew", wsID, "Crew", "fe2-crew")

	agentID := "fe2-agent"
	if ownedAgent {
		seedOwnedAgent(t, db, agentID, wsID, crewID, ownerID)
	} else {
		execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug)
			VALUES (?, ?, ?, 'Agent', ?)`, agentID, wsID, crewID, agentID)
	}

	credID := credIDOverride
	if credID == "" && level != 0 {
		credID = "fe2-cred"
		execOrFatal(t, db, `INSERT INTO credentials
			(id, workspace_id, name, encrypted_value, type, provider, scope, security_level, status, created_by)
			VALUES (?, ?, 'Deploy Key', 'v1:aW52YWxpZA==', 'SECRET', 'NONE', 'WORKSPACE', ?, 'PENDING_APPROVAL', ?)`,
			credID, wsID, int(level), ownerID)
	}
	var credArg any
	if credID != "" {
		credArg = credID
	}
	execOrFatal(t, db, `INSERT INTO escalations
		(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, credential_id, status, created_at)
		VALUES ('fe2-esc', ?, ?, 'fe2-chat', ?, 'need a key', ?, ?, 'PENDING', datetime('now'))`,
		wsID, crewID, agentID, escType, credArg)

	if toggleOn {
		if err := governance.Upsert(context.Background(), db, wsID,
			governance.Settings{RequireSecondApprover: true}, ownerID); err != nil {
			t.Fatalf("enable require_second_approver: %v", err)
		}
	}
	return NewQueryHandler(db, nil, nil, "", newTestLogger()), ownerID, wsID, crewID
}

// TestListEscalations_FourEyesMatchesResolve is the property the row copy
// actually promises: if the list says a second approver is required, the
// owner's own resolve is refused, and if it says nothing, it goes through.
// Asserting the two against each other is what keeps the explanation honest
// when either side is edited.
//
// The cases are chosen to be the ones where the two could plausibly diverge —
// each is a place where the list's single LEFT JOIN has to reproduce a decision
// ResolveEscalation reaches by a different route: a credential_id with no row
// behind it (the list gets NULL, resolve gets sql.ErrNoRows), a CREDENTIAL
// escalation carrying no credential at all (the legacy human-supplies-the-secret
// flow), a non-CREDENTIAL type, and an agent with no recorded owner, which is
// the identity the rule compares against and therefore the case where it cannot
// be enforced at all.
func TestListEscalations_FourEyesMatchesResolve(t *testing.T) {
	tierFloor, ok := keeper.MinSecondApproverLevel()
	if !ok {
		t.Fatal("no tier forces a second approver — the control this test exists for is gone, which is a failure, not a reason to skip")
	}
	lowest := keeper.SecurityLevels()[0]

	for _, tc := range []struct {
		name       string
		escType    string
		level      keeper.SecurityLevel
		credID     string // credential_id with no credentials row behind it
		toggleOn   bool
		ownedAgent bool
		wantStatus int
	}{
		{
			name:    "tier floor, toggle off: the tier alone refuses it",
			escType: "CREDENTIAL", level: tierFloor, ownedAgent: true,
			wantStatus: http.StatusForbidden,
		},
		{
			name:    "lowest tier, toggle off: nothing refuses it",
			escType: "CREDENTIAL", level: lowest, ownedAgent: true,
			wantStatus: http.StatusOK,
		},
		{
			name:    "lowest tier, toggle on: the workspace refuses it",
			escType: "CREDENTIAL", level: lowest, toggleOn: true, ownedAgent: true,
			wantStatus: http.StatusForbidden,
		},
		{
			// The legacy flow: a CREDENTIAL escalation with no vault row, where
			// the human types the secret into the resolution. The tier half has
			// nothing to read, so the toggle is the whole rule — and the list
			// must not fail closed where the resolve does not.
			name:    "no linked credential, toggle on: the workspace still refuses it",
			escType: "CREDENTIAL", ownedAgent: true, toggleOn: true,
			wantStatus: http.StatusForbidden,
		},
		{
			name:    "no linked credential, toggle off: nothing refuses it",
			escType: "CREDENTIAL", ownedAgent: true,
			wantStatus: http.StatusOK,
		},
		{
			// credential_id points at a row that isn't there. The list's LEFT
			// JOIN yields NULL; resolve's lookup yields sql.ErrNoRows, which it
			// deliberately does NOT treat as the fail-closed case (only an
			// unreadable row is). Both must land on "no tier forces it".
			name:    "dangling credential_id, toggle off: neither side invents a tier",
			escType: "CREDENTIAL", credID: "fe2-ghost", ownedAgent: true,
			wantStatus: http.StatusOK,
		},
		{
			// The rule compares the approver against the agent's recorded owner.
			// With no owner there is nothing to compare and resolve proceeds, so
			// a row that warned about a refusal would be warning about a refusal
			// that cannot happen — the toggle AND the top tier are both on here
			// precisely to make that the only thing under test.
			name:    "no recorded owner: the rule cannot be enforced, so it is not claimed",
			escType: "CREDENTIAL", level: tierFloor, toggleOn: true, ownedAgent: false,
			wantStatus: http.StatusOK,
		},
		{
			name:    "TEXT escalation: out of scope even with the toggle on",
			escType: "TEXT", toggleOn: true, ownedAgent: true,
			wantStatus: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ownerID, wsID, crewID := seedFourEyesFixture(
				t, tc.escType, tc.level, tc.credID, tc.toggleOn, tc.ownedAgent)

			items := listEscalations(t, h, ownerID, wsID, crewID)
			if len(items) != 1 {
				t.Fatalf("got %d escalations, want 1", len(items))
			}
			listSaysBlocked := items[0].SecondApproverRequired

			// A TEXT escalation is answered with text; a CREDENTIAL one takes
			// none (#2376) — the value goes through supply.
			body := map[string]string{"action": "approve"}
			if tc.escType != "CREDENTIAL" {
				body["resolution"] = "granted"
			}
			rr := covEscResolve(h, ownerID, wsID, "fe2-esc", body)
			if rr.Code != tc.wantStatus {
				t.Fatalf("resolve status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if listSaysBlocked != (rr.Code == http.StatusForbidden) {
				t.Errorf("list said required=%v but the owner's resolve returned %d — the row lies about what will happen",
					listSaysBlocked, rr.Code)
			}
		})
	}
}

// TestListEscalations_FourEyesFollowsATierChange pins the one thing that makes
// the agreement above hold over time: the row is a LIVE read of both inputs,
// not a value frozen when the escalation was raised.
//
// Re-tiering a credential to the floor changes what resolve will do, and the
// list has to change with it. If the flags were ever stored on the escalation
// at raise time — an obvious-looking optimisation, since it saves a join — this
// is the case that would start lying, and it would lie in the dangerous
// direction: a row still showing an unguarded Approve for a credential that has
// since been marked critical.
func TestListEscalations_FourEyesFollowsATierChange(t *testing.T) {
	tierFloor, ok := keeper.MinSecondApproverLevel()
	if !ok {
		t.Fatal("no tier forces a second approver — the control this test exists for is gone, which is a failure, not a reason to skip")
	}
	h, ownerID, wsID, crewID := seedFourEyesFixture(
		t, "CREDENTIAL", keeper.SecurityLevels()[0], "", false, true)

	if got := listEscalations(t, h, ownerID, wsID, crewID)[0]; got.SecondApproverRequired {
		t.Fatalf("precondition: the lowest tier should need no second approver, got %+v", got)
	}

	execOrFatal(t, h.db, `UPDATE credentials SET security_level = ? WHERE id = 'fe2-cred'`, int(tierFloor))

	after := listEscalations(t, h, ownerID, wsID, crewID)[0]
	if !after.SecondApproverRequired || !after.SecondApproverByTier {
		t.Errorf("after re-tiering to %s the row still shows an unguarded approve: %+v", tierFloor, after)
	}
	if after.SecurityLevelLabel != tierFloor.Label() {
		t.Errorf("security_level_label = %q, want %q", after.SecurityLevelLabel, tierFloor.Label())
	}
	rr := covEscResolve(h, ownerID, wsID, "fe2-esc", map[string]string{
		"action": "approve",
	})
	if rr.Code != http.StatusForbidden {
		t.Errorf("resolve after re-tiering = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}
