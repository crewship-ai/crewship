package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/safepath"
)

func safePathSegment(s string) (string, error) {
	if _, err := safepath.ValidateComponent(s); err != nil {
		return "", err
	}
	return s, nil
}

type seedMemoryDocument struct {
	Path string `json:"path" yaml:"path"`
	Body string `json:"body" yaml:"body"`
}

// seedAgentMemory provisions the target server's memory through its scoped,
// durable, create-if-absent API. No server directories are inferred on the CLI
// host, even when the selected server happens to be localhost.
func seedAgentMemory(ctx context.Context, client *cli.Client, crewIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client = client.WithContext(ctx)
	fmt.Fprintln(os.Stderr, "Seeding agent memory tiers on the target server...")
	crewSlugByID := make(map[string]string, len(crewIDs))
	slugs := make([]string, 0, len(crewIDs))
	for slug, id := range crewIDs {
		if _, err := safePathSegment(id); err != nil {
			return fmt.Errorf("seedAgentMemory: invalid crew_id %q: %w", id, err)
		}
		crewSlugByID[id] = slug
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	resp, err := client.Get("/api/v1/agents")
	if err != nil {
		return fmt.Errorf("seedAgentMemory: list agents: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("seedAgentMemory: list agents: %w", err)
	}
	var agents []struct {
		Slug   string `json:"slug" yaml:"slug"`
		CrewID string `json:"crew_id" yaml:"crew_id"`
	}
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return fmt.Errorf("seedAgentMemory: parse agents: %w", err)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Slug < agents[j].Slug })
	for _, a := range agents {
		if crewSlugByID[a.CrewID] == "" {
			continue
		}
		if _, err := safePathSegment(a.Slug); err != nil {
			return fmt.Errorf("seedAgentMemory: invalid agent slug %q: %w", a.Slug, err)
		}
	}
	initialize := func(crewID, agentSlug string, docs []seedMemoryDocument) (int, error) {
		resp, err := client.Post("/api/v1/memory/initialize", map[string]any{"crew_id": crewID, "agent_slug": agentSlug, "documents": docs})
		if err != nil {
			return 0, err
		}
		if err := cli.CheckError(resp); err != nil {
			return 0, err
		}
		var result struct {
			Written  int `json:"written" yaml:"written"`
			Existing int `json:"existing" yaml:"existing"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return 0, err
		}
		if result.Written < 0 || result.Existing < 0 || result.Written+result.Existing != len(docs) {
			return 0, fmt.Errorf("server did not acknowledge every memory document")
		}
		return result.Written, nil
	}
	wrote := 0
	for _, slug := range slugs {
		count, err := initialize(crewIDs[slug], "", []seedMemoryDocument{{"CREW.md", demoCrewMD(slug)}, {"learned.md", demoLearnedMD()}})
		if err != nil {
			return fmt.Errorf("initialize crew %s memory: %w", slug, err)
		}
		wrote += count
	}
	month := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	for _, a := range agents {
		crewSlug := crewSlugByID[a.CrewID]
		if crewSlug == "" {
			continue
		}
		docs := []seedMemoryDocument{{"AGENT.md", demoAgentMD(a.Slug, crewSlug)}, {"PERSONA.md", demoPersonaMD(a.Slug)}, {"pins.md", demoPinsMD()}, {"daily/" + month + ".md", demoDailyMD(month, a.Slug)}}
		count, err := initialize(a.CrewID, a.Slug, docs)
		if err != nil {
			return fmt.Errorf("initialize agent %s memory: %w", a.Slug, err)
		}
		wrote += count
	}
	fmt.Fprintf(os.Stderr, "  ✓ Initialized %d memory files on the target server; existing files preserved\n", wrote)
	return nil
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
Mission: Demo crew seeded by `+"`crewship seed --with-memory`"+`.

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
