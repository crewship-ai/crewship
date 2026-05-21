package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// safeSlugRe is the strict allowlist used to gate every slug before we
// build a filesystem path from it. Skills are written to several
// filesystem roots inside a multi-tenant container; an attacker-
// controlled slug containing path separators or "../" would let a
// malicious skill scribble outside its own folder. Match the slugify
// rules from internal/skills/parser.go (lowercase letters, digits,
// hyphen, underscore — kebab-case).
var safeSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// writeAgentSkills materialises every installed skill into the per-CLI
// discovery paths inside the agent's container. Six of the eight CLIs
// Crewship integrates use the SKILL.md folder convention with minor
// differences in the parent directory; we write the same content to each
// so the same agent can swap CLIs without losing skill access.
//
// Targets covered (May 2026 spec):
//
//	.claude/skills/<slug>/SKILL.md     (Claude Code)
//	.agents/skills/<slug>/SKILL.md     (OpenAI Codex CLI; OpenCode also walks this)
//	.opencode/skills/<slug>/SKILL.md   (sst/opencode native path)
//	.factory/skills/<slug>/SKILL.md    (Factory Droid)
//	.cursor/rules/<slug>.mdc           (Cursor — flat .mdc file, same body)
//
// Gemini CLI's TOML extension format and other adapters' bespoke
// skill mechanisms are out of scope for this pass; their adapters
// fall back to the [SKILLS AVAILABLE] block already injected into
// the system prompt via writeCanonicalMemoryFiles.
//
// Best-effort like writeCanonicalMemoryFiles: a single skill or path
// failing logs a warning but does not abort. The caller is one of the
// adapters' SetupSystemPrompt — we do not want a flaky chmod on one
// path to block agent startup.
func writeAgentSkills(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	workDir string,
	skills []SkillBundle,
	logger *slog.Logger,
) error {
	folderRoots := []string{
		".claude/skills",
		".agents/skills",
		".opencode/skills",
		".factory/skills",
	}

	// Prune stale skill folders BEFORE writing the current set so that a
	// skill removed via `crewship skill unassign` no longer survives in
	// .claude/skills/<slug>/. Without this pass, Claude Code's filesystem
	// auto-discovery would still pick up the orphaned SKILL.md and the
	// agent could keep activating a skill that the operator believes is
	// gone — same hazard for .opencode and .factory which also walk
	// these dirs.
	keep := make(map[string]struct{}, len(skills))
	for _, s := range skills {
		if s.Slug != "" && safeSlugRe.MatchString(s.Slug) {
			keep[s.Slug] = struct{}{}
		}
	}
	pruneStaleSkillFolders(ctx, container, containerID, workDir, folderRoots, keep, logger)

	if len(skills) == 0 {
		return nil
	}
	written := 0
	skipped := 0
	// firstWriteErr captures the first concrete write failure so the
	// final "zero of N landed" error wraps a real root cause. Without
	// this, callers with a nil logger had no signal at all (the
	// per-path warnings were the only place the underlying reason
	// showed up).
	var firstWriteErr error
	for _, skill := range skills {
		if skill.Slug == "" || skill.Content == "" {
			skipped++
			continue
		}
		if !safeSlugRe.MatchString(skill.Slug) {
			// Untrusted slug — refuse to build paths from it. This
			// catches "../etc/passwd", "x/y/z", absolute paths, and
			// non-ASCII shenanigans before they reach mkdir/echo.
			if logger != nil {
				logger.Warn("skill slug rejected by safety regex", "slug", skill.Slug)
			}
			skipped++
			continue
		}
		for _, root := range folderRoots {
			rel := path.Join(root, skill.Slug, "SKILL.md")
			if err := writeFileViaContainer(ctx, container, containerID, workDir, rel, skill.Content, logger); err != nil {
				if firstWriteErr == nil {
					firstWriteErr = err
				}
				if logger != nil {
					logger.Warn("write skill failed", "skill", skill.Slug, "path", rel, "error", err)
				}
				continue
			}
			written++
		}
		// Cursor uses .mdc rule files at .cursor/rules/<name>.mdc — same
		// frontmatter+markdown shape, just a flat filename. Cursor's
		// rule parser is forgiving of unknown frontmatter keys, so the
		// SKILL.md body lands without conversion.
		mdcPath := path.Join(".cursor/rules", skill.Slug+".mdc")
		if err := writeFileViaContainer(ctx, container, containerID, workDir, mdcPath, skill.Content, logger); err != nil {
			if firstWriteErr == nil {
				firstWriteErr = err
			}
			if logger != nil {
				logger.Warn("write skill failed", "skill", skill.Slug, "path", mdcPath, "error", err)
			}
		} else {
			written++
		}
	}
	if logger != nil {
		logger.Debug("agent skills materialised", "written", written, "skipped", skipped, "skills", len(skills))
	}
	if written == 0 && len(skills) > 0 {
		if firstWriteErr != nil {
			return fmt.Errorf("write agent skills: zero of %d skills landed in any path: %w", len(skills), firstWriteErr)
		}
		return fmt.Errorf("write agent skills: zero of %d skills landed in any path", len(skills))
	}
	return nil
}

// pruneStaleSkillFolders removes <root>/<slug>/ entries (and the matching
// .cursor/rules/<slug>.mdc) for slugs no longer in `keep`. Best-effort:
// failures log a warning but never abort the agent setup since the
// [SKILLS AVAILABLE] system prompt is the authoritative surface — the
// filesystem layer is the bonus discovery path.
//
// The implementation shells out once per root for the listing and once
// to rm the orphan set in a single command, so the cost is bounded
// regardless of skill count. We restrict the rm to slugs that pass
// safeSlugRe so a corrupted directory entry can never escape the
// rooted folder.
func pruneStaleSkillFolders(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	workDir string,
	folderRoots []string,
	keep map[string]struct{},
	logger *slog.Logger,
) {
	for _, root := range folderRoots {
		listing, err := execCapture(ctx, container, containerID, workDir,
			fmt.Sprintf("ls -1 %s 2>/dev/null || true", shellEscape(root)))
		if err != nil {
			if logger != nil {
				logger.Warn("skill prune: list failed", "root", root, "error", err)
			}
			continue
		}
		var orphans []string
		for _, name := range strings.Split(strings.TrimSpace(listing), "\n") {
			name = strings.TrimSpace(name)
			if name == "" || !safeSlugRe.MatchString(name) {
				continue
			}
			if _, ok := keep[name]; ok {
				continue
			}
			orphans = append(orphans, path.Join(root, name))
		}
		if len(orphans) == 0 {
			continue
		}
		args := make([]string, 0, len(orphans))
		for _, p := range orphans {
			args = append(args, shellEscape(p))
		}
		script := "rm -rf " + strings.Join(args, " ")
		if _, err := execCapture(ctx, container, containerID, workDir, script); err != nil {
			if logger != nil {
				logger.Warn("skill prune: rm failed", "root", root, "error", err)
			}
			continue
		}
		if logger != nil {
			logger.Debug("skill prune: removed orphan skill folders", "root", root, "count", len(orphans))
		}
	}

	// Cursor rules are flat .mdc files, not folders — handle them
	// separately so a single ls + rm walks .cursor/rules and skips any
	// non-.mdc entries the user may have parked there manually.
	listing, err := execCapture(ctx, container, containerID, workDir,
		"ls -1 .cursor/rules 2>/dev/null || true")
	if err != nil {
		return
	}
	var orphans []string
	for _, name := range strings.Split(strings.TrimSpace(listing), "\n") {
		name = strings.TrimSpace(name)
		if !strings.HasSuffix(name, ".mdc") {
			continue
		}
		slug := strings.TrimSuffix(name, ".mdc")
		if !safeSlugRe.MatchString(slug) {
			continue
		}
		if _, ok := keep[slug]; ok {
			continue
		}
		orphans = append(orphans, path.Join(".cursor/rules", name))
	}
	if len(orphans) == 0 {
		return
	}
	args := make([]string, 0, len(orphans))
	for _, p := range orphans {
		args = append(args, shellEscape(p))
	}
	script := "rm -f " + strings.Join(args, " ")
	if _, err := execCapture(ctx, container, containerID, workDir, script); err != nil {
		if logger != nil {
			logger.Warn("skill prune: rm cursor rules failed", "error", err)
		}
	}
}

// execCapture runs a shell snippet inside the container and returns
// stdout as a string. Used for the prune pass which needs to read
// directory listings (writeFileViaContainer discards stdout).
func execCapture(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	workDir string,
	script string,
) (string, error) {
	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		WorkingDir:  workDir,
		User:        "1001:1001",
	}
	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return "", err
	}
	defer result.Reader.Close()
	buf, err := io.ReadAll(result.Reader)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}
