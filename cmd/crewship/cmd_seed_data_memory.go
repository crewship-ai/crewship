package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// seedAgentMemory pre-populates the on-disk memory tiers for each seeded
// agent so a fresh workspace has a realistic "month of context" backing
// every agent — useful for demoing memory recall and for RBAC/GDPR live
// tests where memory_versions and peer_cards need to exist.
//
// Writes are filesystem-level under {storagePath}/crews/{crew_id}/...
// because memory tools normally run inside the agent container; at seed
// time no container exists yet. The orchestrator mounts these paths
// into /crew/{agents,shared}/ on first agent run.
//
// Files written per agent:
//
//	{agent_slug}/.memory/AGENT.md             — identity + preferences (4KB cap)
//	{agent_slug}/.memory/PERSONA.md           — voice & dissent rules (1.5KB cap)
//	{agent_slug}/.memory/pins.md              — never-evict facts (8KB cap)
//	{agent_slug}/.memory/daily/{date}.md      — one daily log from a month ago
//
// Plus per crew:
//
//	shared/.memory/CREW.md                    — shared knowledge (4KB cap)
//	shared/.memory/learned.md                 — promoted lessons (writer-managed)
//
// Toggled via `--with-memory`; default off so existing seeds stay
// byte-identical for callers who didn't opt in.
func seedAgentMemory(ctx context.Context, client *cli.Client, crewIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	basePath := resolveStorageBasePath()
	if basePath == "" {
		return fmt.Errorf("seedAgentMemory: cannot resolve CREWSHIP_STORAGE_BASE_PATH; set the env or pass it explicitly")
	}
	fmt.Fprintln(os.Stderr, "Seeding agent memory tiers...")

	// Build reverse lookup crewID → crewSlug for the per-crew files.
	crewSlugByID := make(map[string]string, len(crewIDs))
	for slug, id := range crewIDs {
		crewSlugByID[id] = slug
	}

	// Fetch agents and group by crew so we know which slugs to write
	// memory files for. Going through the API instead of taking the
	// flat slug→id map keeps the function self-contained and survives
	// a future seedAgents return-shape change.
	wsID := client.GetWorkspaceID()
	resp, err := client.Get(fmt.Sprintf("/api/v1/agents?workspace_id=%s", wsID))
	if err != nil {
		return fmt.Errorf("seedAgentMemory: list agents: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("seedAgentMemory: list agents: %w", err)
	}
	var agents []struct {
		Slug   string `json:"slug"`
		CrewID string `json:"crew_id"`
	}
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return fmt.Errorf("seedAgentMemory: parse agents: %w", err)
	}

	month := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	wrote := 0

	// Crew-shared memory (one CREW.md + learned.md per crew, regardless
	// of how many agents the crew has).
	for crewSlug, crewID := range crewIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		sharedDir := filepath.Join(basePath, "crews", crewID, "shared", ".memory")
		if err := os.MkdirAll(sharedDir, 0o755); err != nil {
			return fmt.Errorf("mkdir shared mem: %w", err)
		}
		if err := writeFileIfAbsent(filepath.Join(sharedDir, "CREW.md"), demoCrewMD(crewSlug)); err != nil {
			return err
		}
		if err := writeFileIfAbsent(filepath.Join(sharedDir, "learned.md"), demoLearnedMD()); err != nil {
			return err
		}
		wrote += 2
	}

	// Per-agent memory (AGENT, PERSONA, pins, one daily log a month ago).
	for _, a := range agents {
		if err := ctx.Err(); err != nil {
			return err
		}
		if a.CrewID == "" || crewSlugByID[a.CrewID] == "" {
			continue
		}
		agentMemDir := filepath.Join(basePath, "crews", a.CrewID, "agents", a.Slug, ".memory")
		dailyDir := filepath.Join(agentMemDir, "daily")
		if err := os.MkdirAll(dailyDir, 0o755); err != nil {
			return fmt.Errorf("mkdir agent mem: %w", err)
		}
		if err := writeFileIfAbsent(filepath.Join(agentMemDir, "AGENT.md"), demoAgentMD(a.Slug, crewSlugByID[a.CrewID])); err != nil {
			return err
		}
		if err := writeFileIfAbsent(filepath.Join(agentMemDir, "PERSONA.md"), demoPersonaMD(a.Slug)); err != nil {
			return err
		}
		if err := writeFileIfAbsent(filepath.Join(agentMemDir, "pins.md"), demoPinsMD()); err != nil {
			return err
		}
		if err := writeFileIfAbsent(filepath.Join(dailyDir, month+".md"), demoDailyMD(month, a.Slug)); err != nil {
			return err
		}
		wrote += 4
	}
	fmt.Fprintf(os.Stderr, "  ✓ Wrote %d memory files across %d crew(s) / %d agent(s)\n", wrote, len(crewIDs), len(agents))
	return nil
}

// resolveStorageBasePath reads the storage base path the orchestrator
// uses at runtime. Honours CREWSHIP_STORAGE_BASE_PATH (set by dev.sh
// per-instance, e.g. /tmp/crewship-1-data) and falls back to the
// default in $HOME/.crewship.
func resolveStorageBasePath() string {
	if v := os.Getenv("CREWSHIP_STORAGE_BASE_PATH"); v != "" {
		return v
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".crewship")
	}
	return ""
}

// writeFileIfAbsent skips when the file already exists so re-running
// seed with --with-memory doesn't clobber any edits an operator made
// after the first seed. To force-overwrite, delete the file first
// or pass --nuke before re-seeding.
func writeFileIfAbsent(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func demoAgentMD(agentSlug, crewSlug string) string {
	return fmt.Sprintf(`# AGENT.md — %s

Role: Demo agent in the %s crew.
Tenure: seeded %s.
Reports to: Captain (coordinator).

## Working preferences
- Respond in English by default; switch to Czech when operator does.
- Lead with the verdict, then evidence (file:line for code claims).
- Surface dissent if you disagree before agreeing.

## Known identifiers
- My slug: %s
- My crew: %s

## Skills calibrated (from prior session log)
- Reading and cross-referencing memory tiers (AGENT / CREW / pins / daily).
- Using memory tools mid-session via the in-sidecar MCP server.
`, agentSlug, crewSlug, time.Now().Format("2006-01-02"), agentSlug, crewSlug)
}

func demoCrewMD(crewSlug string) string {
	return fmt.Sprintf(`# CREW.md — %s shared knowledge

Crew slug: %s
Mission: Demo crew seeded by ` + "`crewship seed --with-memory`" + `.

## Shared conventions
- File-first markdown for all memory tiers.
- Promoted lessons land in learned.md once they recur across sessions.
- Daily logs in agents/{slug}/.memory/daily/YYYY-MM-DD.md.

## Rules of engagement
- Never admin-merge multi-feature PRs — only P0 hot-fixes.
- Always grep the file before fixing a single CodeRabbit-flagged line
  ("fix the line vs fix the class").
- Use the audit-stack payloads/ for prompt-injection fuzzing.
`, crewSlug, crewSlug)
}

func demoPersonaMD(agentSlug string) string {
	return fmt.Sprintf(`# PERSONA.md — %s voice

- Tone: direct, slightly skeptical, no fluff.
- Always lead with the verdict, then evidence.
- Surface dissent: if the user is wrong, say so before agreeing.
- Quote file:line for any code claim.
- Sign off important reports with "⛵ %s".
`, agentSlug, agentSlug)
}

func demoPinsMD() string {
	return `# pins.md — never-evict

PINNED-1: Memory tools are MCP-server-backed. Mid-session memory.write
calls land in {agent}/.memory/AGENT.md or daily/{date}.md depending on
the requested tier.

PINNED-2: Crew shared memory lives at /crew/shared/.memory/CREW.md.
Only the LEAD should write there; agents read from it.

PINNED-3: peer_cards live at /crew/agents/{slug}/.memory/peers/{user_slug}.md.
GDPR cascade on DELETE /admin/users/{id}/data scrubs DB row + on-disk file.

PINNED-4: lessons (formerly lessons.md, now learned.md) are managed by
the lesson_writer primitive. Never write to learned.md via memory.write —
go through the writer so caps and replace-by-ID semantics stay enforced.
`
}

func demoDailyMD(date, agentSlug string) string {
	return fmt.Sprintf(`# daily/%s.md — %s

This entry was seeded one month back to demo memory persistence.

10:14 — Started the day reviewing the memory roadmap PRD and
        looking at the close-the-loop endpoints landed in PR #2.

11:02 — Confirmed file-first markdown works for my agent tier;
        AGENT.md, PERSONA.md, pins.md all load at boot.

14:20 — Spotted that pre-seeded daily logs are visible mid-session
        only when I call memory.read('daily') — they're not in the
        boot context (which carries AGENT / CREW / PERSONA only).

17:55 — Promoted a recurring lesson: always grep the file before
        fixing a single CodeRabbit-flagged line. Filed to learned.md
        as LESSON-001.

Tomorrow: scan the cap protocol — verify soft warning at 80%% triggers
on appends without blocking; hard error at 100%%.
`, date, agentSlug)
}

func demoLearnedMD() string {
	return `# learned.md — crew-wide promoted lessons

LESSON-001 (promoted from a Mariana session, 2026-04-22): When a
CodeRabbit review flags a single line, always grep for the same
pattern across the file before commit. "Fix the line" vs "fix the
class" — round-2 audit cycles routinely catch the second instance.

LESSON-002 (promoted from a Daniel pair-debug, 2026-05-11): Multi-
instance dev.sh start_next() didn't source .env.local; every
crewship_N (N≥1) routed /ws into instance 0. Patched on dev VM
2026-04-30, needs upstream PR.

LESSON-003 (promoted from a Mariana retro, 2026-05-18): Self-hosted
GitHub runner on MBA M3 was removed (PR #218 → PR #417). CI now on
ubuntu-latest only; force-remove API trick documented for next time.
`
}
