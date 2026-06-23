package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covTempDir returns a symlink-resolved temp dir. On macOS t.TempDir()
// lives under /var → /private/var; safeJoin resolves symlinks on the
// base but falls back to the lexical path for non-existent targets,
// so an unresolved base makes every missing-file path look like an
// escape. Resolving up front keeps the tests focused on the behaviour
// under test instead of the platform's temp-dir layout.
func covTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return dir
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestReadSkillFile(t *testing.T) {
	dir := t.TempDir()
	t.Run("ok", func(t *testing.T) {
		p := filepath.Join(dir, "SKILL.md")
		writeFileT(t, p, "# skill body")
		got, err := readSkillFile(p)
		if err != nil || got != "# skill body" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		if _, err := readSkillFile(filepath.Join(dir, "nope.md")); err == nil {
			t.Fatal("want error for missing file")
		}
	})
	t.Run("over limit", func(t *testing.T) {
		p := filepath.Join(dir, "big.md")
		writeFileT(t, p, strings.Repeat("x", maxSkillFileBytes+1))
		_, err := readSkillFile(p)
		if err == nil || !strings.Contains(err.Error(), "byte limit") {
			t.Fatalf("want byte-limit error, got %v", err)
		}
	})
}

func TestReadPromptFile(t *testing.T) {
	dir := t.TempDir()
	t.Run("ok", func(t *testing.T) {
		p := filepath.Join(dir, "PROMPT.md")
		writeFileT(t, p, "be helpful")
		got, err := readPromptFile(p)
		if err != nil || got != "be helpful" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		if _, err := readPromptFile(filepath.Join(dir, "nope.md")); err == nil {
			t.Fatal("want error for missing file")
		}
	})
	t.Run("over limit", func(t *testing.T) {
		p := filepath.Join(dir, "big.md")
		writeFileT(t, p, strings.Repeat("x", maxPromptBytes+1))
		_, err := readPromptFile(p)
		if err == nil || !strings.Contains(err.Error(), "byte limit") {
			t.Fatalf("want byte-limit error, got %v", err)
		}
	})
}

func TestLoad_ErrorShapes(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"empty", "   \n  ", "manifest is empty"},
		{"bad yaml", "apiVersion: [unclosed", "parse manifest yaml"},
		{"missing kind", "apiVersion: crewship/v1\nmetadata: { name: X, slug: x }\n", "missing kind"},
		{"unsupported kind", "apiVersion: crewship/v1\nkind: Spaceship\nmetadata: { slug: x }\n", `unsupported kind "Spaceship"`},
		{"unsupported apiVersion", "apiVersion: crewship/v9\nkind: Crew\nmetadata: { slug: x }\n", "unsupported apiVersion"},
		{"only comments", "# nothing\n", "no documents in manifest"},
		{"trailing null document", "apiVersion: crewship/v1\nkind: Hook\nmetadata: { name: H, slug: h }\nspec: {}\n---\n", `unsupported apiVersion ""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoad_AllNewKindArms(t *testing.T) {
	docs := []string{
		"kind: Milestone\nmetadata: { name: M, slug: m }\nspec: { project_slug: p }",
		"kind: WorkflowTemplate\nmetadata: { name: W, slug: w }\nspec: {}",
		"kind: TriageRule\nmetadata: { name: T, slug: t-rule }\nspec: {}",
		"kind: RecurringIssue\nmetadata: { name: R, slug: r }\nspec: {}",
		"kind: SavedView\nmetadata: { name: S, slug: s }\nspec: {}",
		"kind: Routine\nmetadata: { name: Rt, slug: rt }\nspec: {}",
		"kind: FeatureFlag\nmetadata: { name: F, slug: f }\nspec: {}",
		"kind: InstanceSetting\nmetadata: { name: I, slug: i }\nspec: {}",
		"kind: Recipe\nmetadata: { name: Rc, slug: rc }\nspec: {}",
		"kind: CrewTemplate\nmetadata: { name: Ct, slug: ct }\nspec: {}",
		"kind: Connector\nmetadata: { name: Cn, slug: cn }\nspec: {}",
		"kind: Hook\nmetadata: { name: H, slug: h }\nspec: {}",
		"kind: Skill\nmetadata: { name: Sk, slug: sk }\nspec: { inline: \"---\\nname: sk\\n---\\nbody\" }",
		"kind: Issue\nmetadata: { name: Is, slug: is }\nspec: { crew_slug: c }",
		"kind: Workspace\nmetadata: { name: Ws, slug: ws }\nspec: { crews: [] }",
	}
	var sb strings.Builder
	for i, d := range docs {
		if i > 0 {
			sb.WriteString("---\n")
		}
		sb.WriteString("apiVersion: crewship/v1\n")
		sb.WriteString(d)
		sb.WriteString("\n")
	}

	b, err := Load([]byte(sb.String()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	checks := []struct {
		name string
		got  int
	}{
		{"Milestones", len(b.Milestones)},
		{"WorkflowTemplates", len(b.WorkflowTemplates)},
		{"TriageRules", len(b.TriageRules)},
		{"RecurringIssues", len(b.RecurringIssues)},
		{"SavedViews", len(b.SavedViews)},
		{"Routines", len(b.Routines)},
		{"FeatureFlags", len(b.FeatureFlags)},
		{"InstanceSettings", len(b.InstanceSettings)},
		{"Recipes", len(b.Recipes)},
		{"CrewTemplates", len(b.CrewTemplates)},
		{"Connectors", len(b.Connectors)},
		{"Hooks", len(b.Hooks)},
		{"Skills", len(b.Skills)},
		{"Issues", len(b.Issues)},
		{"Workspaces", len(b.Workspaces)},
	}
	for _, c := range checks {
		if c.got != 1 {
			t.Errorf("%s = %d, want 1", c.name, c.got)
		}
	}
}

func TestLoadFile_Errors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		_, err := LoadFile(filepath.Join(t.TempDir(), "nope.yaml"))
		if err == nil || !strings.Contains(err.Error(), "read manifest") {
			t.Fatalf("want read error, got %v", err)
		}
	})
	t.Run("over manifest limit", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "big.yaml")
		writeFileT(t, p, "# pad\n"+strings.Repeat("x", maxManifestBytes))
		_, err := LoadFile(p)
		if err == nil || !strings.Contains(err.Error(), "byte limit") {
			t.Fatalf("want byte-limit error, got %v", err)
		}
	})
}

func TestLoadFile_ResolvesTopLevelSkillKind(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "skills", "house", "SKILL.md"),
		"---\nname: house\ndescription: x\n---\nthe house body")
	manifest := `apiVersion: crewship/v1
kind: Skill
metadata: { name: House, slug: house }
spec:
  path: ./skills/house/SKILL.md
`
	p := filepath.Join(dir, "crewship.yaml")
	writeFileT(t, p, manifest)
	b, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(b.Skills) != 1 {
		t.Fatalf("Skills = %d", len(b.Skills))
	}
	if got := b.Skills[0].Resolved(); !strings.Contains(got, "the house body") {
		t.Errorf("path body not resolved: %q", got)
	}
}

func TestLoadFile_TopLevelSkillInlineMirroredToResolved(t *testing.T) {
	dir := t.TempDir()
	manifest := `apiVersion: crewship/v1
kind: Skill
metadata: { name: H, slug: h }
spec:
  inline: "---\nname: h\n---\ninline body"
`
	p := filepath.Join(dir, "crewship.yaml")
	writeFileT(t, p, manifest)
	b, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := b.Skills[0].Resolved(); !strings.Contains(got, "inline body") {
		t.Errorf("inline body not mirrored to resolved: %q", got)
	}
}

func TestLoadFile_TopLevelSkillPathEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	manifest := `apiVersion: crewship/v1
kind: Skill
metadata: { name: H, slug: h }
spec:
  path: ../outside/SKILL.md
`
	p := filepath.Join(dir, "crewship.yaml")
	writeFileT(t, p, manifest)
	_, err := LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "escapes manifest directory") {
		t.Fatalf("want path-escape error, got %v", err)
	}
}

func TestResolveLocalReferences_PromptRules(t *testing.T) {
	t.Run("prompt and prompt_file both set", func(t *testing.T) {
		b := &Bundle{
			SourcePath: filepath.Join(t.TempDir(), "m.yaml"),
			Documents: []Document{{
				Metadata: Metadata{Slug: "t"},
				Spec: &CrewSpec{Agents: []Agent{
					{Slug: "a", Name: "A", Prompt: "x", PromptFile: "./p.md"},
				}},
			}},
		}
		err := b.resolveLocalReferences()
		if err == nil || !strings.Contains(err.Error(), "cannot set both prompt and prompt_file") {
			t.Fatalf("want both-set error, got %v", err)
		}
	})
	t.Run("prompt over limit", func(t *testing.T) {
		b := &Bundle{
			SourcePath: filepath.Join(t.TempDir(), "m.yaml"),
			Documents: []Document{{
				Metadata: Metadata{Slug: "t"},
				Spec: &CrewSpec{Agents: []Agent{
					{Slug: "a", Name: "A", Prompt: strings.Repeat("x", maxPromptBytes+1)},
				}},
			}},
		}
		err := b.resolveLocalReferences()
		if err == nil || !strings.Contains(err.Error(), "prompt body is") {
			t.Fatalf("want prompt-size error, got %v", err)
		}
	})
	t.Run("prompt_file resolves and clears", func(t *testing.T) {
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "p.md"), "from file")
		b := &Bundle{
			SourcePath: filepath.Join(dir, "m.yaml"),
			Documents: []Document{{
				Metadata: Metadata{Slug: "t"},
				Spec: &CrewSpec{Agents: []Agent{
					{Slug: "a", Name: "A", PromptFile: "./p.md"},
				}},
			}},
		}
		if err := b.resolveLocalReferences(); err != nil {
			t.Fatalf("resolveLocalReferences: %v", err)
		}
		a := b.Documents[0].Spec.Agents[0]
		if a.Prompt != "from file" || a.PromptFile != "" {
			t.Errorf("prompt_file resolution wrong: %+v", a)
		}
	})
	t.Run("prompt_file read failure", func(t *testing.T) {
		b := &Bundle{
			SourcePath: filepath.Join(covTempDir(t), "m.yaml"),
			Documents: []Document{{
				Metadata: Metadata{Slug: "t"},
				Spec: &CrewSpec{Agents: []Agent{
					{Slug: "a", Name: "A", PromptFile: "./missing.md"},
				}},
			}},
		}
		err := b.resolveLocalReferences()
		if err == nil || !strings.Contains(err.Error(), "read prompt_file") {
			t.Fatalf("want read error, got %v", err)
		}
	})
	t.Run("no source path is a no-op", func(t *testing.T) {
		b := &Bundle{
			Documents: []Document{{
				Spec: &CrewSpec{Agents: []Agent{{Slug: "a", PromptFile: "./missing.md"}}},
			}},
		}
		if err := b.resolveLocalReferences(); err != nil {
			t.Fatalf("inline-only bundle must skip resolution: %v", err)
		}
	})
}

func TestResolveLocalReferences_WorkspaceNested(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "skills", "s", "SKILL.md"), "---\nname: s\n---\nnested body")
	writeFileT(t, filepath.Join(dir, "prompt.md"), "nested prompt")
	manifest := `apiVersion: crewship/v1
kind: Workspace
metadata: { name: W, slug: w }
spec:
  skills:
    - { slug: ws-s, path: ./skills/s/SKILL.md }
  crews:
    - slug: alpha
      name: Alpha
      skills:
        - { slug: crew-s, path: ./skills/s/SKILL.md }
      agents:
        - { slug: a, name: A, agent_role: LEAD, prompt_file: ./prompt.md }
`
	p := filepath.Join(dir, "crewship.yaml")
	writeFileT(t, p, manifest)
	b, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	ws := b.Workspaces[0]
	if got := ws.Spec.Skills[0].Resolved(); !strings.Contains(got, "nested body") {
		t.Errorf("workspace skill not resolved: %q", got)
	}
	crew := ws.Spec.Crews[0]
	if got := crew.Skills[0].Resolved(); !strings.Contains(got, "nested body") {
		t.Errorf("crew skill not resolved: %q", got)
	}
	if crew.Agents[0].Prompt != "nested prompt" {
		t.Errorf("agent prompt_file not resolved: %q", crew.Agents[0].Prompt)
	}
}

func TestSafeJoin_EdgeCases(t *testing.T) {
	base := covTempDir(t)
	t.Run("absolute rejected", func(t *testing.T) {
		_, err := safeJoin(base, "/etc/passwd")
		if err == nil || !strings.Contains(err.Error(), "absolute paths not allowed") {
			t.Fatalf("want absolute-path error, got %v", err)
		}
	})
	t.Run("dotdot prefix rejected", func(t *testing.T) {
		_, err := safeJoin(base, "../outside")
		if err == nil || !strings.Contains(err.Error(), "escapes manifest directory") {
			t.Fatalf("want escape error, got %v", err)
		}
	})
	t.Run("embedded dotdot rejected", func(t *testing.T) {
		_, err := safeJoin(base, "a/../../outside")
		if err == nil || !strings.Contains(err.Error(), "escapes manifest directory") {
			t.Fatalf("want escape error, got %v", err)
		}
	})
	t.Run("clean relative accepted", func(t *testing.T) {
		got, err := safeJoin(base, "sub/file.md")
		if err != nil {
			t.Fatalf("safeJoin: %v", err)
		}
		if !strings.HasPrefix(got, base) {
			t.Errorf("joined path %q should stay under base %q", got, base)
		}
	})
	t.Run("interior dotdot that stays inside is allowed", func(t *testing.T) {
		if _, err := safeJoin(base, "a/../b.md"); err != nil {
			t.Fatalf("a/../b.md cleans to b.md and is safe, got %v", err)
		}
	})
}

func TestMappingValueByKey_NonMapping(t *testing.T) {
	if got := mappingValueByKey(nil, "x"); got != nil {
		t.Errorf("nil node should return nil, got %v", got)
	}
}
