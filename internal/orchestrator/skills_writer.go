package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"path"

	"github.com/crewship-ai/crewship/internal/provider"
)

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
	for _, skill := range skills {
		if skill.Slug == "" || skill.Content == "" {
			skipped++
			continue
		}
		for _, root := range folderRoots {
			rel := path.Join(root, skill.Slug, "SKILL.md")
			if err := writeFileViaContainer(ctx, container, containerID, workDir, rel, skill.Content, logger); err != nil {
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
		return fmt.Errorf("write agent skills: zero of %d skills landed in any path", len(skills))
	}
	return nil
}
