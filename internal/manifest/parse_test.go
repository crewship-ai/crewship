package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_SingleCrewDoc(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata:
  name: Test
  slug: test
spec:
  agents:
    - slug: a
      name: A
      agent_role: LEAD
      cli_adapter: CLAUDE_CODE
      prompt: hi
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Documents) != 1 {
		t.Fatalf("want 1 crew doc, got %d", len(b.Documents))
	}
	if got := b.Documents[0].Metadata.Slug; got != "test" {
		t.Errorf("slug = %q, want test", got)
	}
	if got := len(b.Documents[0].Spec.Agents); got != 1 {
		t.Errorf("agents = %d, want 1", got)
	}
}

func TestLoad_MultiDoc(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: One, slug: one }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
---
apiVersion: crewship/v1
kind: Crew
metadata: { name: Two, slug: two }
spec:
  agents:
    - { slug: b, name: B, agent_role: LEAD, prompt: y }
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Documents) != 2 {
		t.Fatalf("want 2 docs, got %d", len(b.Documents))
	}
}

func TestLoad_WorkspaceDoc(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Workspace
metadata: { name: ACME, slug: acme }
spec:
  credentials:
    - { env: ANTHROPIC_API_KEY, provider: ANTHROPIC, type: API_KEY }
  crews:
    - slug: code-review
      name: Code Review
      agents:
        - { slug: daniel, name: Daniel, agent_role: LEAD, prompt: hi }
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Workspaces) != 1 {
		t.Fatalf("want 1 workspace doc, got %d", len(b.Workspaces))
	}
	if got := len(b.Workspaces[0].Spec.Crews); got != 1 {
		t.Fatalf("want 1 crew in workspace, got %d", got)
	}
}

func TestLoad_RejectsUnknownAPIVersion(t *testing.T) {
	_, err := Load([]byte(`
apiVersion: crewship/v999
kind: Crew
metadata: { name: x, slug: x }
spec:
  agents:
    - { slug: a, name: A, prompt: x }
`))
	if err == nil || !strings.Contains(err.Error(), "unsupported apiVersion") {
		t.Errorf("want apiVersion error, got %v", err)
	}
}

func TestLoad_RejectsUnknownKind(t *testing.T) {
	_, err := Load([]byte(`
apiVersion: crewship/v1
kind: Galaxy
metadata: { name: x, slug: x }
`))
	if err == nil || !strings.Contains(err.Error(), "unsupported kind") {
		t.Errorf("want kind error, got %v", err)
	}
}

func TestLoadFile_ResolvesPaths(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "house-style")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	skillBody := "---\nname: house-style\ndescription: x\nlicense: MIT\n---\n# style\nA\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	promptPath := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("you are a tester"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	manifestBody := `
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  skills:
    - { slug: house-style, path: ./skills/house-style/SKILL.md }
  agents:
    - slug: a
      name: A
      agent_role: LEAD
      cli_adapter: CLAUDE_CODE
      prompt_file: ./prompt.md
      skills: [house-style]
`
	manifestPath := filepath.Join(dir, "crew.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifestBody), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	b, err := LoadFile(manifestPath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := b.Documents[0].Spec.Skills[0].Resolved(); !strings.Contains(got, "# style") {
		t.Errorf("skill body not resolved: %q", got)
	}
	if got := b.Documents[0].Spec.Agents[0].Prompt; got != "you are a tester" {
		t.Errorf("prompt body not resolved: %q", got)
	}
	if got := b.Documents[0].Spec.Agents[0].PromptFile; got != "" {
		t.Errorf("PromptFile should be cleared after resolution, got %q", got)
	}
}

func TestLoadFile_RejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(filepath.Dir(dir), "outside.md")
	if err := os.WriteFile(target, []byte("dangerous"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	defer os.Remove(target)

	manifestBody := `
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt_file: ../outside.md }
`
	manifestPath := filepath.Join(dir, "crew.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifestBody), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if _, err := LoadFile(manifestPath); err == nil {
		t.Fatal("expected path-escape error, got nil")
	}
}

func TestLoadFile_RejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	manifestBody := `
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt_file: /etc/passwd }
`
	manifestPath := filepath.Join(dir, "crew.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifestBody), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	_, err := LoadFile(manifestPath)
	if err == nil {
		t.Fatal("expected absolute-path error, got nil")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error doesn't mention absolute path: %v", err)
	}
}

func TestLoadFile_LoadsExampleCrewManifests(t *testing.T) {
	// Anchor each example fixture in the repo so a future move
	// shows up as a test failure rather than silently breaking
	// the docs. The examples directory is intentionally checked
	// in — these are the manifests users will copy-paste from.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("repo root: %v", err) // not a git checkout? fine, skip
	}
	for _, rel := range []string{
		"examples/manifests/code-review.crew.yaml",
		"examples/manifests/triage.crew.yaml",
		"examples/manifests/full-team.workspace.yaml",
	} {
		t.Run(rel, func(t *testing.T) {
			b, err := LoadFile(filepath.Join(repoRoot, rel))
			if err != nil {
				t.Fatalf("LoadFile: %v", err)
			}
			if err := b.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

// findRepoRoot walks up from the test binary's CWD looking for a
// go.mod. Tests run from the package directory, so this routinely
// climbs two levels (internal/manifest → repo root).
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}
