package manifest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_ResolvesInlineWithoutSourcePath(t *testing.T) {
	// Regression for the bug where Load() returned unresolved
	// inline skills if no SourcePath was set. The fix: inline
	// resolves immediately during Load; only path:/prompt_file:
	// wait for LoadFile.
	body := []byte(`apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  skills:
    - slug: inline-test
      inline: "---\nname: inline-test\ndescription: x\n---\nbody"
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x, skills: [inline-test]}
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := b.Documents[0].Spec.Skills[0].Resolved(); got == "" {
		t.Error("Load() did not resolve inline skill — regression of fix #1")
	}
}

func TestLoad_RejectsTooLargeInline(t *testing.T) {
	big := strings.Repeat("a", maxInlineSkillBytes+1)
	body := []byte(`apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  skills:
    - slug: huge
      inline: "` + big + `"
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	_, err := Load(body)
	if err == nil || !strings.Contains(err.Error(), "max") {
		t.Errorf("want size-limit error, got %v", err)
	}
}

func TestSafeJoin_RejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(dir, "innocent.md")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	manifestPath := filepath.Join(dir, "crew.yaml")
	body := `apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  agents:
    - slug: a
      name: A
      agent_role: LEAD
      prompt_file: ./innocent.md
`
	if err := os.WriteFile(manifestPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	_, err := LoadFile(manifestPath)
	if err == nil {
		t.Fatal("expected symlink-escape error, got nil")
	}
}

func TestValidate_RejectsObserverRole(t *testing.T) {
	// OBSERVER passes through some legacy docs but the server only
	// accepts AGENT and LEAD; this regression check keeps validate
	// honest after fix #5.
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  agents:
    - {slug: a, name: A, agent_role: OBSERVER, prompt: x}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = b.Validate()
	if err == nil || !strings.Contains(err.Error(), "agent_role") {
		t.Fatalf("want agent_role-rejection, got %v", err)
	}
}

func TestApply_SyncDeletesDriftedAgents(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  agents:
    - {slug: alice, name: Alice, agent_role: LEAD, prompt: hi}
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	fake := newFakeAPI(t)
	fake.crewsBySlug["t"] = map[string]any{"id": "crew_existing", "slug": "t", "workspace_id": fake.wsID, "name": "T"}
	fake.agentsBySlug["alice"] = map[string]any{"id": "agent_keep", "slug": "alice", "name": "Alice", "agent_role": "LEAD", "crew_id": "crew_existing"}
	fake.agentsBySlug["ghost"] = map[string]any{"id": "agent_ghost", "slug": "ghost", "name": "Ghost", "agent_role": "AGENT", "crew_id": "crew_existing"}

	client := NewClient(fake)
	res, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Yes: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Deleted == 0 {
		t.Errorf("expected ghost agent to be deleted, got Deleted=0; result=%+v", res)
	}
	// Make sure the DELETE went out for the ghost.
	var sawDelete bool
	for _, call := range fake.Calls {
		if call.Method == "DELETE" && strings.Contains(call.Path, "/agents/agent_ghost") {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Error("expected DELETE call for /api/v1/agents/agent_ghost")
	}
}

func TestApply_DestructivePlanRequiresConfirmation(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  agents:
    - {slug: alice, name: Alice, agent_role: LEAD, prompt: hi}
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	fake := newFakeAPI(t)
	fake.crewsBySlug["t"] = map[string]any{"id": "crew_existing", "slug": "t", "workspace_id": fake.wsID, "name": "T"}
	fake.agentsBySlug["alice"] = map[string]any{"id": "agent_keep", "slug": "alice", "name": "Alice", "agent_role": "LEAD", "crew_id": "crew_existing"}
	fake.agentsBySlug["ghost"] = map[string]any{"id": "agent_ghost", "slug": "ghost", "name": "Ghost", "agent_role": "AGENT", "crew_id": "crew_existing"}

	client := NewClient(fake)
	_, err = Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Yes: false})
	if err != ErrConfirmationRequired {
		t.Errorf("want ErrConfirmationRequired, got %v", err)
	}
	// No DELETE should have happened.
	for _, call := range fake.Calls {
		if call.Method == "DELETE" {
			t.Errorf("destructive operation ran without confirmation: %s %s", call.Method, call.Path)
		}
	}
}

func TestApply_UnlinksRemovedAgentSkill(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  skills:
    - {slug: keep, inline: "---\nname: keep\ndescription: x\n---\nbody"}
  agents:
    - {slug: alice, name: Alice, agent_role: LEAD, prompt: hi, skills: [keep]}
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	fake := newFakeAPI(t)
	fake.crewsBySlug["t"] = map[string]any{"id": "crew_x", "slug": "t", "workspace_id": fake.wsID, "name": "T"}
	fake.agentsBySlug["alice"] = map[string]any{"id": "agent_alice", "slug": "alice", "name": "Alice", "agent_role": "LEAD", "crew_id": "crew_x"}
	// Existing bindings: alice has "keep" AND "stale" — manifest only declares "keep".
	fake.agentSkillBindings = map[string][]map[string]any{
		"agent_alice": {
			{"id": "bind_keep", "skill_id": "skill_keep", "skill": map[string]any{"slug": "keep"}},
			{"id": "bind_stale", "skill_id": "skill_stale", "skill": map[string]any{"slug": "stale"}},
		},
	}
	fake.skillsBySlug["keep"] = map[string]any{"id": "skill_keep", "slug": "keep", "created": false}

	client := NewClient(fake)
	res, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Yes: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Deleted == 0 {
		t.Errorf("expected stale agent_skill deletion, got %+v", res)
	}
	var sawSkillUnlink bool
	for _, call := range fake.Calls {
		if call.Method == "DELETE" && strings.Contains(call.Path, "/skills/skill_stale") {
			sawSkillUnlink = true
		}
	}
	if !sawSkillUnlink {
		t.Error("expected DELETE on /agents/agent_alice/skills/skill_stale")
	}
}

func TestApply_StrictModeOnFreshCrewSucceeds(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  agents:
    - {slug: alice, name: Alice, agent_role: LEAD, prompt: hi}
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	fake := newFakeAPI(t)
	client := NewClient(fake)
	res, err := Apply(context.Background(), client, b, Options{Mode: ApplyStrict, Yes: true})
	if err != nil {
		t.Fatalf("strict on fresh workspace should succeed, got %v", err)
	}
	if res.Created < 2 {
		t.Errorf("expected crew + agent created, got Created=%d", res.Created)
	}
}
