package api

// cross_workspace_reference_test.go — the half of the fence that IDOR tests
// miss.
//
// #1471 and #1481 were not "fetch a row you don't own". They were "attach an id
// you don't own to a row you do": an assignee_id from another tenant written
// into a local issue, which the read path then resolved to a foreign user's
// full_name. Seven write paths carried that bug and two of them were found by
// an invariant rather than by review. The audit's §4 D3 (`crew_id` injection on
// POST /agents) is the same shape on a different column.
//
// A direct-object matrix cannot see this class. The attacker never asks for the
// foreign row; the server fetches it on their behalf because they named it in a
// request body. So this file drives the *write* paths and then asks the
// database a different question: after the call, does any row inside the
// attacker's workspace point at the victim's id?
//
// That last assertion is deliberately independent of the status code. A handler
// may reject the request outright (best), or silently drop the offending field
// (acceptable — nothing crossed), and both are a pass; only a persisted foreign
// reference is a breach. Status is recorded in the log so the two outcomes stay
// distinguishable to a reader.
//
// The tenants, the seeding, and the shared ids come from
// cross_workspace_fence_matrix_test.go — the two files test the two directions
// of the same fence.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// refCase is one foreign-key injection attempt.
type refCase struct {
	name string
	// what the request does, in one line, for the failure message.
	attack string
	method string
	// path and body may contain {key} (attacker's own id) and {vic:key}
	// (the victim's id — the thing being smuggled in).
	path string
	body string
	// linkTable/linkColumn/victimKey describe the persisted linkage that must
	// not exist afterwards. countScope is an extra SQL predicate (already
	// parameter-free) narrowing the count to rows the attacker could have
	// created — usually workspace_id.
	linkTable  string
	linkColumn string
	victimKey  string
	// scopeSQL is appended to the WHERE clause with the attacker's workspace id
	// bound as the second parameter. Empty means "no extra scope" — used for
	// join tables that have no workspace column, where the linkColumn match
	// alone is already proof.
	scopeSQL string
}

func refCases() []refCase {
	return []refCase{
		{
			name:       "agent_create_foreign_crew_id",
			attack:     "POST /agents naming a crew that belongs to the other tenant (audit §4 D3)",
			method:     "POST",
			path:       "/api/v1/agents",
			body:       `{"name":"Grafted","slug":"grafted-agent","crew_id":"{vic:crewId}"}`,
			linkTable:  "agents",
			linkColumn: "crew_id",
			victimKey:  "crewId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "issue_create_foreign_project_id",
			attack:     "POST /crews/{mine}/issues filing into the other tenant's project",
			method:     "POST",
			path:       "/api/v1/crews/{crewId}/issues",
			body:       `{"title":"grafted issue","project_id":"{vic:projectId}"}`,
			linkTable:  "missions",
			linkColumn: "project_id",
			victimKey:  "projectId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "issue_create_foreign_milestone_id",
			attack:     "POST /crews/{mine}/issues pointing at the other tenant's milestone",
			method:     "POST",
			path:       "/api/v1/crews/{crewId}/issues",
			body:       `{"title":"grafted milestone issue","milestone_id":"{vic:milestoneId}"}`,
			linkTable:  "missions",
			linkColumn: "milestone_id",
			victimKey:  "milestoneId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "issue_create_foreign_parent_issue_id",
			attack:     "POST /crews/{mine}/issues as a subtask of the other tenant's issue",
			method:     "POST",
			path:       "/api/v1/crews/{crewId}/issues",
			body:       `{"title":"grafted subtask","parent_issue_id":"{vic:issueId}"}`,
			linkTable:  "missions",
			linkColumn: "parent_issue_id",
			victimKey:  "issueId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "issue_create_foreign_routine_id",
			attack:     "POST /crews/{mine}/issues bound to the other tenant's routine",
			method:     "POST",
			path:       "/api/v1/crews/{crewId}/issues",
			body:       `{"title":"grafted routine issue","routine_id":"{vic:pipelineId}"}`,
			linkTable:  "missions",
			linkColumn: "routine_id",
			victimKey:  "pipelineId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "issue_create_foreign_assignee_id",
			attack:     "POST /crews/{mine}/issues assigned to the other tenant's user (#1471 anchor)",
			method:     "POST",
			path:       "/api/v1/crews/{crewId}/issues",
			body:       `{"title":"grafted assignee issue","assignee_type":"user","assignee_id":"{vic:userId}"}`,
			linkTable:  "missions",
			linkColumn: "assignee_id",
			victimKey:  "userId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "issue_update_foreign_project_id",
			attack:     "PATCH my own issue to move it into the other tenant's project",
			method:     "PATCH",
			path:       "/api/v1/crews/{crewId}/issues/{issueIdent}",
			body:       `{"project_id":"{vic:projectId}"}`,
			linkTable:  "missions",
			linkColumn: "project_id",
			victimKey:  "projectId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "issue_update_foreign_milestone_id",
			attack:     "PATCH my own issue onto the other tenant's milestone",
			method:     "PATCH",
			path:       "/api/v1/crews/{crewId}/issues/{issueIdent}",
			body:       `{"milestone_id":"{vic:milestoneId}"}`,
			linkTable:  "missions",
			linkColumn: "milestone_id",
			victimKey:  "milestoneId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "recurring_issue_create_foreign_crew_id",
			attack:     "POST /recurring-issues scheduled onto the other tenant's crew",
			method:     "POST",
			path:       "/api/v1/recurring-issues",
			body:       `{"crew_id":"{vic:crewId}","title":"grafted recurring","cron_expression":"0 8 * * *"}`,
			linkTable:  "recurring_issues",
			linkColumn: "crew_id",
			victimKey:  "crewId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "recurring_issue_create_foreign_project_id",
			attack:     "POST /recurring-issues filing into the other tenant's project",
			method:     "POST",
			path:       "/api/v1/recurring-issues",
			body:       `{"crew_id":"{crewId}","title":"grafted recurring proj","cron_expression":"0 8 * * *","project_id":"{vic:projectId}"}`,
			linkTable:  "recurring_issues",
			linkColumn: "project_id",
			victimKey:  "projectId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "triage_rule_create_foreign_crew_id",
			attack:     "POST /triage-rules routing matches into the other tenant's crew",
			method:     "POST",
			path:       "/api/v1/triage-rules",
			body:       `{"name":"grafted rule","pattern":"x","match_type":"contains","crew_id":"{vic:crewId}"}`,
			linkTable:  "triage_rules",
			linkColumn: "crew_id",
			victimKey:  "crewId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "triage_rule_create_foreign_project_id",
			attack:     "POST /triage-rules routing matches into the other tenant's project",
			method:     "POST",
			path:       "/api/v1/triage-rules",
			body:       `{"name":"grafted rule 2","pattern":"y","match_type":"contains","project_id":"{vic:projectId}"}`,
			linkTable:  "triage_rules",
			linkColumn: "project_id",
			victimKey:  "projectId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "recurring_issue_update_foreign_project_id",
			attack:     "PATCH my recurring template into the other tenant's project",
			method:     "PATCH",
			path:       "/api/v1/recurring-issues/{recurringId}",
			body:       `{"project_id":"{vic:projectId}"}`,
			linkTable:  "recurring_issues",
			linkColumn: "project_id",
			victimKey:  "projectId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "triage_rule_update_foreign_crew_id",
			attack:     "PATCH my triage rule to route into the other tenant's crew",
			method:     "PATCH",
			path:       "/api/v1/triage-rules/{ruleId}",
			body:       `{"crew_id":"{vic:crewId}"}`,
			linkTable:  "triage_rules",
			linkColumn: "crew_id",
			victimKey:  "crewId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "triage_rule_update_foreign_project_id",
			attack:     "PATCH my triage rule to file into the other tenant's project",
			method:     "PATCH",
			path:       "/api/v1/triage-rules/{ruleId}",
			body:       `{"project_id":"{vic:projectId}"}`,
			linkTable:  "triage_rules",
			linkColumn: "project_id",
			victimKey:  "projectId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "project_update_foreign_lead_id",
			attack:     "PATCH my project to be led by the other tenant's user",
			method:     "PATCH",
			path:       "/api/v1/projects/{projectId}",
			body:       `{"lead_type":"user","lead_id":"{vic:userId}"}`,
			linkTable:  "projects",
			linkColumn: "lead_id",
			victimKey:  "userId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "project_create_foreign_lead_id",
			attack:     "POST /projects led by the other tenant's user",
			method:     "POST",
			path:       "/api/v1/projects",
			body:       `{"name":"Grafted Project","slug":"grafted-project","lead_type":"user","lead_id":"{vic:userId}"}`,
			linkTable:  "projects",
			linkColumn: "lead_id",
			victimKey:  "userId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "credential_binding_foreign_credential",
			attack:     "POST /credentials/bindings mounting the other tenant's secret onto my agent",
			method:     "POST",
			path:       "/api/v1/credentials/bindings",
			body:       `{"credential_id":"{vic:credentialId}","scope":"AGENT","agent_id":"{agentId}","slot":"GRAFTED_TOKEN"}`,
			linkTable:  "credential_bindings",
			linkColumn: "credential_id",
			victimKey:  "credentialId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "credential_binding_foreign_agent",
			attack:     "POST /credentials/bindings mounting my secret onto the other tenant's agent",
			method:     "POST",
			path:       "/api/v1/credentials/bindings",
			body:       `{"credential_id":"{credentialId}","scope":"AGENT","agent_id":"{vic:agentId}","slot":"GRAFTED_TOKEN2"}`,
			linkTable:  "credential_bindings",
			linkColumn: "agent_id",
			victimKey:  "agentId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "agent_credential_assign_foreign_credential",
			attack:     "POST /agents/{mine}/credentials handing my agent the other tenant's secret",
			method:     "POST",
			path:       "/api/v1/agents/{agentId}/credentials",
			body:       `{"credential_id":"{vic:credentialId}","env_var_name":"GRAFTED_ENV"}`,
			linkTable:  "agent_credentials",
			linkColumn: "credential_id",
			victimKey:  "credentialId",
			// agent_credentials has no workspace column; a row linking the
			// victim's credential at all is already the breach.
			scopeSQL: "",
		},
		{
			name:       "crew_connection_to_foreign_crew",
			attack:     "POST /crew-connections wiring my crew to the other tenant's crew",
			method:     "POST",
			path:       "/api/v1/crew-connections",
			body:       `{"from_crew_id":"{crewId}","to_crew_id":"{vic:crewId}"}`,
			linkTable:  "crew_connections",
			linkColumn: "to_crew_id",
			victimKey:  "crewId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "crew_connection_from_foreign_crew",
			attack:     "POST /crew-connections wiring the other tenant's crew to mine",
			method:     "POST",
			path:       "/api/v1/crew-connections",
			body:       `{"from_crew_id":"{vic:crewId}","to_crew_id":"{crewId}"}`,
			linkTable:  "crew_connections",
			linkColumn: "from_crew_id",
			victimKey:  "crewId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "crew_member_add_foreign_user",
			attack:     "POST /crews/{mine}/members adding the other tenant's user to my crew",
			method:     "POST",
			path:       "/api/v1/crews/{crewId}/members",
			body:       `{"user_id":"{vic:userId}"}`,
			linkTable:  "crew_members",
			linkColumn: "user_id",
			victimKey:  "userId",
			scopeSQL:   "",
		},
		{
			name:       "notification_channel_pair_foreign_agent",
			attack:     "POST /notification-channels/{mine}/agents pairing the other tenant's agent",
			method:     "POST",
			path:       "/api/v1/notification-channels/{channelId}/agents",
			body:       `{"agent_id":"{vic:agentId}"}`,
			linkTable:  "notification_channel_agents",
			linkColumn: "agent_id",
			victimKey:  "agentId",
			scopeSQL:   "",
		},
		{
			name:       "pipeline_schedule_targets_foreign_routine",
			attack:     "POST /pipeline-schedules cronning the other tenant's routine",
			method:     "POST",
			path:       "/api/v1/workspaces/{wsSelf}/pipeline-schedules",
			body:       `{"name":"grafted schedule","target_pipeline_id":"{vic:pipelineId}","cron_expr":"0 8 * * *"}`,
			linkTable:  "pipeline_schedules",
			linkColumn: "target_pipeline_id",
			victimKey:  "pipelineId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "pipeline_schedule_targets_foreign_routine_by_slug",
			attack:     "POST /pipeline-schedules cronning the other tenant's routine by slug",
			method:     "POST",
			path:       "/api/v1/workspaces/{wsSelf}/pipeline-schedules",
			body:       `{"name":"grafted schedule slug","target_pipeline_slug":"{vic:pipelineSlug}","cron_expr":"0 9 * * *"}`,
			linkTable:  "pipeline_schedules",
			linkColumn: "target_pipeline_id",
			victimKey:  "pipelineId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "pipeline_webhook_targets_foreign_routine",
			attack:     "POST /pipeline-webhooks firing the other tenant's routine",
			method:     "POST",
			path:       "/api/v1/workspaces/{wsSelf}/pipeline-webhooks",
			body:       `{"name":"grafted webhook","target_pipeline_id":"{vic:pipelineId}"}`,
			linkTable:  "pipeline_webhooks",
			linkColumn: "target_pipeline_id",
			victimKey:  "pipelineId",
			scopeSQL:   "workspace_id = ?",
		},
		{
			name:       "checkpoint_on_foreign_mission",
			attack:     "POST /missions/{theirs}/checkpoints snapshotting the other tenant's mission",
			method:     "POST",
			path:       "/api/v1/missions/{vic:missionId}/checkpoints",
			body:       `{"label":"grafted checkpoint"}`,
			linkTable:  "checkpoints",
			linkColumn: "mission_id",
			victimKey:  "missionId",
			scopeSQL:   "workspace_id = ?",
		},
	}
}

// refFill resolves {key} against the attacker and {vic:key} against the victim.
func refFill(t *testing.T, s string, attacker, victim *fenceTenant) string {
	t.Helper()
	out := s
	if strings.Contains(out, "{wsSelf}") {
		out = strings.ReplaceAll(out, "{wsSelf}", attacker.wsID)
	}
	for key, id := range victim.ids {
		out = strings.ReplaceAll(out, "{vic:"+key+"}", id)
	}
	out = strings.ReplaceAll(out, "{vic:userId}", victim.userID)
	for key, id := range attacker.ids {
		out = strings.ReplaceAll(out, "{"+key+"}", id)
	}
	out = strings.ReplaceAll(out, "{userId}", attacker.userID)
	// Only our own {key} / {vic:key} form counts as unresolved — a JSON body
	// legitimately starts with '{'.
	if leftover := refPlaceholderRe.FindString(out); leftover != "" {
		t.Fatalf("unresolved placeholder %s in %q — the case and the tenant seeds disagree", leftover, s)
	}
	return out
}

// refPlaceholderRe matches the {key} / {vic:key} template form only.
var refPlaceholderRe = regexp.MustCompile(`\{(vic:)?[a-zA-Z][a-zA-Z0-9_]*\}`)

// TestCrossWorkspaceFence_ForeignReferences drives every write path that takes
// a foreign key from the request and asserts nothing in the attacker's
// workspace ends up pointing at the victim's row.
func TestCrossWorkspaceFence_ForeignReferences(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	attacker := fenceSeedTenant(t, db, "a")
	victim := fenceSeedTenant(t, db, "b")
	attacker.ids["userId"] = attacker.userID
	victim.ids["userId"] = victim.userID

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithOutputBasePath(t.TempDir()))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	r.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	r.PipelinesHandler.SetWebhookStore(pipeline.NewWebhookStore(db))

	cases := refCases()
	if len(cases) < 15 {
		t.Fatalf("only %d reference cases — the table collapsed (vacuous pass guard)", len(cases))
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			path := refFill(t, c.path, attacker, victim)
			body := refFill(t, c.body, attacker, victim)

			url := path + "?workspace_id=" + attacker.wsID
			req := httptest.NewRequest(c.method, url, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+attacker.token)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)

			if rr.Code == http.StatusServiceUnavailable &&
				(strings.Contains(rr.Body.String(), "not wired") || strings.Contains(rr.Body.String(), "not configured")) {
				t.Fatalf("VACUOUS: %s %s answered 503 %q before the write path ran — wire that backend here rather than counting the 503 as a rejection",
					c.method, path, fenceTrim(rr.Body.String()))
			}

			victimID, ok := victim.ids[c.victimKey]
			if c.victimKey == "userId" {
				victimID, ok = victim.userID, true
			}
			if !ok || victimID == "" {
				t.Fatalf("case %s names victim key %q, which the tenant seeds never set", c.name, c.victimKey)
			}

			n := refLinkCount(t, db, c.linkTable, c.linkColumn, victimID, c.scopeSQL, attacker.wsID)
			if n > 0 {
				t.Fatalf("LEAKED: %s — %s persisted %d row(s) in %s.%s pointing at the other tenant's %s (%s). status=%d body=%s",
					c.name, c.attack, n, c.linkTable, c.linkColumn, c.victimKey, victimID,
					rr.Code, fenceTrim(rr.Body.String()))
			}
			outcome := "rejected"
			if rr.Code >= 200 && rr.Code < 300 {
				outcome = "accepted, foreign reference dropped"
			}
			// The body is logged on the happy path too, deliberately: a 4xx for
			// an unrelated reason (a malformed fixture, a missing required
			// field) would otherwise read as "the fence held" when in truth the
			// write path was never reached.
			t.Logf("held (%s, status %d): %s -- %s", outcome, rr.Code, c.attack, fenceTrim(rr.Body.String()))
		})
	}
}

// refLinkCount counts rows in the attacker's own scope that reference victimID.
func refLinkCount(t *testing.T, db *sql.DB, table, column, victimID, scopeSQL, attackerWS string) int {
	t.Helper()
	// #nosec G202 -- table/column/scopeSQL come from the in-file case table, never from input.
	q := "SELECT COUNT(*) FROM " + table + " WHERE " + column + " = ?"
	args := []any{victimID}
	if scopeSQL != "" {
		q += " AND " + scopeSQL
		args = append(args, attackerWS)
	}
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %s.%s: %v", table, column, err)
	}
	return n
}
