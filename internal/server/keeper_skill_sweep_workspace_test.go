package server

// CRE-138 regression: the F4.1 skill_review sweep hard-coded
// WorkspaceID:"" ("global catalog; F4.1 prompt tolerates empty") — but the
// paymaster pre-call Enforce does NOT tolerate empty, so every gatekeeper
// LLM call failed with "paymaster: workspace_id required", the gatekeeper
// swallowed it into deny-by-default, and every skill got unverified with a
// blocking inbox item on every daily sweep.
//
// Contract under test:
//  1. loadSkillSweepInputs resolves a real billing workspace for each
//     skill from its enabled assignments on live agents (deterministic:
//     first by workspace_id ASC — one LLM review per skill, billed to a
//     workspace that actually uses it).
//  2. Skills with no enabled assignment on a live agent are skipped
//     entirely: there is no workspace to bill and WriteInboxItem would
//     drop the outcome anyway (no workspace to notify).

import (
	"context"
	"testing"
	"time"
)

func TestLoadSkillSweepInputs_ResolvesBillingWorkspace(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	covSeedSkillFixtures(t, db)

	inputs, err := loadSkillSweepInputs(context.Background(), db, krLogger())
	if err != nil {
		t.Fatalf("loadSkillSweepInputs: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %d, want 1", len(inputs))
	}
	if got := inputs[0].Skill.WorkspaceID; got != "ws_cov" {
		t.Errorf("Skill.WorkspaceID = %q, want %q — empty means the paymaster rejects the review call pre-flight (CRE-138)", got, "ws_cov")
	}
}

func TestLoadSkillSweepInputs_BillingWorkspaceIsDeterministicFirstASC(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	covSeedSkillFixtures(t, db)
	// Second workspace using the same skill, lexically BEFORE ws_cov.
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_aaa','WS2','ws-aaa')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_aaa','ws_aaa','C2','c-aaa')`)
	mustExec(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag_aaa','cr_aaa','ws_aaa','A2','a-aaa')`)
	mustExec(t, db, `INSERT INTO agent_skills (agent_id, skill_id, enabled) VALUES ('ag_aaa','sk_cov',1)`)

	inputs, err := loadSkillSweepInputs(context.Background(), db, krLogger())
	if err != nil {
		t.Fatalf("loadSkillSweepInputs: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %d, want 1", len(inputs))
	}
	if got := inputs[0].Skill.WorkspaceID; got != "ws_aaa" {
		t.Errorf("Skill.WorkspaceID = %q, want %q (first by workspace_id ASC)", got, "ws_aaa")
	}
}

func TestLoadSkillSweepInputs_SkipsSkillWithNoLiveAssignment(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	covSeedSkillFixtures(t, db)
	now := time.Now().UTC().Format(time.RFC3339)
	// A skill nobody has assigned: no workspace to bill, no workspace to
	// notify — reviewing it burns LLM spend for an outcome nobody sees.
	mustExec(t, db, `INSERT INTO skills (id, name, slug, display_name, description, lifecycle_state, last_used_at)
	                 VALUES ('sk_orphan','orphan','orphan','Orphan','unused','active',?)`, now)
	// A skill whose only assignment is disabled — same treatment.
	mustExec(t, db, `INSERT INTO skills (id, name, slug, display_name, description, lifecycle_state, last_used_at)
	                 VALUES ('sk_disabled','disabled','disabled-skill','Disabled','off','active',?)`, now)
	mustExec(t, db, `INSERT INTO agent_skills (agent_id, skill_id, enabled) VALUES ('ag_cov','sk_disabled',0)`)

	inputs, err := loadSkillSweepInputs(context.Background(), db, krLogger())
	if err != nil {
		t.Fatalf("loadSkillSweepInputs: %v", err)
	}
	if len(inputs) != 1 {
		ids := make([]string, 0, len(inputs))
		for _, in := range inputs {
			ids = append(ids, in.Skill.ID)
		}
		t.Fatalf("inputs = %v, want only [sk_cov] — unassigned skills must be skipped (CRE-138)", ids)
	}
	if inputs[0].Skill.ID != "sk_cov" {
		t.Errorf("Skill.ID = %q, want sk_cov", inputs[0].Skill.ID)
	}
}
