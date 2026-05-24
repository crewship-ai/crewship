package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/manifest/kinds"
)

// Defensive size caps. These match the skills package's
// maxSkillFileBytes (512 KB) for parity with the URL-import path,
// plus a tighter 8 KB ceiling specifically for inline blocks that
// the manifest carries verbatim. Manifest files themselves get a
// 4 MB cap so a runaway YAML can't OOM the apply CLI.
const (
	maxManifestBytes    = 4 << 20   // 4 MB — covers any realistic workspace bundle
	maxSkillFileBytes   = 512 << 10 // 512 KB — mirrors internal/skills cap
	maxInlineSkillBytes = 8 << 10   // 8 KB — keep inline blocks short, force path: for big skills
	maxPromptBytes      = 64 << 10  // 64 KB — generous for a system prompt; full books belong in skills
)

// readSkillFile reads a SKILL.md file with the same byte cap the
// skills package enforces for URL imports. Surfacing the error at
// load time means a malformed checkout can't balloon memory or
// silently truncate the skill body.
func readSkillFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSkillFileBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxSkillFileBytes {
		return "", fmt.Errorf("file exceeds %d-byte limit", maxSkillFileBytes)
	}
	return string(data), nil
}

// readPromptFile mirrors readSkillFile with the prompt cap. Prompts
// are usually small (a few hundred lines of markdown); a 1 MB
// prompt is almost always a mistake or a binary blob mislabelled
// as text.
func readPromptFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPromptBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxPromptBytes {
		return "", fmt.Errorf("file exceeds %d-byte limit", maxPromptBytes)
	}
	return string(data), nil
}

// Bundle is the parsed-and-resolved result of loading a manifest
// file. It carries the path of the source file so subsequent path:
// and prompt_file: references can be resolved relative to it.
//
// Documents holds one entry per `---`-separated YAML doc in the
// source. A typical file has exactly one entry; a workspace bundle
// with extra crew overlays has more.
// isEmpty reports whether the bundle has zero documents across every
// kind slice. Used by Load to refuse manifests that parse cleanly but
// declare nothing. Must list every populated slice on Bundle — adding
// a kind without updating isEmpty would silently regress to "single-
// new-kind manifest errors with 'no documents'".
func (b *Bundle) isEmpty() bool {
	return len(b.Documents) == 0 &&
		len(b.Workspaces) == 0 &&
		len(b.Projects) == 0 &&
		len(b.Labels) == 0 &&
		len(b.Milestones) == 0 &&
		len(b.WorkflowTemplates) == 0 &&
		len(b.TriageRules) == 0 &&
		len(b.RecurringIssues) == 0 &&
		len(b.SavedViews) == 0 &&
		len(b.Routines) == 0 &&
		len(b.FeatureFlags) == 0 &&
		len(b.InstanceSettings) == 0 &&
		len(b.Recipes) == 0 &&
		len(b.CrewTemplates) == 0 &&
		len(b.Connectors) == 0 &&
		len(b.Hooks) == 0 &&
		len(b.Skills) == 0
}

type Bundle struct {
	SourcePath string
	Documents  []Document
	Workspaces []WorkspaceDocument

	// SPEC-2 kinds — per-kind slices populated by Load when the
	// parser encounters one of the new top-level kinds. The kinds
	// package owns each doc shape; this package only routes by
	// apiVersion+kind.
	Projects          []kinds.ProjectDocument
	Labels            []kinds.LabelDocument
	Milestones        []kinds.MilestoneDocument
	WorkflowTemplates []kinds.WorkflowTemplateDocument
	TriageRules       []kinds.TriageRuleDocument
	RecurringIssues   []kinds.RecurringIssueDocument
	SavedViews        []kinds.SavedViewDocument
	Routines          []kinds.RoutineDocument
	FeatureFlags      []kinds.FeatureFlagDocument
	InstanceSettings  []kinds.InstanceSettingDocument
	Recipes           []kinds.RecipeDocument
	CrewTemplates     []kinds.CrewTemplateDocument
	Connectors        []kinds.ConnectorDocument
	Hooks             []kinds.HookDocument

	// Skills are workspace-scoped SKILL.md documents authored
	// declaratively via `kind: Skill`. NOT the same as the legacy
	// nested Skills in a Crew/Workspace spec (those are an embedded
	// shape with their own path-resolution and are kept on the
	// existing Crew/Workspace document types). The new top-level
	// kind plugs into the SPEC-2 dispatcher and survives apply
	// without crossing through the legacy bundle parser.
	Skills []kinds.SkillDocument
}

// LoadFile reads a manifest file and returns the parsed bundle with
// inline / path-referenced skill bodies and prompt files already
// resolved. URL-sourced skills are NOT fetched here — that happens
// in the apply path so a validate-only pass can run without network.
func LoadFile(path string) (*Bundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %q: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read manifest %q: %w", path, err)
	}
	if int64(len(data)) > maxManifestBytes {
		return nil, fmt.Errorf("manifest %q exceeds %d-byte limit", path, maxManifestBytes)
	}
	bundle, err := Load(data)
	if err != nil {
		return nil, err
	}
	bundle.SourcePath = path
	if err := bundle.resolveLocalReferences(); err != nil {
		return nil, err
	}
	return bundle, nil
}

// Load parses raw manifest bytes (YAML or JSON; JSON is a YAML 1.2
// subset). Multi-doc YAML is supported via `---` separators — each
// document is independently typed by its top-level apiVersion+kind.
//
// Returns a Bundle with SourcePath unset; callers that want path
// resolution should either call LoadFile or set SourcePath
// themselves and call ResolveLocalReferences.
func Load(data []byte) (*Bundle, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("manifest is empty")
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(false) // tolerate forward-compat fields with a warning later
	out := &Bundle{}
	for {
		// Peek as a raw node first so we can read apiVersion/kind
		// before committing to a specific Go type — YAML can't
		// dispatch by sibling field, so this is the canonical
		// pattern for kubectl-style discriminated unions.
		var raw yaml.Node
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse manifest yaml: %w", err)
		}
		if raw.Kind == 0 {
			continue // empty document between separators is harmless
		}

		var head struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
		}
		if err := raw.Decode(&head); err != nil {
			return nil, fmt.Errorf("read apiVersion/kind: %w", err)
		}
		if head.APIVersion != APIVersion {
			return nil, fmt.Errorf("unsupported apiVersion %q (want %q)", head.APIVersion, APIVersion)
		}

		switch head.Kind {
		case KindCrew:
			var doc Document
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode Crew document: %w", err)
			}
			out.Documents = append(out.Documents, doc)
		case KindWorkspace:
			var doc WorkspaceDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode Workspace document: %w", err)
			}
			out.Workspaces = append(out.Workspaces, doc)
		case KindProject:
			var doc kinds.ProjectDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode Project document: %w", err)
			}
			out.Projects = append(out.Projects, doc)
		case KindLabel:
			var doc kinds.LabelDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode Label document: %w", err)
			}
			out.Labels = append(out.Labels, doc)
		case KindMilestone:
			var doc kinds.MilestoneDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode Milestone document: %w", err)
			}
			out.Milestones = append(out.Milestones, doc)
		case KindWorkflowTemplate:
			var doc kinds.WorkflowTemplateDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode WorkflowTemplate document: %w", err)
			}
			out.WorkflowTemplates = append(out.WorkflowTemplates, doc)
		case KindTriageRule:
			var doc kinds.TriageRuleDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode TriageRule document: %w", err)
			}
			out.TriageRules = append(out.TriageRules, doc)
		case KindRecurringIssue:
			var doc kinds.RecurringIssueDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode RecurringIssue document: %w", err)
			}
			out.RecurringIssues = append(out.RecurringIssues, doc)
		case KindSavedView:
			var doc kinds.SavedViewDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode SavedView document: %w", err)
			}
			out.SavedViews = append(out.SavedViews, doc)
		case KindRoutine:
			var doc kinds.RoutineDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode Routine document: %w", err)
			}
			out.Routines = append(out.Routines, doc)
		case KindFeatureFlag:
			var doc kinds.FeatureFlagDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode FeatureFlag document: %w", err)
			}
			out.FeatureFlags = append(out.FeatureFlags, doc)
		case KindInstanceSetting:
			var doc kinds.InstanceSettingDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode InstanceSetting document: %w", err)
			}
			out.InstanceSettings = append(out.InstanceSettings, doc)
		case KindRecipe:
			var doc kinds.RecipeDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode Recipe document: %w", err)
			}
			out.Recipes = append(out.Recipes, doc)
		case KindCrewTemplate:
			var doc kinds.CrewTemplateDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode CrewTemplate document: %w", err)
			}
			out.CrewTemplates = append(out.CrewTemplates, doc)
		case KindConnector:
			var doc kinds.ConnectorDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode Connector document: %w", err)
			}
			out.Connectors = append(out.Connectors, doc)
		case KindHook:
			var doc kinds.HookDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode Hook document: %w", err)
			}
			out.Hooks = append(out.Hooks, doc)
		case KindSkill:
			var doc kinds.SkillDocument
			if err := raw.Decode(&doc); err != nil {
				return nil, fmt.Errorf("decode Skill document: %w", err)
			}
			// Inline body lands in spec.inline directly; path: bodies
			// get pulled in below in resolveLocalReferences along with
			// the legacy nested Skills. URL skills are deferred to
			// apply-time so a validate-only pass stays offline.
			out.Skills = append(out.Skills, doc)
		case "":
			return nil, errors.New("missing kind: (expected one of: Crew, Workspace, Project, Label, Milestone, WorkflowTemplate, TriageRule, RecurringIssue, SavedView, Routine, FeatureFlag, InstanceSetting, Recipe, CrewTemplate, Connector, Hook, Skill)")
		default:
			return nil, fmt.Errorf("unsupported kind %q (expected one of: Crew, Workspace, Project, Label, Milestone, WorkflowTemplate, TriageRule, RecurringIssue, SavedView, Routine, FeatureFlag, InstanceSetting, Recipe, CrewTemplate, Connector, Hook, Skill)", head.Kind)
		}
	}

	if out.isEmpty() {
		return nil, errors.New("no documents in manifest")
	}
	// Resolve inline content immediately. path: and prompt_file:
	// stay deferred — they need a file-system anchor that only
	// LoadFile can provide. This split exists so SDK callers that
	// pass raw bytes still get fully-populated inline skills.
	if err := out.resolveInlineOnly(); err != nil {
		return nil, err
	}
	return out, nil
}

// resolveInlineOnly populates Skill.resolved for every skill that
// uses the `inline:` source, and validates the one-source-per-skill
// invariant. Path-based skills and prompt files are left untouched
// — those need a SourcePath which only LoadFile sets.
func (b *Bundle) resolveInlineOnly() error {
	check := func(slug, path, source, inline string) error {
		count := 0
		if path != "" {
			count++
		}
		if source != "" {
			count++
		}
		if inline != "" {
			count++
		}
		if count == 0 {
			return fmt.Errorf("skill %q: must have one of path/source/inline", slug)
		}
		if count > 1 {
			return fmt.Errorf("skill %q: only one of path/source/inline allowed", slug)
		}
		return nil
	}
	resolveOne := func(s *Skill) error {
		if err := check(s.Slug, s.Path, s.Source, s.Inline); err != nil {
			return err
		}
		if s.Inline != "" {
			if len(s.Inline) > maxInlineSkillBytes {
				return fmt.Errorf("skill %q: inline body is %d bytes (max %d); use path: for larger skills",
					s.Slug, len(s.Inline), maxInlineSkillBytes)
			}
			s.resolved = s.Inline
		}
		return nil
	}
	for i := range b.Documents {
		spec := b.Documents[i].Spec
		if spec == nil {
			continue
		}
		for j := range spec.Skills {
			if err := resolveOne(&spec.Skills[j]); err != nil {
				return err
			}
		}
	}
	for i := range b.Workspaces {
		ws := &b.Workspaces[i].Spec
		for j := range ws.Skills {
			if err := resolveOne(&ws.Skills[j]); err != nil {
				return err
			}
		}
		for ci := range ws.Crews {
			crew := &ws.Crews[ci]
			for j := range crew.Skills {
				if err := resolveOne(&crew.Skills[j]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// resolveLocalReferences walks every Crew/Workspace and pulls in the
// bodies of prompt_file: and skill path: entries. URL skills are
// deferred to apply-time, inline skills were already resolved during
// Load via resolveInlineOnly. Path resolution is relative to the
// directory of SourcePath, with a strict ".."-safety check plus
// symlink resolution so a manifest can't escape its sandbox.
func (b *Bundle) resolveLocalReferences() error {
	if b.SourcePath == "" {
		return nil // inline-only bundle; LoadFile is the only caller that sets SourcePath
	}
	baseDir := filepath.Dir(b.SourcePath)

	resolveSkill := func(skill *Skill) error {
		if skill.Path == "" {
			return nil // inline already resolved by resolveInlineOnly; URL deferred to apply
		}
		abs, err := safeJoin(baseDir, skill.Path)
		if err != nil {
			return fmt.Errorf("skill %q: %w", skill.Slug, err)
		}
		body, err := readSkillFile(abs)
		if err != nil {
			return fmt.Errorf("skill %q: read %s: %w", skill.Slug, skill.Path, err)
		}
		skill.resolved = body
		return nil
	}

	resolveAgent := func(agent *Agent) error {
		if agent.Prompt != "" && agent.PromptFile != "" {
			return fmt.Errorf("agent %q: cannot set both prompt and prompt_file", agent.Slug)
		}
		if agent.PromptFile != "" {
			abs, err := safeJoin(baseDir, agent.PromptFile)
			if err != nil {
				return fmt.Errorf("agent %q: %w", agent.Slug, err)
			}
			body, err := readPromptFile(abs)
			if err != nil {
				return fmt.Errorf("agent %q: read prompt_file %s: %w", agent.Slug, agent.PromptFile, err)
			}
			agent.Prompt = body
			agent.PromptFile = "" // Prompt is now the source of truth
		}
		if len(agent.Prompt) > maxPromptBytes {
			return fmt.Errorf("agent %q: prompt body is %d bytes (max %d); split into a skill or pin a smaller file",
				agent.Slug, len(agent.Prompt), maxPromptBytes)
		}
		return nil
	}

	for i := range b.Documents {
		spec := b.Documents[i].Spec
		if spec == nil {
			continue
		}
		for j := range spec.Skills {
			if err := resolveSkill(&spec.Skills[j]); err != nil {
				return err
			}
		}
		for j := range spec.Agents {
			if err := resolveAgent(&spec.Agents[j]); err != nil {
				return err
			}
		}
	}
	for i := range b.Workspaces {
		ws := &b.Workspaces[i].Spec
		for j := range ws.Skills {
			if err := resolveSkill(&ws.Skills[j]); err != nil {
				return err
			}
		}
		for ci := range ws.Crews {
			crew := &ws.Crews[ci]
			for j := range crew.Skills {
				if err := resolveSkill(&crew.Skills[j]); err != nil {
					return err
				}
			}
			for j := range crew.Agents {
				if err := resolveAgent(&crew.Agents[j]); err != nil {
					return err
				}
			}
		}
	}

	// Top-level `kind: Skill` documents (SPEC-2 shape, distinct from
	// the legacy nested Skills above). Path bodies get pulled in via
	// the kind's SetResolved hook so Validate stays offline. Inline
	// bodies are decoded directly into spec.inline by the YAML
	// dispatcher and don't need a separate resolution pass — we
	// mirror them into resolved here so Plan can treat both source
	// shapes uniformly, matching the SkillDocument contract.
	for i := range b.Skills {
		s := &b.Skills[i]
		if s.Spec.Inline != "" {
			s.SetResolved(s.Spec.Inline)
			continue
		}
		if s.Spec.Path == "" {
			// Empty path AND empty inline AND nil source URL is a
			// Validate-time error; leave it for Validate to catch
			// (here we'd otherwise emit a duplicate "missing source"
			// message and confuse the failure phase reporting).
			continue
		}
		abs, err := safeJoin(baseDir, s.Spec.Path)
		if err != nil {
			return fmt.Errorf("Skill %q: %w", s.Metadata.Slug, err)
		}
		body, err := readSkillFile(abs)
		if err != nil {
			return fmt.Errorf("Skill %q: read %s: %w", s.Metadata.Slug, s.Spec.Path, err)
		}
		s.SetResolved(body)
	}
	return nil
}

// safeJoin enforces that `rel` resolves to a path inside `base`. Any
// `..`-escape attempt or absolute path returns an error so a manifest
// shared between teammates can't be weaponised into a file-read
// primitive ("path: /etc/passwd" or "path: ../../../etc/passwd").
//
// Absolute paths are rejected outright — manifests are repo-portable
// artifacts; an absolute path is always either a mistake or an attack.
//
// Symlinks are evaluated and the resolved target must still be inside
// base. Without this gate a manifest in a trusted repo could carry a
// `path: ./safe-looking` that's actually a symlink to /etc/passwd —
// the lexical ".."-prefix check alone wouldn't catch it. EvalSymlinks
// fails on a non-existent file; that case is fine because the caller
// will report "read file: not found" with the original path which is
// clearer than a symlink error anyway.
func safeJoin(base, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths not allowed in manifest: %q", rel)
	}
	cleaned := filepath.Clean(rel)
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, string(filepath.Separator)+"..") {
		return "", fmt.Errorf("path escapes manifest directory: %q", rel)
	}
	full := filepath.Join(base, cleaned)

	// Both base and full need to be absolute before EvalSymlinks
	// + Rel can produce a comparable answer. filepath.Rel returns
	// an error when one side is absolute and the other relative
	// (it'd need cwd to interpret the relative half), and
	// EvalSymlinks on a relative path returns a relative path —
	// so the lexical comparison could miss a sibling-of-cwd
	// escape. Normalising both to absolute closes that gap.
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}

	// Evaluate symlinks on both base and target. Resolving base is
	// necessary because the manifest dir itself might be a symlinked
	// repo checkout (common with package managers and worktrees) and
	// without resolving it, every safe target would look "outside".
	// EvalSymlinks errors when the target doesn't exist — fall back to
	// the lexical absolute path so a path: to a not-yet-created file
	// (rare but valid for templates) still surfaces as a read error
	// rather than a confusing symlink error.
	resolvedBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		resolvedBase = absBase
	}
	resolvedFull, err := filepath.EvalSymlinks(absFull)
	if err != nil {
		resolvedFull = absFull
	}

	rel2, err := filepath.Rel(resolvedBase, resolvedFull)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel2, "..") || rel2 == ".." {
		return "", fmt.Errorf("path escapes manifest directory: %q", rel)
	}
	return full, nil
}
