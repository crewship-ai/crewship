package manifest

import (
	"strings"
	"testing"
)

func TestValidate_RejectsBadSlug(t *testing.T) {
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: "Bad Slug" }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = b.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid slug") {
		t.Fatalf("want slug error, got %v", err)
	}
}

func TestValidate_RejectsTwoLeads(t *testing.T) {
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
    - { slug: b, name: B, agent_role: LEAD, prompt: y }
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = b.Validate()
	if err == nil || !strings.Contains(err.Error(), "LEAD") {
		t.Fatalf("want LEAD-conflict error, got %v", err)
	}
}

func TestValidate_RejectsDanglingSkillRef(t *testing.T) {
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  agents:
    - slug: a
      name: A
      agent_role: LEAD
      prompt: x
      skills: [ghost]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = b.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("want unknown-skill error, got %v", err)
	}
}

func TestValidate_RejectsDanglingCredRef(t *testing.T) {
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  agents:
    - slug: a
      name: A
      agent_role: LEAD
      prompt: x
      env_refs: [PHANTOM_KEY]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = b.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown credential") {
		t.Fatalf("want unknown-credential error, got %v", err)
	}
}

func TestValidate_RejectsBadMCPEnvMapping(t *testing.T) {
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  mcp_servers:
    - name: github
      transport: stdio
      command: npx
      env_mapping:
        TOKEN: NONEXISTENT_CRED
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = b.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown credential") {
		t.Fatalf("want unknown-credential error from MCP mapping, got %v", err)
	}
}

func TestValidate_RejectsZeroSkillSources(t *testing.T) {
	// Load() now enforces the one-source-per-skill invariant during
	// resolveInlineOnly so the error surfaces immediately rather
	// than at Validate-time. The user-visible behaviour is the
	// same: a malformed manifest stops the pipeline.
	_, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  skills:
    - { slug: empty }
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`))
	if err == nil || !strings.Contains(err.Error(), "one of path") {
		t.Fatalf("want missing-source error from Load, got %v", err)
	}
}

func TestValidate_RejectsMultipleSkillSources(t *testing.T) {
	_, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  skills:
    - slug: dual
      inline: "---\nname: dual\ndescription: x\n---\n# X"
      source: https://example.com/SKILL.md
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`))
	if err == nil || !strings.Contains(err.Error(), "only one of") {
		t.Fatalf("want multi-source error from Load, got %v", err)
	}
}

func TestValidate_WorkspaceCrossScopeReference(t *testing.T) {
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Workspace
metadata: { name: WS, slug: ws }
spec:
  credentials:
    - { env: KEY, provider: NONE, type: API_KEY }
  skills:
    - { slug: shared, inline: "---\nname: shared\ndescription: y\n---\nB" }
  crews:
    - slug: c1
      name: One
      agents:
        - slug: a
          name: A
          agent_role: LEAD
          prompt: x
          env_refs: [KEY]      # references workspace-scope credential
          skills: [shared]     # references workspace-scope skill
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("cross-scope refs should resolve, got: %v", err)
	}
}
