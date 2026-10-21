package api

import "testing"

func TestAutonomousPipelineMentionRequiresFrozenPositiveAdmission(t *testing.T) {
	h, _, workspace, crew, agent, worker := newTestIssueHandler(t)
	// This validator consumes persisted admission evidence; it does not publish
	// recipes. Seed the historical recipe/version directly to keep its fixture
	// independent of the separate fresh-test-run publication gate.
	const recipeID, recipeSlug = "machine-mention-recipe", "machine-mention"
	const definition = `{"name":"Machine","steps":[{"id":"done","type":"transform","transform":{"input":"ok","expression":"."}}]}`
	execOrFatal(t, h.db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,author_agent_id) VALUES(?,?,?,'Machine',?,'fixture-hash',?,?)`, recipeID, workspace, recipeSlug, definition, crew, agent)
	execOrFatal(t, h.db, `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id) VALUES('machine-mention-version',?,1,?,'fixture-hash','agent',?)`, recipeID, definition, agent)
	execOrFatal(t, h.db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,pipeline_version,status,mode,started_at,created_at,updated_at) VALUES('committed-pipeline',?,?,?,1,'completed','run',datetime('now'),datetime('now'),datetime('now'))`, workspace, recipeID, recipeSlug)
	mc := mentionContext{WorkspaceID: workspace, AuthorType: "agent", AuthorID: agent, SourceRunID: "committed-pipeline", AutonomousPipeline: true, AutonomousPipelineCrewID: crew}
	if err := validateAutonomousPipelineMentionSource(t.Context(), h.db, mc); err != nil {
		t.Fatalf("committed machine intent denied: %v", err)
	}
	// The recipe's mutable head is not the author of an already-committed intent.
	execOrFatal(t, h.db, `UPDATE pipelines SET author_agent_id=? WHERE id=?`, worker, recipeID)
	if err := validateAutonomousPipelineMentionSource(t.Context(), h.db, mc); err != nil {
		t.Fatalf("head edit revoked original actor: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*mentionContext)
	}{
		{"unverified", func(c *mentionContext) { c.AutonomousPipeline = false }},
		{"no snapshot crew", func(c *mentionContext) { c.AutonomousPipelineCrewID = "" }},
		{"foreign crew", func(c *mentionContext) { c.AutonomousPipelineCrewID = "foreign" }},
		{"foreign workspace", func(c *mentionContext) { c.WorkspaceID = "foreign" }},
		{"missing run", func(c *mentionContext) { c.SourceRunID = "unknown" }},
		{"human author", func(c *mentionContext) { c.AuthorType = "user" }},
		{"system author", func(c *mentionContext) { c.AuthorType = "system" }},
		{"missing actor", func(c *mentionContext) { c.AuthorID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := mc
			tc.change(&candidate)
			if err := validateAutonomousPipelineMentionSource(t.Context(), h.db, candidate); err == nil {
				t.Fatal("unverified source admitted")
			}
		})
	}
	execOrFatal(t, h.db, `UPDATE pipeline_runs SET pipeline_version=NULL WHERE id='committed-pipeline'`)
	if err := validateAutonomousPipelineMentionSource(t.Context(), h.db, mc); err != nil {
		t.Fatalf("host-admitted unversioned legacy intent denied: %v", err)
	}
	execOrFatal(t, h.db, `UPDATE pipeline_runs SET pipeline_version=77 WHERE id='committed-pipeline'`)
	if err := validateAutonomousPipelineMentionSource(t.Context(), h.db, mc); err == nil {
		t.Fatal("missing pinned version admitted")
	}
	execOrFatal(t, h.db, `UPDATE pipeline_runs SET pipeline_version=1 WHERE id='committed-pipeline'`)
	execOrFatal(t, h.db, `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES('invalid-human-source',?,'unknown','legacy','legacy','{}',datetime('now'))`, workspace)
	execOrFatal(t, h.db, `UPDATE pipeline_runs SET chat_origin_id='invalid-human-source' WHERE id='committed-pipeline'`)
	if err := validateAutonomousPipelineMentionSource(t.Context(), h.db, mc); err == nil {
		t.Fatal("invalid human source became autonomous")
	}
	execOrFatal(t, h.db, `UPDATE pipeline_runs SET chat_origin_id=NULL WHERE id='committed-pipeline';UPDATE pipeline_versions SET chat_origin_id='invalid-human-source' WHERE pipeline_id=? AND version=1`, recipeID)
	if err := validateAutonomousPipelineMentionSource(t.Context(), h.db, mc); err == nil {
		t.Fatal("human pinned version became autonomous")
	}
	execOrFatal(t, h.db, `UPDATE pipeline_versions SET chat_origin_id=NULL WHERE pipeline_id=? AND version=1`, recipeID)
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,trace_id,entry_type,actor_type,summary,payload) VALUES('missing-requester-pipeline',?,?,'committed-pipeline','run.started','orchestrator','ambiguous','{"requester_authority_missing":true}')`, workspace, agent)
	if err := validateAutonomousPipelineMentionSource(t.Context(), h.db, mc); err == nil {
		t.Fatal("missing human requester evidence became autonomous")
	}
}
