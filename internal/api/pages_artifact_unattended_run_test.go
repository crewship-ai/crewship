package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifactrun"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/journal"
)

// #5317 step 5 (#5339): a run without a human (routine, webhook, scheduled)
// reaches artifacts only through a run the host recorded, with a capability
// the server minted for it, and only through the agent's own standing access.

const unattendedRunMaster = "synthetic-artifact-run-scope-master"

type unattendedRun struct {
	runID, agentID, token, capability string
}

// unattended starts a run without a human for agent (journal run.started via
// the internal run record, no human authority) and records it as the host
// does before the agent starts.
func (g *runScopeRig) unattended(t *testing.T, runID, agentID, trigger string) unattendedRun {
	t.Helper()
	writer := journal.NewWriter(g.f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-unattended-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	h := NewInternalHandler(g.f.rig.db, unattendedRunMaster, newTestLogger())
	h.SetJournal(writer)
	raw, _ := json.Marshal(map[string]any{"id": runID, "workspace_id": g.f.a.WorkspaceID, "agent_id": agentID, "trigger_type": trigger})
	w := httptest.NewRecorder()
	h.CreateRun(w, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)))
	if w.Code != 201 {
		t.Fatalf("unattended run record: %d %s", w.Code, w.Body)
	}
	capability, err := artifactrun.Record(t.Context(), g.f.rig.db, artifactrun.Run{WorkspaceID: g.f.a.WorkspaceID, CrewID: g.f.a.CrewID, AgentID: agentID, RunID: runID, Trigger: trigger, Source: "test-source"}, time.Hour)
	if err != nil {
		t.Fatal("record unattended run", err)
	}
	key := internaltoken.DeriveAgentRunKey(unattendedRunMaster, g.f.a.WorkspaceID, g.f.a.CrewID)
	return unattendedRun{runID: runID, agentID: agentID, token: internaltoken.DeriveAgentRunToken(key, g.f.a.WorkspaceID, agentID, runID), capability: capability}
}

func (g *runScopeRig) toolAs(token, capability, operation, slug, payload string) *httptest.ResponseRecorder {
	raw := fmt.Sprintf(`{"operation":%q,"slug":%q,"payload":%s}`, operation, slug, payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/pages/artifact-tool", bytes.NewReader([]byte(raw)))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Internal-Token", g.crewTok)
	req.Header.Set("Content-Type", "application/json")
	if capability != "" {
		req.Header.Set(artifactrun.Header, capability)
	}
	w := httptest.NewRecorder()
	g.router.ServeHTTP(w, req)
	return w
}

func (u unattendedRun) tool(g *runScopeRig, operation, slug, payload string) *httptest.ResponseRecorder {
	return g.toolAs(u.token, u.capability, operation, slug, payload)
}

// standing creates an artifact owned by the run's human-free owner (the
// fixture's human) and grants the agent level on it, issued by issuer.
func (g *runScopeRig) standing(t *testing.T, slug, agent, level, issuer string) string {
	t.Helper()
	id := g.create(t, g.f.a.HumanUserID, "MEMBER", slug)
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, id)
	mustExec(t, g.f.rig.db, `UPDATE artifact_version_heads SET shared_version=latest_version WHERE page_id=?`, id)
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,?,?,?,?)`, id, "agent", agent, level, issuer)
	return id
}

func sameRefusal(t *testing.T, what string, got, missing *httptest.ResponseRecorder) {
	t.Helper()
	if got.Code == 200 || got.Code == 201 {
		t.Fatalf("%s succeeded: %s", what, got.Body)
	}
	if got.Code != missing.Code || got.Body.String() != missing.Body.String() {
		t.Fatalf("%s: refusal differs from a missing artifact: %d %q vs %d %q", what, got.Code, got.Body, missing.Code, missing.Body)
	}
}

type unattendedAuditRow struct {
	User                                          *string
	Page, Agent, Crew, Run, Reach, Operation, Trg string
	Chat                                          string
}

func unattendedAuditRows(t *testing.T, g *runScopeRig) []unattendedAuditRow {
	t.Helper()
	rows, err := g.f.rig.db.Query(`SELECT user_id,coalesce(entity_id,''),coalesce(metadata,'') FROM audit_logs WHERE workspace_id=? AND action=? AND entity_type='page' ORDER BY created_at,id`, g.f.a.WorkspaceID, auditActionAgentRunArtifactAccess)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []unattendedAuditRow
	for rows.Next() {
		var r unattendedAuditRow
		var meta string
		if err := rows.Scan(&r.User, &r.Page, &meta); err != nil {
			t.Fatal(err)
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(meta), &m); err != nil {
			t.Fatalf("audit metadata %q: %v", meta, err)
		}
		r.Agent, r.Crew, r.Run, r.Reach, r.Operation, r.Trg, r.Chat = m["agent_id"], m["crew_id"], m["run_id"], m["reach"], m["operation"], m["trigger"], m["chat_id"]
		out = append(out, r)
	}
	return out
}

// Baseline: the agent's own standing grant reaches the artifact, and the first
// read is audited with no human (user_id NULL), naming run, agent, crew and
// trigger.
func TestUnattendedRun_ReadsThroughItsOwnStandingGrant(t *testing.T) {
	g := newRunScopeRig(t)
	id := g.standing(t, "ua-read", g.f.a.AgentID, "read", g.f.rig.adminID)
	u := g.unattended(t, "ua-run-read", g.f.a.AgentID, "WEBHOOK")
	for i := 0; i < 2; i++ {
		if w := u.tool(g, "artifact_versions", "ua-read", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), "ua-read") {
			t.Fatalf("read %d: %d %s", i, w.Code, w.Body)
		}
	}
	rows := unattendedAuditRows(t, g)
	if len(rows) != 1 {
		t.Fatalf("want one read audit row, got %+v", rows)
	}
	r := rows[0]
	if r.User != nil || r.Page != id || r.Agent != g.f.a.AgentID || r.Crew != g.f.a.CrewID || r.Run != u.runID || r.Reach != "standing" || r.Operation != "read" || r.Trg != "WEBHOOK" || r.Chat != "" {
		t.Fatalf("audit row %+v (user %v)", r, r.User)
	}
}

// A valid run token for a run id the host never recorded is refused, with or
// without a capability-shaped header.
func TestUnattendedRun_UnrecordedRunIsRefused(t *testing.T) {
	g := newRunScopeRig(t)
	g.standing(t, "ua-unrecorded", g.f.a.AgentID, "read", g.f.rig.adminID)
	recorded := g.unattended(t, "ua-run-recorded", g.f.a.AgentID, "ROUTINE")
	key := internaltoken.DeriveAgentRunKey(unattendedRunMaster, g.f.a.WorkspaceID, g.f.a.CrewID)
	fresh := internaltoken.DeriveAgentRunToken(key, g.f.a.WorkspaceID, g.f.a.AgentID, "ua-run-fresh")
	for name, capability := range map[string]string{"none": "", "other run's": recorded.capability, "forged": artifactrun.Prefix + strings.Repeat("ab", 32)} {
		if w := g.toolAs(fresh, capability, "artifact_versions", "ua-unrecorded", `{}`); w.Code == 200 {
			t.Fatalf("unrecorded run with %s capability read: %s", name, w.Body)
		}
	}
}

// Agent A's recorded run does not act as agent B of the same crew: B's token
// with A's run id and A's capability is refused, and so is A's token on B's
// grant.
func TestUnattendedRun_AgentAIsNotAgentB(t *testing.T) {
	g := newRunScopeRig(t)
	other := seedAgentRow(t, g.f.rig.db, "ua-agent-b", g.f.a.WorkspaceID, g.f.a.CrewID, "Agent B", "ua-agent-b", "AGENT")
	g.standing(t, "ua-b-only", other, "read", g.f.rig.adminID)
	a := g.unattended(t, "ua-run-a", g.f.a.AgentID, "WEBHOOK")
	key := internaltoken.DeriveAgentRunKey(unattendedRunMaster, g.f.a.WorkspaceID, g.f.a.CrewID)
	asB := internaltoken.DeriveAgentRunToken(key, g.f.a.WorkspaceID, other, a.runID)
	if w := g.toolAs(asB, a.capability, "artifact_versions", "ua-b-only", `{}`); w.Code == 200 {
		t.Fatalf("agent B acted on A's recorded run: %s", w.Body)
	}
	sameRefusal(t, "A on B's grant", a.tool(g, "artifact_versions", "ua-b-only", `{}`), a.tool(g, "artifact_versions", "ua-nowhere", `{}`))
}

// The run token a container can derive (it holds the crew run key) is not
// enough: without the host-minted capability, or with one derived from the
// run key, the recorded run is refused.
func TestUnattendedRun_ContainerDerivedCredentialIsRefused(t *testing.T) {
	g := newRunScopeRig(t)
	g.standing(t, "ua-derived", g.f.a.AgentID, "read", g.f.rig.adminID)
	u := g.unattended(t, "ua-run-derived", g.f.a.AgentID, "SCHEDULED")
	key := internaltoken.DeriveAgentRunKey(unattendedRunMaster, g.f.a.WorkspaceID, g.f.a.CrewID)
	derived := internaltoken.DeriveAgentRunToken(key, g.f.a.WorkspaceID, g.f.a.AgentID, u.runID)
	for name, capability := range map[string]string{
		"no capability":           "",
		"run token as capability": derived,
		"key-derived":             artifactrun.Prefix + strings.TrimPrefix(internaltoken.DeriveAgentRunToken(key, g.f.a.WorkspaceID, g.f.a.AgentID, u.runID+"-cap"), "agtv2.")[:64],
	} {
		if w := g.toolAs(derived, capability, "artifact_versions", "ua-derived", `{}`); w.Code == 200 {
			t.Fatalf("%s: a container-derived credential read: %s", name, w.Body)
		}
	}
	if w := u.tool(g, "artifact_versions", "ua-derived", `{}`); w.Code != 200 {
		t.Fatalf("baseline host capability: %d %s", w.Code, w.Body)
	}
}

// The token ends with the run: after End, or once the run's terminal journal
// entry exists, or past its expiry, nothing is reachable.
func TestUnattendedRun_EndsWithTheRun(t *testing.T) {
	for name, end := range map[string]func(*runScopeRig, unattendedRun){
		"ended": func(g *runScopeRig, u unattendedRun) {
			if err := artifactrun.End(t.Context(), g.f.rig.db, u.runID); err != nil {
				t.Fatal(err)
			}
		},
		"terminal journal": func(g *runScopeRig, u unattendedRun) {
			mustExec(t, g.f.rig.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('ua-terminal',?,?,'run.completed','orchestrator','done',?)`, g.f.a.WorkspaceID, u.agentID, u.runID)
		},
		"expired": func(g *runScopeRig, u unattendedRun) {
			mustExec(t, g.f.rig.db, `DROP TRIGGER artifact_unattended_runs_immutable`)
			mustExec(t, g.f.rig.db, `UPDATE artifact_unattended_runs SET expires_at='2026-01-01T00:00:00Z' WHERE run_id=?`, u.runID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRunScopeRig(t)
			g.standing(t, "ua-ends", g.f.a.AgentID, "read", g.f.rig.adminID)
			u := g.unattended(t, "ua-run-ends", g.f.a.AgentID, "WEBHOOK")
			if w := u.tool(g, "artifact_versions", "ua-ends", `{}`); w.Code != 200 {
				t.Fatalf("baseline: %d %s", w.Code, w.Body)
			}
			end(g, u)
			if w := u.tool(g, "artifact_versions", "ua-ends", `{}`); w.Code == 200 {
				t.Fatal("the run's capability outlived the run")
			}
		})
	}
}

// The read cap per run is the workspace's run_read_limit (default 50): the
// 51st distinct artifact is refused with 429 artifact_run_limit; a repeat
// read of one already reached is not.
func TestUnattendedRun_The51stDistinctReadIsRefused(t *testing.T) {
	g := newRunScopeRig(t)
	for i := 1; i <= defaultRunReadLimit+1; i++ {
		g.standing(t, fmt.Sprintf("ua-cap-%d", i), g.f.a.AgentID, "read", g.f.rig.adminID)
	}
	u := g.unattended(t, "ua-run-cap", g.f.a.AgentID, "ROUTINE")
	for i := 1; i <= defaultRunReadLimit; i++ {
		if w := u.tool(g, "artifact_versions", fmt.Sprintf("ua-cap-%d", i), `{}`); w.Code != 200 {
			t.Fatalf("read %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := u.tool(g, "artifact_versions", fmt.Sprintf("ua-cap-%d", defaultRunReadLimit+1), `{}`); w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "artifact_run_limit") {
		t.Fatalf("51st distinct read: %d %s", w.Code, w.Body)
	}
	if w := u.tool(g, "artifact_versions", "ua-cap-1", `{}`); w.Code != 200 {
		t.Fatalf("repeat read past the cap: %d %s", w.Code, w.Body)
	}
}

// A folder (D2) lets the run read, never write.
func TestUnattendedRun_NoWriteThroughAFolder(t *testing.T) {
	g := newRunScopeRig(t)
	id := g.create(t, g.f.a.HumanUserID, "MEMBER", "ua-folder")
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, id)
	mustExec(t, g.f.rig.db, `UPDATE artifact_version_heads SET shared_version=latest_version WHERE page_id=?`, id)
	g.folder(t, "ua-folder", g.f.a.CrewID, "propose", id)
	u := g.unattended(t, "ua-run-folder", g.f.a.AgentID, "WEBHOOK")
	if w := u.tool(g, "artifact_versions", "ua-folder", `{}`); w.Code != 200 {
		t.Fatalf("folder read: %d %s", w.Code, w.Body)
	}
	if w := u.tool(g, "artifact_publish", "ua-folder", artifactProjectBody(1, "Second")); w.Code == 200 {
		t.Fatal("a run without a human wrote through a folder")
	}
	rows := unattendedAuditRows(t, g)
	if len(rows) != 1 || rows[0].Reach != "folder" || rows[0].Operation != "read" {
		t.Fatalf("folder audit: %+v", rows)
	}
}

// Writes only through a write grant the artifact's owner issued, re-checked
// on every write: a grant from anyone else, or a read grant, does not write,
// and revoking the owner's grant mid-run stops the next write.
func TestUnattendedRun_WritesOnlyWithAnOwnerIssuedWriteGrant(t *testing.T) {
	g := newRunScopeRig(t)
	byAdmin := g.standing(t, "ua-write-admin", g.f.a.AgentID, "write", g.f.rig.adminID)
	g.grant(t, byAdmin, "user", g.f.rig.adminID, "write")
	g.standing(t, "ua-write-read", g.f.a.AgentID, "read", g.f.a.HumanUserID)
	byOwner := g.standing(t, "ua-write-owner", g.f.a.AgentID, "write", g.f.a.HumanUserID)
	u := g.unattended(t, "ua-run-write", g.f.a.AgentID, "WEBHOOK")
	if w := u.tool(g, "artifact_publish", "ua-write-admin", artifactProjectBody(1, "Second")); w.Code == 200 {
		t.Fatal("a write grant not issued by the owner wrote")
	}
	if w := u.tool(g, "artifact_publish", "ua-write-read", artifactProjectBody(1, "Second")); w.Code == 200 {
		t.Fatal("a read grant wrote")
	}
	if w := u.tool(g, "artifact_publish", "ua-write-owner", artifactProjectBody(1, "Second")); w.Code != 200 {
		t.Fatalf("owner-issued write grant: %d %s", w.Code, w.Body)
	}
	var actor string
	if err := g.f.rig.db.QueryRow(`SELECT actor FROM artifact_versions WHERE page_id=? AND version=2`, byOwner).Scan(&actor); err != nil || actor != "agent:"+g.f.a.AgentID+":"+u.runID {
		t.Fatalf("version actor %q %v", actor, err)
	}
	mustExec(t, g.f.rig.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent'`, byOwner)
	if w := u.tool(g, "artifact_publish", "ua-write-owner", artifactProjectBody(2, "Third")); w.Code == 200 {
		t.Fatal("a write succeeded after the owner revoked the grant mid-run")
	}
	var writes int
	for _, r := range unattendedAuditRows(t, g) {
		if r.Operation == "write" {
			writes++
			if r.Page != byOwner || r.Reach != "standing" || r.User != nil {
				t.Fatalf("write audit %+v", r)
			}
		}
	}
	if writes != 1 {
		t.Fatalf("want one write audit row, got %d", writes)
	}
}

// A private (Personal) artifact is never reachable, whatever grant the agent
// holds, and the refusal looks like a missing artifact.
func TestUnattendedRun_PrivateIsNeverReachable(t *testing.T) {
	g := newRunScopeRig(t)
	id := g.standing(t, "ua-private", g.f.a.AgentID, "write", g.f.a.HumanUserID)
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, id)
	u := g.unattended(t, "ua-run-private", g.f.a.AgentID, "WEBHOOK")
	missing := u.tool(g, "artifact_versions", "ua-nowhere", `{}`)
	sameRefusal(t, "private read", u.tool(g, "artifact_versions", "ua-private", `{}`), missing)
	if w := u.tool(g, "artifact_publish", "ua-private", artifactProjectBody(1, "Second")); w.Code == 200 {
		t.Fatal("private write")
	}
}

// A run without a human may not create, share, comment, reply or touch
// dashboard data through this path; only the read and the publish are open.
func TestUnattendedRun_OtherOperationsAreRefused(t *testing.T) {
	g := newRunScopeRig(t)
	g.standing(t, "ua-ops", g.f.a.AgentID, "write", g.f.a.HumanUserID)
	u := g.unattended(t, "ua-run-ops", g.f.a.AgentID, "ROUTINE")
	body := `{"name":"ua-new",` + strings.TrimPrefix(artifactProjectBody(0, "ua-new"), "{")
	for op, payload := range map[string]string{
		"artifact_create":           body,
		"artifact_comments":         `{}`,
		"artifact_restore":          `{"base_version":1,"version":1}`,
		"artifact_data_collections": `{"version":1}`,
	} {
		if w := u.tool(g, op, "ua-ops", payload); w.Code == 200 || w.Code == 201 {
			t.Fatalf("%s succeeded for a run without a human: %s", op, w.Body)
		}
	}
	var n int
	if err := g.f.rig.db.QueryRow(`SELECT count(*) FROM pages WHERE slug='ua-new'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("create left a page: %d %v", n, err)
	}
}

// Fail closed: an audit row that cannot be written, or is silently dropped,
// refuses the access.
func TestUnattendedRun_AuditFailsClosed(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER ua_audit_abort BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`,
		`CREATE TRIGGER ua_audit_ignore BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(IGNORE); END`,
	} {
		t.Run(strings.Fields(trigger)[2], func(t *testing.T) {
			g := newRunScopeRig(t)
			id := g.standing(t, "ua-audit", g.f.a.AgentID, "write", g.f.a.HumanUserID)
			u := g.unattended(t, "ua-run-audit", g.f.a.AgentID, "WEBHOOK")
			mustExec(t, g.f.rig.db, trigger)
			if w := u.tool(g, "artifact_versions", "ua-audit", `{}`); w.Code == 200 {
				t.Fatal("read succeeded without its audit")
			}
			if w := u.tool(g, "artifact_publish", "ua-audit", artifactProjectBody(1, "Second")); w.Code == 200 {
				t.Fatal("write succeeded without its audit")
			}
			var latest int
			if err := g.f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, id).Scan(&latest); err != nil || latest != 1 {
				t.Fatalf("refused write left a version: %d %v", latest, err)
			}
		})
	}
}

// Flag off: unchanged. Nothing is recorded and a run without a human reaches
// nothing, even with a capability recorded while the flag was on.
func TestUnattendedRun_FlagOffReachesNothing(t *testing.T) {
	g := newRunScopeRig(t)
	g.standing(t, "ua-flag-off", g.f.a.AgentID, "write", g.f.a.HumanUserID)
	u := g.unattended(t, "ua-run-flag-off", g.f.a.AgentID, "WEBHOOK")
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	if w := u.tool(g, "artifact_versions", "ua-flag-off", `{}`); w.Code == 200 {
		t.Fatal("flag off: a run without a human read")
	}
	if w := u.tool(g, "artifact_publish", "ua-flag-off", artifactProjectBody(1, "Second")); w.Code == 200 {
		t.Fatal("flag off: a run without a human wrote")
	}
	capability, err := artifactrun.Record(t.Context(), g.f.rig.db, artifactrun.Run{WorkspaceID: g.f.a.WorkspaceID, CrewID: g.f.a.CrewID, AgentID: g.f.a.AgentID, RunID: "ua-run-flag-off-2", Trigger: "WEBHOOK"}, time.Hour)
	if err != nil || capability != "" {
		t.Fatalf("flag off minted a capability: %q %v", capability, err)
	}
	if rows := unattendedAuditRows(t, g); len(rows) != 0 {
		t.Fatalf("flag off audited: %+v", rows)
	}
}

// A chat run with a human is untouched by the capability header.
func TestUnattendedRun_ChatRunIgnoresTheHeader(t *testing.T) {
	g := newRunScopeRig(t)
	id := g.create(t, g.f.a.HumanUserID, "MEMBER", "ua-chat")
	g.start(t, id)
	if w := g.toolAs(g.runToken, artifactrun.Prefix+strings.Repeat("cd", 32), "artifact_versions", "ua-chat", `{}`); w.Code != 200 {
		t.Fatalf("chat run with a stray header: %d %s", w.Code, w.Body)
	}
}

// The host producers' helper: flag on, it records the run and its end closes
// it; a crew-less run or flag off records nothing.
func TestUnattendedRun_ProducerHelperRecordsAndEnds(t *testing.T) {
	g := newRunScopeRig(t)
	g.unattended(t, "ua-run-helper-seed", g.f.a.AgentID, "WEBHOOK")
	capability, end := recordUnattendedArtifactRun(t.Context(), g.f.rig.db, newTestLogger(), artifactrun.Run{WorkspaceID: g.f.a.WorkspaceID, CrewID: g.f.a.CrewID, AgentID: g.f.a.AgentID, RunID: "ua-run-helper", Trigger: artifactrun.TriggerScheduled, Source: "schedule"}, time.Hour)
	if !strings.HasPrefix(capability, artifactrun.Prefix) {
		t.Fatalf("capability %q", capability)
	}
	end()
	var ended *string
	if err := g.f.rig.db.QueryRow(`SELECT ended_at FROM artifact_unattended_runs WHERE run_id='ua-run-helper'`).Scan(&ended); err != nil || ended == nil {
		t.Fatalf("not ended: %v %v", ended, err)
	}
	if c, _ := recordUnattendedArtifactRun(t.Context(), g.f.rig.db, newTestLogger(), artifactrun.Run{WorkspaceID: g.f.a.WorkspaceID, AgentID: g.f.a.AgentID, RunID: "ua-run-crewless", Trigger: artifactrun.TriggerScheduled}, time.Hour); c != "" {
		t.Fatal("crew-less run recorded")
	}
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	if c, _ := recordUnattendedArtifactRun(t.Context(), g.f.rig.db, newTestLogger(), artifactrun.Run{WorkspaceID: g.f.a.WorkspaceID, CrewID: g.f.a.CrewID, AgentID: g.f.a.AgentID, RunID: "ua-run-off", Trigger: artifactrun.TriggerWebhook}, time.Hour); c != "" {
		t.Fatal("flag off recorded")
	}
}

// Each layer binds the agent on its own (review M2): the record lookup refuses
// another agent of the crew even with the right capability, and the run gate
// refuses another agent even when that agent also has a live journal entry
// for the run id.
func TestUnattendedRun_EachLayerBindsTheAgent(t *testing.T) {
	g := newRunScopeRig(t)
	other := seedAgentRow(t, g.f.rig.db, "ua-agent-m2", g.f.a.WorkspaceID, g.f.a.CrewID, "Agent M2", "ua-agent-m2", "AGENT")
	u := g.unattended(t, "ua-run-m2", g.f.a.AgentID, "WEBHOOK")
	mustExec(t, g.f.rig.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('ua-m2-started',?,?,'run.started','orchestrator','started',?)`, g.f.a.WorkspaceID, other, u.runID)
	h := g.router.pages
	ctx := t.Context()
	if a := h.lookupUnattendedRun(ctx, g.f.a.WorkspaceID, g.f.a.CrewID, g.f.a.AgentID, u.runID, u.capability); a == nil {
		t.Fatal("baseline lookup refused")
	}
	if a := h.lookupUnattendedRun(ctx, g.f.a.WorkspaceID, g.f.a.CrewID, other, u.runID, u.capability); a != nil {
		t.Fatal("lookup admitted another agent with the run's capability")
	}
	if err := h.checkUnattendedArtifactRun(ctx, &artifactRunAuthority{WorkspaceID: g.f.a.WorkspaceID, CrewID: g.f.a.CrewID, AgentID: g.f.a.AgentID, RunID: u.runID, Unattended: true}); err != nil {
		t.Fatal("baseline gate refused", err)
	}
	if err := h.checkUnattendedArtifactRun(ctx, &artifactRunAuthority{WorkspaceID: g.f.a.WorkspaceID, CrewID: g.f.a.CrewID, AgentID: other, RunID: u.runID, Unattended: true}); err == nil {
		t.Fatal("gate admitted another agent on the recorded run")
	}
}

// A page that is private while a live agent grant on it still exists (the
// grant written straight to the table, past any retire path) is refused like
// a missing artifact, for reads and writes (review M5).
func TestUnattendedRun_PrivateWithALiveGrantIsRefusedLikeMissing(t *testing.T) {
	g := newRunScopeRig(t)
	id := g.create(t, g.f.a.HumanUserID, "MEMBER", "ua-private-live")
	mustExec(t, g.f.rig.db, `UPDATE artifact_version_heads SET shared_version=latest_version WHERE page_id=?`, id)
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,?,?,?,?)`, id, "agent", g.f.a.AgentID, "write", g.f.a.HumanUserID)
	var private, live int
	if err := g.f.rig.db.QueryRow(`SELECT p.private_audience,(SELECT count(*) FROM page_grants WHERE page_id=p.id AND subject_type='agent' AND retired_at IS NULL) FROM pages p WHERE p.id=?`, id).Scan(&private, &live); err != nil || private != 1 || live != 1 {
		t.Fatalf("fixture: private=%d live grants=%d %v", private, live, err)
	}
	u := g.unattended(t, "ua-run-private-live", g.f.a.AgentID, "WEBHOOK")
	missing := u.tool(g, "artifact_versions", "ua-nowhere", `{}`)
	sameRefusal(t, "private read with a live grant", u.tool(g, "artifact_versions", "ua-private-live", `{}`), missing)
	if w := u.tool(g, "artifact_publish", "ua-private-live", artifactProjectBody(1, "Second")); w.Code == 200 {
		t.Fatal("private write with a live grant")
	}
	if rows := unattendedAuditRows(t, g); len(rows) != 0 {
		t.Fatalf("refused private access audited: %+v", rows)
	}
	// The page loader already hides private pages from a caller with no user;
	// the decision layer must refuse them on its own too.
	h := g.router.pages
	a := h.lookupUnattendedRun(t.Context(), g.f.a.WorkspaceID, g.f.a.CrewID, g.f.a.AgentID, u.runID, u.capability)
	if a == nil {
		t.Fatal("run lookup refused")
	}
	decide := func(private, write bool) error {
		tx, err := g.f.rig.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		rec := &pageRecord{ID: id, Slug: "ua-private-live", OwnerUserID: g.f.a.HumanUserID, PrivateAudience: private}
		_, err = h.authorizeUnattendedArtifactRun(t.Context(), tx, a, g.f.a.WorkspaceID, rec, write, false)
		return err
	}
	if err := decide(false, false); err != nil {
		t.Fatal("baseline: the live grant should reach the page when it is not private:", err)
	}
	for _, write := range []bool{false, true} {
		if err := decide(true, write); !errors.Is(err, errArtifactRefNotFound) {
			t.Fatalf("private page with a live grant (write=%v): %v, want refused like missing", write, err)
		}
	}
}

// The api producers' capability lives as long as their hard run deadline.
func TestUnattendedRun_ProducerTTLCoversTheRunDeadline(t *testing.T) {
	for name, deadline := range map[string]time.Duration{"webhook": webhookAgentRunTimeout, "scheduled": scheduledAgentRunTimeout} {
		if ttl := artifactrun.TTLFor(deadline); ttl < deadline || ttl > artifactrun.MaxTTL {
			t.Fatalf("%s: ttl %s for a %s run", name, ttl, deadline)
		}
	}
}

// A recorded, live run whose call arrives without its capability (its sidecar
// restarted and forgot it) is told so plainly, 403 run_capability_lost, and
// the loss is journaled once per run. It still reaches nothing. A run the
// host never recorded, an ended run, and the flag off keep the generic
// refusal (owner decision B).
func TestUnattendedRun_LostCapabilityIsSaidPlainly(t *testing.T) {
	g := newRunScopeRig(t)
	g.standing(t, "ua-lost", g.f.a.AgentID, "read", g.f.a.HumanUserID)
	writer := journal.NewWriter(g.f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-unattended-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	g.router.pages.SetJournal(writer)
	lostRows := func() int {
		t.Helper()
		if err := writer.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := g.f.rig.db.QueryRow(`SELECT count(*) FROM journal_entries WHERE entry_type='artifact.run_capability_lost'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	u := g.unattended(t, "ua-run-lost", g.f.a.AgentID, "WEBHOOK")
	for i := 0; i < 2; i++ {
		w := g.toolAs(u.token, "", "artifact_versions", "ua-lost", `{}`)
		var body map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 403 || body["code"] != "run_capability_lost" || !strings.Contains(body["error"], "sidecar") {
			t.Fatalf("call %d without the capability: %d %s", i, w.Code, w.Body)
		}
	}
	if n := lostRows(); n != 1 {
		t.Fatalf("lost capability journaled %d times, want once", n)
	}
	if rows := unattendedAuditRows(t, g); len(rows) != 0 {
		t.Fatalf("a lost capability reached something: %+v", rows)
	}
	if w := u.tool(g, "artifact_versions", "ua-lost", `{}`); w.Code != 200 {
		t.Fatalf("the capability itself still works: %d %s", w.Code, w.Body)
	}

	generic := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 403 || strings.Contains(w.Body.String(), "run_capability_lost") {
			t.Fatalf("%s: %d %s, want the generic refusal", what, w.Code, w.Body)
		}
	}
	key := internaltoken.DeriveAgentRunKey(unattendedRunMaster, g.f.a.WorkspaceID, g.f.a.CrewID)
	generic("a run the host never recorded", g.toolAs(internaltoken.DeriveAgentRunToken(key, g.f.a.WorkspaceID, g.f.a.AgentID, "ua-run-unrecorded"), "", "artifact_versions", "ua-lost", `{}`))
	ended := g.unattended(t, "ua-run-lost-ended", g.f.a.AgentID, "WEBHOOK")
	if err := artifactrun.End(t.Context(), g.f.rig.db, ended.runID); err != nil {
		t.Fatal(err)
	}
	generic("an ended run", g.toolAs(ended.token, "", "artifact_versions", "ua-lost", `{}`))
	// A call that carries a capability, even a wrong one, did not lose it:
	// it gets the generic refusal and journals nothing.
	generic("a wrong capability", g.toolAs(u.token, "urcv1."+strings.Repeat("0", 64), "artifact_versions", "ua-lost", `{}`))
	other := g.unattended(t, "ua-run-lost-flag", g.f.a.AgentID, "WEBHOOK")
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	generic("flag off", g.toolAs(other.token, "", "artifact_versions", "ua-lost", `{}`))
	if n := lostRows(); n != 1 {
		t.Fatalf("generic refusals journaled a loss: %d", n)
	}
}
