package api

// inbox_foureyes_test.go — issue #1574.
//
// #1559 taught the crew escalations panel to say that a second approver is
// required, and which of the two controls demands it. The inbox was not
// touched, so the other surface with a one-click Approve on the same
// escalation still rendered it as if a single person could close it, and the
// operator learned otherwise from the 403.
//
// The inbox row is written from a payload snapshot at raise time, and both
// inputs to the answer change afterwards — the workspace toggle and the
// credential's tier. So the interesting property is not "the field is
// present": it is that the field agrees with what /escalations/{id}/resolve
// will actually do at the moment the row is read. These tests assert exactly
// that, the same way the escalation list's agreement test does, and pin the
// two cases where a stored answer would drift.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// inboxFourEyesRow decodes only the fields under test. Declared here rather
// than reusing inboxItemResponse deliberately: this is the WIRE contract the
// console reads, and a test that decodes the server's own struct would keep
// passing if the json tags were renamed out from under the UI.
type inboxFourEyesRow struct {
	ID                        string `json:"id"`
	Kind                      string `json:"kind"`
	SourceID                  string `json:"source_id"`
	SecondApproverRequired    bool   `json:"second_approver_required"`
	SecondApproverByWorkspace bool   `json:"second_approver_by_workspace"`
	SecondApproverByTier      bool   `json:"second_approver_by_tier"`
	SecurityLevelLabel        string `json:"security_level_label"`
}

func listInbox(t *testing.T, h *InboxHandler, userID, wsID string) []inboxFourEyesRow {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/inbox", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("inbox list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Rows []inboxFourEyesRow `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode inbox list: %v (%s)", err, rr.Body.String())
	}
	return out.Rows
}

func getInboxItem(t *testing.T, h *InboxHandler, userID, wsID, id string) inboxFourEyesRow {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/inbox/"+id, nil), userID, wsID, "OWNER")
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("inbox get status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out inboxFourEyesRow
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode inbox get: %v (%s)", err, rr.Body.String())
	}
	return out
}

const (
	inboxFourEyesEscID  = "ife-esc"
	inboxFourEyesRowID  = "ife-inbox"
	inboxFourEyesCredID = "ife-cred"
)

// seedInboxFourEyesFixture builds the pair the issue is about: one PENDING
// escalation and the inbox_items projection written beside it at raise time.
// level 0 links no credential row; credIDOverride links a credential_id with
// no row behind it.
func seedInboxFourEyesFixture(t *testing.T, escType string, level keeper.SecurityLevel,
	credIDOverride string, toggleOn, ownedAgent bool) (*InboxHandler, *QueryHandler, string, string) {
	t.Helper()
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	crewID := seedCrewRow(t, db, "ife-crew", wsID, "Crew", "ife-crew")

	agentID := "ife-agent"
	if ownedAgent {
		seedOwnedAgent(t, db, agentID, wsID, crewID, ownerID)
	} else {
		execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug)
			VALUES (?, ?, ?, 'Agent', ?)`, agentID, wsID, crewID, agentID)
	}

	credID := credIDOverride
	if credID == "" && level != 0 {
		credID = inboxFourEyesCredID
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
		VALUES (?, ?, ?, 'ife-chat', ?, 'need a key', ?, ?, 'PENDING', datetime('now'))`,
		inboxFourEyesEscID, wsID, crewID, agentID, escType, credArg)

	// The projection, exactly as CreateEscalation writes it: a payload frozen
	// at raise time, carrying the escalation type and whether a credential is
	// already waiting in the vault — and nothing about four-eyes, because at
	// raise time nobody can know what the answer will be when it is read.
	payload := fmt.Sprintf(`{"escalation_type":%q,"has_pending_credential":%t,"crew_id":%q}`,
		escType, credID != "", crewID)
	execOrFatal(t, db, `INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, target_role, title, body_md,
		 sender_type, sender_id, sender_name, state, priority, blocking,
		 payload_json, created_at, updated_at)
		VALUES (?, ?, 'escalation', ?, 'MANAGER', 'Credential approval: Deploy Key', '',
		 'agent', ?, ?, 'unread', 'high', 1, ?, datetime('now'), datetime('now'))`,
		inboxFourEyesRowID, wsID, inboxFourEyesEscID, agentID, agentID, payload)

	if toggleOn {
		if err := governance.Upsert(context.Background(), db, wsID,
			governance.Settings{RequireSecondApprover: true}, ownerID); err != nil {
			t.Fatalf("enable require_second_approver: %v", err)
		}
	}

	return NewInboxHandler(db, newTestLogger(), nil),
		NewQueryHandler(db, nil, nil, "", newTestLogger()), ownerID, wsID
}

// TestInboxList_FourEyesMatchesResolve is the point of #1574: whatever the
// inbox row claims about a second approver, the resolve endpoint the row's
// own Approve button calls must return the matching status. Asserting the two
// against each other is what stops the inbox from drifting away from the
// escalations panel, or from the enforcement, when either is edited.
//
// The cases are the ones where a read-time answer could plausibly diverge from
// what ResolveEscalation decides: a dangling credential_id (the join yields
// NULL where the resolve lookup yields sql.ErrNoRows), a CREDENTIAL escalation
// with no credential at all (the legacy flow, where the toggle is the whole
// rule), a non-CREDENTIAL type, and an agent with no recorded owner — the
// identity the rule compares against, and therefore the case where it cannot
// be enforced and the row must claim nothing.
func TestInboxList_FourEyesMatchesResolve(t *testing.T) {
	tierFloor, ok := keeper.MinSecondApproverLevel()
	if !ok {
		t.Fatal("no tier forces a second approver — the control this test exists for is gone, which is a failure, not a reason to skip")
	}
	lowest := keeper.SecurityLevels()[0]

	for _, tc := range []struct {
		name          string
		escType       string
		level         keeper.SecurityLevel
		credID        string // credential_id with no credentials row behind it
		toggleOn      bool
		ownedAgent    bool
		wantStatus    int
		wantWorkspace bool
		wantTier      bool
		wantLabel     string
	}{
		{
			name:    "tier floor, toggle off: the tier alone refuses it",
			escType: "CREDENTIAL", level: tierFloor, ownedAgent: true,
			wantStatus: http.StatusForbidden, wantTier: true, wantLabel: tierFloor.Label(),
		},
		{
			name:    "lowest tier, toggle off: nothing refuses it",
			escType: "CREDENTIAL", level: lowest, ownedAgent: true,
			wantStatus: http.StatusOK, wantLabel: lowest.Label(),
		},
		{
			name:    "lowest tier, toggle on: the workspace refuses it",
			escType: "CREDENTIAL", level: lowest, toggleOn: true, ownedAgent: true,
			wantStatus: http.StatusForbidden, wantWorkspace: true, wantLabel: lowest.Label(),
		},
		{
			name:    "no linked credential, toggle on: the workspace still refuses it",
			escType: "CREDENTIAL", ownedAgent: true, toggleOn: true,
			wantStatus: http.StatusForbidden, wantWorkspace: true,
		},
		{
			name:    "no linked credential, toggle off: nothing refuses it",
			escType: "CREDENTIAL", ownedAgent: true,
			wantStatus: http.StatusOK,
		},
		{
			name:    "dangling credential_id, toggle off: neither side invents a tier",
			escType: "CREDENTIAL", credID: "ife-ghost", ownedAgent: true,
			wantStatus: http.StatusOK,
		},
		{
			// The rule compares the approver against agents.created_by_user_id.
			// With no recorded owner there is nothing to compare, resolve goes
			// through, and a row warning about a refusal would be warning about
			// a refusal that cannot happen. The toggle AND the top tier are both
			// on here precisely so that is the only thing under test.
			name:    "no recorded owner: the rule cannot be enforced, so it is not claimed",
			escType: "CREDENTIAL", level: tierFloor, toggleOn: true, ownedAgent: false,
			wantStatus: http.StatusOK, wantLabel: tierFloor.Label(),
		},
		{
			name:    "TEXT escalation: out of scope even with the toggle on",
			escType: "TEXT", toggleOn: true, ownedAgent: true,
			wantStatus: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ih, qh, ownerID, wsID := seedInboxFourEyesFixture(
				t, tc.escType, tc.level, tc.credID, tc.toggleOn, tc.ownedAgent)

			rows := listInbox(t, ih, ownerID, wsID)
			if len(rows) != 1 {
				t.Fatalf("got %d inbox rows, want 1", len(rows))
			}
			row := rows[0]
			wantRequired := tc.wantStatus == http.StatusForbidden
			if row.SecondApproverByWorkspace != tc.wantWorkspace {
				t.Errorf("second_approver_by_workspace = %v, want %v", row.SecondApproverByWorkspace, tc.wantWorkspace)
			}
			if row.SecondApproverByTier != tc.wantTier {
				t.Errorf("second_approver_by_tier = %v, want %v", row.SecondApproverByTier, tc.wantTier)
			}
			if row.SecurityLevelLabel != tc.wantLabel {
				t.Errorf("security_level_label = %q, want %q", row.SecurityLevelLabel, tc.wantLabel)
			}
			// GET /inbox/{id} is the same row read one at a time — the CLI's
			// `inbox get` and the deep-linked pane both come through it, so it
			// must not be the surface that still says nothing.
			if one := getInboxItem(t, ih, ownerID, wsID, inboxFourEyesRowID); one.SecondApproverRequired != row.SecondApproverRequired {
				t.Errorf("inbox get says required=%v but the list says %v", one.SecondApproverRequired, row.SecondApproverRequired)
			}

			rr := covEscResolve(qh, ownerID, wsID, inboxFourEyesEscID, map[string]string{
				"resolution": "granted", "action": "approve",
			})
			if rr.Code != tc.wantStatus {
				t.Fatalf("resolve status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if row.SecondApproverRequired != (rr.Code == http.StatusForbidden) {
				t.Errorf("inbox row said required=%v but the owner's resolve returned %d — the row lies about what will happen",
					row.SecondApproverRequired, rr.Code)
			}
			if row.SecondApproverRequired != wantRequired {
				t.Errorf("second_approver_required = %v, want %v", row.SecondApproverRequired, wantRequired)
			}
		})
	}
}

// TestInboxList_FourEyesFollowsATierChange is why this could not be solved by
// enriching the stored payload, which is where the inbox gets everything else
// it renders.
//
// The row is written once, when the escalation is raised. Re-tiering the
// credential afterwards changes what resolve will do; a payload written before
// that cannot know. And it goes stale in the dangerous direction — an inbox
// row still offering an unguarded one-click Approve for a credential somebody
// has since marked critical.
func TestInboxList_FourEyesFollowsATierChange(t *testing.T) {
	tierFloor, ok := keeper.MinSecondApproverLevel()
	if !ok {
		t.Fatal("no tier forces a second approver — the control this test exists for is gone, which is a failure, not a reason to skip")
	}
	ih, qh, ownerID, wsID := seedInboxFourEyesFixture(
		t, "CREDENTIAL", keeper.SecurityLevels()[0], "", false, true)

	if got := listInbox(t, ih, ownerID, wsID)[0]; got.SecondApproverRequired {
		t.Fatalf("precondition: the lowest tier should need no second approver, got %+v", got)
	}

	execOrFatal(t, ih.db, `UPDATE credentials SET security_level = ? WHERE id = ?`,
		int(tierFloor), inboxFourEyesCredID)

	after := listInbox(t, ih, ownerID, wsID)[0]
	if !after.SecondApproverRequired || !after.SecondApproverByTier {
		t.Errorf("after re-tiering to %s the inbox still offers an unguarded Approve: %+v", tierFloor, after)
	}
	if after.SecurityLevelLabel != tierFloor.Label() {
		t.Errorf("security_level_label = %q, want %q", after.SecurityLevelLabel, tierFloor.Label())
	}
	rr := covEscResolve(qh, ownerID, wsID, inboxFourEyesEscID, map[string]string{
		"resolution": "granted", "action": "approve",
	})
	if rr.Code != http.StatusForbidden {
		t.Errorf("resolve after re-tiering = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// TestInboxList_FourEyesFollowsTheWorkspaceToggle is the other input that
// moves after the row is written, and the one an operator changes far more
// often than a credential's tier.
func TestInboxList_FourEyesFollowsTheWorkspaceToggle(t *testing.T) {
	ih, qh, ownerID, wsID := seedInboxFourEyesFixture(
		t, "CREDENTIAL", keeper.SecurityLevels()[0], "", false, true)

	if got := listInbox(t, ih, ownerID, wsID)[0]; got.SecondApproverRequired {
		t.Fatalf("precondition: the toggle is off, got %+v", got)
	}
	if err := governance.Upsert(context.Background(), ih.db, wsID,
		governance.Settings{RequireSecondApprover: true}, ownerID); err != nil {
		t.Fatalf("enable require_second_approver: %v", err)
	}

	after := listInbox(t, ih, ownerID, wsID)[0]
	if !after.SecondApproverRequired || !after.SecondApproverByWorkspace {
		t.Errorf("after enabling the workspace toggle the inbox still offers an unguarded Approve: %+v", after)
	}
	rr := covEscResolve(qh, ownerID, wsID, inboxFourEyesEscID, map[string]string{
		"resolution": "granted", "action": "approve",
	})
	if rr.Code != http.StatusForbidden {
		t.Errorf("resolve after enabling the toggle = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// TestInboxList_FourEyesLeavesOtherKindsAlone: the enrichment joins on
// source_id, and an inbox row of another kind carries a source_id that is not
// an escalations.id — a waitpoint token, a run id. Those must not pick up a
// claim because some escalation happens to share the string, and the rows must
// still list at all when there is no escalation to enrich.
func TestInboxList_FourEyesLeavesOtherKindsAlone(t *testing.T) {
	ih, _, ownerID, wsID := seedInboxFourEyesFixture(
		t, "CREDENTIAL", keeper.SecurityLevels()[0], "", true, true)

	// A message row whose source_id collides with the escalation id.
	execOrFatal(t, ih.db, `INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, title, body_md, sender_type, sender_id, sender_name,
		 state, priority, blocking, payload_json, created_at, updated_at)
		VALUES ('ife-msg', ?, 'message', ?, 'Atlas replied', '', 'agent', 'ife-agent', 'ife-agent',
		 'unread', 'low', 0, '{}', datetime('now'), datetime('now'))`,
		wsID, inboxFourEyesEscID)

	for _, row := range listInbox(t, ih, ownerID, wsID) {
		if row.Kind == "message" && (row.SecondApproverRequired || row.SecurityLevelLabel != "") {
			t.Errorf("a message row picked up a four-eyes claim from a colliding source_id: %+v", row)
		}
	}
}

// A KEEPER credential escalation carries a keeper_requests id in source_id, not
// an escalations id — it has no backing escalations row at all (see corpus.go on
// humanInboxSQL, and keeper_request.go which writes the item directly). So the
// enrichment's `FROM escalations e WHERE e.id IN (…)` matches nothing for it, and
// every four-eyes field stays false.
//
// That did not matter while the card had no buttons. #1671 gave it Approve and
// Deny, and the notice #1574 added to the inbox *specifically so an operator
// would not meet the 403 cold* went on rendering nothing — because it was never
// fed. On dev2 an OWNER pressed Approve on an L4 request and got a refusal the
// card had given no hint of.
//
// The rule itself is right, and so is the RBAC: `manage` is OWNER **or** ADMIN,
// and an ADMIN who does not own the agent can approve. What was wrong is that
// the card promised something it could not deliver to the person reading it.
func seedKeeperInboxFourEyes(t *testing.T, level keeper.SecurityLevel, toggleOn bool) (*InboxHandler, string, string) {
	t.Helper()
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	crewID := seedCrewRow(t, db, "kife-crew", wsID, "Crew", "kife-crew")
	agentID := "kife-agent"
	seedOwnedAgent(t, db, agentID, wsID, crewID, ownerID)

	execOrFatal(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, security_level, status, created_by)
		VALUES ('kife-cred', ?, 'PROD_DB_ADMIN', 'v1:aW52YWxpZA==', 'SECRET', 'NONE', 'WORKSPACE', ?, 'ACTIVE', ?)`,
		wsID, int(level), ownerID)

	// The keeper's own row: no escalations row anywhere, which is the point.
	execOrFatal(t, db, `INSERT INTO keeper_requests
		(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES ('kife-kr', 'access', ?, ?, 'kife-cred', 'migrate the orders table', 'ESCALATE')`,
		agentID, crewID)

	execOrFatal(t, db, `INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, target_role, title, body_md,
		 sender_type, sender_id, sender_name, state, priority, blocking,
		 payload_json, created_at, updated_at)
		VALUES ('kife-inbox', ?, 'escalation', 'kife-kr', 'ADMIN',
		 'Keeper escalation: Agent requested PROD_DB_ADMIN', '',
		 'agent', ?, ?, 'unread', 'high', 1,
		 '{"request_type":"access","request_id":"kife-kr"}', datetime('now'), datetime('now'))`,
		wsID, agentID, agentID)

	if toggleOn {
		if err := governance.Upsert(context.Background(), db, wsID,
			governance.Settings{RequireSecondApprover: true}, ownerID); err != nil {
			t.Fatalf("enable require_second_approver: %v", err)
		}
	}
	return NewInboxHandler(db, newTestLogger(), nil), ownerID, wsID
}

// L4 forces the rule whatever the workspace toggle says, so the card must warn
// even on an instance that has deliberately switched second approvers off.
func TestInboxList_KeeperEscalationCarriesFourEyes(t *testing.T) {
	h, ownerID, wsID := seedKeeperInboxFourEyes(t, keeper.SecurityLevelL4, false)

	for _, row := range listInbox(t, h, ownerID, wsID) {
		if row.SourceID != "kife-kr" {
			continue
		}
		if !row.SecondApproverRequired {
			t.Error("the keeper escalation claims no second approver is needed; " +
				"the card offers Approve and the server will refuse the agent's owner")
		}
		if !row.SecondApproverByTier {
			t.Error("by_tier is false for an L4 credential — the tier forces the rule " +
				"regardless of the workspace toggle, and that is the half the operator cannot switch off")
		}
		if row.SecurityLevelLabel == "" {
			t.Error("no tier label, so the notice cannot say WHICH tier is demanding it")
		}
		return
	}
	t.Fatal("the keeper escalation is not in the inbox at all")
}

// A low-tier keeper request must claim nothing: warning where no refusal is
// coming trains the operator to ignore the warning that matters.
func TestInboxList_KeeperEscalationBelowTheFloorClaimsNothing(t *testing.T) {
	h, ownerID, wsID := seedKeeperInboxFourEyes(t, keeper.SecurityLevelL1, false)

	for _, row := range listInbox(t, h, ownerID, wsID) {
		if row.SourceID != "kife-kr" {
			continue
		}
		if row.SecondApproverRequired {
			t.Error("an L1 keeper request threatens a second approver that will never be demanded")
		}
		return
	}
	t.Fatal("the keeper escalation is not in the inbox at all")
}

// And the workspace toggle reaches keeper requests too, at any tier.
func TestInboxList_KeeperEscalationHonoursTheWorkspaceToggle(t *testing.T) {
	h, ownerID, wsID := seedKeeperInboxFourEyes(t, keeper.SecurityLevelL1, true)

	for _, row := range listInbox(t, h, ownerID, wsID) {
		if row.SourceID != "kife-kr" {
			continue
		}
		if !row.SecondApproverByWorkspace || !row.SecondApproverRequired {
			t.Error("the workspace toggle is on and the row does not say so")
		}
		return
	}
	t.Fatal("the keeper escalation is not in the inbox at all")
}

// The enrichment query just grew a second branch, and a widened query is where
// tenant scoping gets lost. keeper_requests has no workspace_id column of its
// own, so the scope comes from the requesting agent's — and a mistake there
// would let one tenant's inbox row take its tier and its agent-owner from
// another tenant's request.
func TestInboxList_KeeperFourEyesDoesNotCrossTenants(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	crewID := seedCrewRow(t, db, "xt-crew", wsID, "Crew", "xt-crew")
	agentID := "xt-agent"
	seedOwnedAgent(t, db, agentID, wsID, crewID, ownerID)

	// Another tenant, holding an L4 request whose id the victim's row names.
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES ('xt-owner', 'xt@ex.com', 'XT')`)
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('xt-ws', 'Other', 'other')`)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('xt-crew2', 'xt-ws', 'C', 'c')`)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, created_by_user_id)
		VALUES ('xt-agent2', 'xt-ws', 'xt-crew2', 'A', 'a', 'xt-owner')`)
	execOrFatal(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, security_level, status, created_by)
		VALUES ('xt-cred', 'xt-ws', 'PROD', 'v1:aW52YWxpZA==', 'SECRET', 'NONE', 'WORKSPACE', 4, 'ACTIVE', 'xt-owner')`)
	execOrFatal(t, db, `INSERT INTO keeper_requests
		(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES ('xt-kr', 'access', 'xt-agent2', 'xt-crew2', 'xt-cred', 'x', 'ESCALATE')`)

	// The victim's inbox row points at the OTHER tenant's request id.
	execOrFatal(t, db, `INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, target_role, title, body_md,
		 sender_type, sender_id, sender_name, state, priority, blocking,
		 payload_json, created_at, updated_at)
		VALUES ('xt-inbox', ?, 'escalation', 'xt-kr', 'ADMIN', 'Keeper escalation', '',
		 'agent', ?, ?, 'unread', 'high', 1, '{"request_type":"access"}',
		 datetime('now'), datetime('now'))`, wsID, agentID, agentID)

	h := NewInboxHandler(db, newTestLogger(), nil)
	for _, row := range listInbox(t, h, ownerID, wsID) {
		if row.SourceID != "xt-kr" {
			continue
		}
		if row.SecondApproverRequired || row.SecondApproverByTier || row.SecurityLevelLabel != "" {
			t.Fatalf("another tenant's L4 request leaked into this row: required=%v by_tier=%v label=%q",
				row.SecondApproverRequired, row.SecondApproverByTier, row.SecurityLevelLabel)
		}
		return
	}
	t.Fatal("the row is not in the inbox at all")
}
