package manifest

import (
	"context"
	"strings"
	"testing"
)

// env_refs may name a credential created outside the manifest (device keys
// are kept out of Git). The plan resolves it against the server: present →
// one link planned; absent → the dry-run fails naming the fix.
func TestBuildPlan_UndeclaredEnvRefResolvesAgainstServer(t *testing.T) {
	for _, tc := range []struct {
		name, creds string
		wantErr     bool
	}{
		{"exists on the server", `[{"id":"crDev","name":"DEVICE_SSH_KEY","type":"SSH_KEY","scope":"CREW"}]`, false},
		{"exists nowhere", `[]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, cli_adapter: CLAUDE_CODE, tool_profile: CODING, timeout_seconds: 1800, prompt: x, env_refs: [DEVICE_SSH_KEY] }
`))
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			stub := newCovStub()
			stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"t","name":"T"}]`)
			stub.on("GET", "/api/v1/credentials", 200, tc.creds)
			stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[]`)
			stub.on("GET", "/api/v1/agents?crew_id=c1", 200,
				`[{"id":"a1","slug":"a","name":"A","agent_role":"LEAD","cli_adapter":"CLAUDE_CODE","tool_profile":"CODING","timeout_seconds":1800,"system_prompt":"x"}]`)
			stub.on("GET", "/api/v1/agents/a1/skills", 200, `[]`)
			stub.on("GET", "/api/v1/agents/a1/credentials", 200, `[]`)
			stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 200, `[]`)

			plan, err := BuildPlan(context.Background(), NewClient(stub), b, Options{Mode: ApplyUpsert, NoDelete: true})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "crewship credential create DEVICE_SSH_KEY") {
					t.Fatalf("err = %v, want a plan-time error naming the fix", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			var links int
			for _, it := range plan.Items {
				if it.Kind == "agent_credential" && it.Action == ActionCreate {
					links++
				}
			}
			if links != 1 {
				t.Fatalf("planned %d credential links, want 1", links)
			}
		})
	}
}

// The import route refuses a SKILL.md whose frontmatter does not parse;
// validation (and so --dry-run) must refuse it first.
func TestValidate_SkillFrontmatterIsPreflighted(t *testing.T) {
	for _, tc := range []struct {
		name, inline string
		wantErr      bool
	}{
		{"valid frontmatter", "---\nname: ssh-probe\ndescription: Probe a device over SSH\n---\nBody\n", false},
		{"missing frontmatter", "# ssh-probe\nno frontmatter here\n", true},
		{"unterminated frontmatter", "---\nname: ssh-probe\ndescription: x\nBody\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "apiVersion: crewship/v1\nkind: Crew\nmetadata: { name: T, slug: t }\nspec:\n  skills:\n    - slug: ssh-probe\n      inline: |\n"
			for _, line := range strings.Split(strings.TrimRight(tc.inline, "\n"), "\n") {
				doc += "        " + line + "\n"
			}
			doc += "  agents:\n    - { slug: a, name: A, agent_role: LEAD, prompt: x, skills: [ssh-probe] }\n"
			b, err := Load([]byte(doc))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			err = b.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "would be refused by the server") {
				t.Fatalf("error must say why: %v", err)
			}
		})
	}
}
