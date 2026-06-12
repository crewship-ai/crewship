package manifest

// Second coverage pass for parse.go: per-kind decode-error arms in
// Load's dispatcher, LoadFile read failures, the resolveInlineOnly /
// resolveLocalReferences error branches, safeJoin's symlink-eval
// fallback and the crewSpecHasNestedSubresources node-shape guards.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestLoad_DecodeErrorPerKindArm drives the type-mismatch decode error
// of every kind arm in Load's dispatcher: metadata.name is a mapping,
// which can never unmarshal into the string field, so the per-kind
// "decode <Kind> document" wrap must surface.
func TestLoad_DecodeErrorPerKindArm(t *testing.T) {
	kinds := []struct {
		kind    string
		extra   string // extra spec body needed to steer dispatch
		wantErr string
	}{
		{"Crew", "spec: { agents: [] }", "decode Crew bundle document"},
		{"Crew", "spec: { description: d }", "decode top-level Crew document"},
		{"Agent", "", "decode Agent document"},
		{"Integration", "", "decode Integration document"},
		{"Workspace", "", "decode Workspace document"},
		{"Project", "", "decode Project document"},
		{"Label", "", "decode Label document"},
		{"Milestone", "", "decode Milestone document"},
		{"WorkflowTemplate", "", "decode WorkflowTemplate document"},
		{"TriageRule", "", "decode TriageRule document"},
		{"RecurringIssue", "", "decode RecurringIssue document"},
		{"SavedView", "", "decode SavedView document"},
		{"Routine", "", "decode Routine document"},
		{"FeatureFlag", "", "decode FeatureFlag document"},
		{"InstanceSetting", "", "decode InstanceSetting document"},
		{"Recipe", "", "decode Recipe document"},
		{"CrewTemplate", "", "decode CrewTemplate document"},
		{"Connector", "", "decode Connector document"},
		{"Hook", "", "decode Hook document"},
		{"Skill", "", "decode Skill document"},
		{"Issue", "", "decode Issue document"},
	}
	for _, tc := range kinds {
		t.Run(tc.wantErr, func(t *testing.T) {
			body := "apiVersion: crewship/v1\nkind: " + tc.kind + "\nmetadata: { name: { bad: structure }, slug: x }\n"
			if tc.extra != "" {
				body += tc.extra + "\n"
			}
			_, err := Load([]byte(body))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("kind %s: want error containing %q, got %v", tc.kind, tc.wantErr, err)
			}
		})
	}
}

func TestLoad_HeadDecodeError(t *testing.T) {
	_, err := Load([]byte("apiVersion: [not, a, string]\nkind: Crew\n"))
	if err == nil || !strings.Contains(err.Error(), "read apiVersion/kind") {
		t.Fatalf("want apiVersion/kind decode error, got %v", err)
	}
}

func TestLoadFile_InvalidYAMLPropagatesLoadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: crewship/v1\nkind: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "parse manifest yaml") {
		t.Fatalf("want yaml parse error from LoadFile, got %v", err)
	}
}

func TestLoadFile_DirectoryReadError(t *testing.T) {
	_, err := LoadFile(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Fatalf("want read error for directory path, got %v", err)
	}
}

func TestReadSkillFile_DirectoryReadError(t *testing.T) {
	_, err := readSkillFile(t.TempDir())
	if err == nil {
		t.Fatal("reading a directory as a skill file must error")
	}
}

func TestReadPromptFile_DirectoryReadError(t *testing.T) {
	_, err := readPromptFile(t.TempDir())
	if err == nil {
		t.Fatal("reading a directory as a prompt file must error")
	}
}

func TestResolveInlineOnly_NilSpecDocumentSkipped(t *testing.T) {
	b := &Bundle{Documents: []Document{{Metadata: Metadata{Slug: "ghost"}}}}
	if err := b.resolveInlineOnly(); err != nil {
		t.Fatalf("nil-spec document must be skipped, got %v", err)
	}
}

func TestResolveInlineOnly_WorkspaceSkillConflicts(t *testing.T) {
	t.Run("workspace-level skill with two sources", func(t *testing.T) {
		b := &Bundle{Workspaces: []WorkspaceDocument{{
			Spec: WorkspaceSpec{Skills: []Skill{{Slug: "dup", Path: "a.md", Inline: "body"}}},
		}}}
		err := b.resolveInlineOnly()
		if err == nil || !strings.Contains(err.Error(), `skill "dup": only one of path/source/inline allowed`) {
			t.Fatalf("want one-source violation, got %v", err)
		}
	})
	t.Run("workspace crew skill with no source", func(t *testing.T) {
		b := &Bundle{Workspaces: []WorkspaceDocument{{
			Spec: WorkspaceSpec{Crews: []CrewSpec{{
				SlugOverride: "ops",
				Skills:       []Skill{{Slug: "empty"}},
			}}},
		}}}
		err := b.resolveInlineOnly()
		if err == nil || !strings.Contains(err.Error(), `skill "empty": must have one of path/source/inline`) {
			t.Fatalf("want missing-source violation, got %v", err)
		}
	})
}

func TestResolveLocalReferences_NilSpecDocumentSkipped(t *testing.T) {
	b := &Bundle{
		SourcePath: filepath.Join(t.TempDir(), "m.yaml"),
		Documents:  []Document{{Metadata: Metadata{Slug: "ghost"}}},
	}
	if err := b.resolveLocalReferences(); err != nil {
		t.Fatalf("nil-spec document must be skipped, got %v", err)
	}
}

// writeManifest writes the manifest body into a temp dir and returns
// the file path, so LoadFile-based reference resolution has an anchor.
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	// Resolve the tempdir's symlinks up front (macOS tempdirs live
	// under /var → /private/var): safeJoin compares the symlink-
	// resolved base against a lexical fallback for not-yet-existing
	// targets, and an unresolved base would falsely flag an escape.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFile_ReferenceResolutionErrors(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{
			name: "legacy crew skill path escapes dir",
			manifest: `
apiVersion: crewship/v1
kind: Crew
metadata: { name: Ops, slug: ops }
spec:
  agents:
    - { slug: amy, name: Amy, prompt: p }
  skills:
    - { slug: esc, path: ../outside.md }
`,
			wantErr: `skill "esc": path escapes manifest directory`,
		},
		{
			name: "legacy crew skill path missing file",
			manifest: `
apiVersion: crewship/v1
kind: Crew
metadata: { name: Ops, slug: ops }
spec:
  agents:
    - { slug: amy, name: Amy, prompt: p }
  skills:
    - { slug: gone, path: missing/SKILL.md }
`,
			wantErr: `skill "gone": read missing/SKILL.md`,
		},
		{
			name: "workspace-level skill path missing file",
			manifest: `
apiVersion: crewship/v1
kind: Workspace
metadata: { name: WS, slug: ws }
spec:
  skills:
    - { slug: wsgone, path: nope.md }
  crews: []
`,
			wantErr: `skill "wsgone": read nope.md`,
		},
		{
			name: "workspace crew skill path missing file",
			manifest: `
apiVersion: crewship/v1
kind: Workspace
metadata: { name: WS, slug: ws }
spec:
  crews:
    - slug: ops
      name: Ops
      agents:
        - { slug: amy, name: Amy, prompt: p }
      skills:
        - { slug: cgone, path: nope.md }
`,
			wantErr: `skill "cgone": read nope.md`,
		},
		{
			name: "workspace crew agent prompt_file missing",
			manifest: `
apiVersion: crewship/v1
kind: Workspace
metadata: { name: WS, slug: ws }
spec:
  crews:
    - slug: ops
      name: Ops
      agents:
        - { slug: amy, name: Amy, prompt_file: prompts/amy.md }
`,
			wantErr: `agent "amy": read prompt_file prompts/amy.md`,
		},
		{
			name: "top-level Skill path missing file",
			manifest: `
apiVersion: crewship/v1
kind: Skill
metadata: { name: gone, slug: gone }
spec: { description: d, path: skills/gone/SKILL.md }
`,
			wantErr: `Skill "gone": read skills/gone/SKILL.md`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFile(writeManifest(t, tc.manifest))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadFile_TopLevelSkillSourceURLDeferred(t *testing.T) {
	path := writeManifest(t, `
apiVersion: crewship/v1
kind: Skill
metadata: { name: remote, slug: remote }
spec:
  description: d
  source: https://github.com/example/skills/remote
`)
	bundle, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(bundle.Skills) != 1 {
		t.Fatalf("want 1 top-level skill, got %d", len(bundle.Skills))
	}
	// URL skills are fetched at apply time; load must leave the body
	// unresolved instead of erroring.
	if got := bundle.Skills[0].Resolved(); got != "" {
		t.Errorf("URL-sourced skill must stay unresolved at load, got %q", got)
	}
}

func TestSafeJoin_NonexistentBaseFallsBackLexically(t *testing.T) {
	base := filepath.Join(t.TempDir(), "does-not-exist")
	got, err := safeJoin(base, "file.md")
	if err != nil {
		t.Fatalf("safeJoin with nonexistent base must fall back lexically, got %v", err)
	}
	if got != filepath.Join(base, "file.md") {
		t.Errorf("safeJoin = %q, want %q", got, filepath.Join(base, "file.md"))
	}
}

func TestCrewSpecHasNestedSubresources_NodeShapes(t *testing.T) {
	t.Run("empty document node", func(t *testing.T) {
		got, err := crewSpecHasNestedSubresources(&yaml.Node{Kind: yaml.DocumentNode})
		if err != nil || got {
			t.Fatalf("empty document node: got (%v, %v), want (false, nil)", got, err)
		}
	})
	t.Run("non-mapping root errors", func(t *testing.T) {
		_, err := crewSpecHasNestedSubresources(&yaml.Node{Kind: yaml.ScalarNode, Value: "x"})
		if err == nil || !strings.Contains(err.Error(), "expected mapping at document root") {
			t.Fatalf("want mapping-root error, got %v", err)
		}
	})
	t.Run("scalar spec is not a legacy bundle", func(t *testing.T) {
		var raw yaml.Node
		if err := yaml.Unmarshal([]byte("kind: Crew\nspec: just-a-string\n"), &raw); err != nil {
			t.Fatal(err)
		}
		got, err := crewSpecHasNestedSubresources(&raw)
		if err != nil || got {
			t.Fatalf("scalar spec: got (%v, %v), want (false, nil)", got, err)
		}
	})
}
