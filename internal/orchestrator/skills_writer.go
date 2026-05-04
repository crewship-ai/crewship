package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"regexp"

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
// Gemini CLI's TOML extension format and Aider/Continue's bespoke
// mechanisms are out of scope for this pass; their adapters fall back
// to the [SKILLS AVAILABLE] block already injected into the system
// prompt via writeCanonicalMemoryFiles.
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
	if len(skills) == 0 {
		return nil
	}
	folderRoots := []string{
		".claude/skills",
		".agents/skills",
		".opencode/skills",
		".factory/skills",
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
