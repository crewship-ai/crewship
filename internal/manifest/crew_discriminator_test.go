package manifest

import (
	"strings"
	"testing"
)

// TestParse_CrewDiscriminator_LegacyBundle pins the structural-
// sniffing rule: a `kind: Crew` doc with ANY nested sub-resource
// key (agents/skills/credentials/mcp_servers) is treated as the
// legacy bundle shape and lands in b.Documents. Empty lists count
// as legacy intent — the operator started a bundle.
func TestParse_CrewDiscriminator_LegacyBundle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "agents populated",
			yaml: `
apiVersion: crewship/v1
kind: Crew
metadata: { name: My Crew, slug: my-crew }
spec:
  agents:
    - slug: alice
      role: AGENT
`,
		},
		{
			name: "empty agents list",
			yaml: `
apiVersion: crewship/v1
kind: Crew
metadata: { name: My Crew, slug: my-crew }
spec:
  agents: []
`,
		},
		{
			name: "only skills nested",
			yaml: `
apiVersion: crewship/v1
kind: Crew
metadata: { name: My Crew, slug: my-crew }
spec:
  skills:
    - slug: pythonista
      inline: "Test skill body"
`,
		},
		{
			name: "only credentials nested",
			yaml: `
apiVersion: crewship/v1
kind: Crew
metadata: { name: My Crew, slug: my-crew }
spec:
  credentials:
    - slug: github-token
`,
		},
		{
			name: "only mcp_servers nested",
			yaml: `
apiVersion: crewship/v1
kind: Crew
metadata: { name: My Crew, slug: my-crew }
spec:
  mcp_servers:
    - name: filesystem
`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := Load([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(b.Documents) != 1 {
				t.Errorf("Documents = %d, want 1 (legacy bundle path)", len(b.Documents))
			}
			if len(b.Crews) != 0 {
				t.Errorf("Crews = %d, want 0 (legacy bundle should NOT land in Crews)", len(b.Crews))
			}
		})
	}
}

// TestParse_CrewDiscriminator_TopLevel confirms a `kind: Crew` doc
// with NO nested sub-resources lands in b.Crews (the new top-level
// CrewDocument shape), not in b.Documents.
func TestParse_CrewDiscriminator_TopLevel(t *testing.T) {
	t.Parallel()
	const y = `
apiVersion: crewship/v1
kind: Crew
metadata: { name: My Crew, slug: my-crew }
spec:
  description: Top-level crew, no nested agents
  icon: terminal
  color: "#3B82F6"
`
	b, err := Load([]byte(y))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Crews) != 1 {
		t.Fatalf("Crews = %d, want 1 (top-level path)", len(b.Crews))
	}
	if len(b.Documents) != 0 {
		t.Errorf("Documents = %d, want 0 (top-level should NOT land in legacy bundle)", len(b.Documents))
	}
	if got := b.Crews[0].Metadata.Slug; got != "my-crew" {
		t.Errorf("slug = %q, want %q", got, "my-crew")
	}
}

// TestParse_AgentTopLevel confirms `kind: Agent` parses into
// b.Agents. Pure addition — no legacy nested-Agent shape to
// disambiguate from.
func TestParse_AgentTopLevel(t *testing.T) {
	t.Parallel()
	const y = `
apiVersion: crewship/v1
kind: Agent
metadata: { name: Alice, slug: alice }
spec:
  crew_slug: my-crew
  role: AGENT
`
	b, err := Load([]byte(y))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Agents) != 1 {
		t.Fatalf("Agents = %d, want 1", len(b.Agents))
	}
	if got := b.Agents[0].Metadata.Slug; got != "alice" {
		t.Errorf("slug = %q, want %q", got, "alice")
	}
}

// TestParse_IntegrationTopLevel confirms `kind: Integration` parses
// into b.Integrations.
func TestParse_IntegrationTopLevel(t *testing.T) {
	t.Parallel()
	const y = `
apiVersion: crewship/v1
kind: Integration
metadata: { name: GitHub, slug: github }
spec:
  crew_slug: my-crew
  type: mcp
`
	b, err := Load([]byte(y))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Integrations) != 1 {
		t.Fatalf("Integrations = %d, want 1", len(b.Integrations))
	}
	if got := b.Integrations[0].Metadata.Slug; got != "github" {
		t.Errorf("slug = %q, want %q", got, "github")
	}
}

// TestParse_MultiDocStream confirms multiple new kinds in one
// stream all land in their respective Bundle slices, alongside
// legacy bundle docs.
func TestParse_MultiDocStream(t *testing.T) {
	t.Parallel()
	const y = `
apiVersion: crewship/v1
kind: Crew
metadata: { name: Legacy, slug: legacy }
spec:
  agents: []
---
apiVersion: crewship/v1
kind: Crew
metadata: { name: Top-level, slug: top-level }
spec:
  description: New shape
---
apiVersion: crewship/v1
kind: Agent
metadata: { name: Bob, slug: bob }
spec:
  crew_slug: legacy
  role: AGENT
---
apiVersion: crewship/v1
kind: Integration
metadata: { name: GitHub, slug: github }
spec:
  crew_slug: legacy
  type: mcp
`
	b, err := Load([]byte(y))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Documents) != 1 {
		t.Errorf("Documents = %d, want 1 (the legacy bundle)", len(b.Documents))
	}
	if len(b.Crews) != 1 {
		t.Errorf("Crews = %d, want 1 (the top-level)", len(b.Crews))
	}
	if len(b.Agents) != 1 {
		t.Errorf("Agents = %d, want 1", len(b.Agents))
	}
	if len(b.Integrations) != 1 {
		t.Errorf("Integrations = %d, want 1", len(b.Integrations))
	}
}

// TestParse_UnknownKindError makes sure the error message lists
// the three newly-recognised kinds so an operator with a typo
// (`kind: Agnet`) sees `Agent` in the suggestion.
func TestParse_UnknownKindError(t *testing.T) {
	t.Parallel()
	const y = `
apiVersion: crewship/v1
kind: Unknown
metadata: { name: x, slug: x }
`
	_, err := Load([]byte(y))
	if err == nil {
		t.Fatal("Load: expected error, got nil")
	}
	for _, want := range []string{"Agent", "Integration", "Crew"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing expected kind %q in suggestion list", err.Error(), want)
		}
	}
}
