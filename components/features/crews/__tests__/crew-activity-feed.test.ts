import { describe, it, expect } from "vitest"
import { journalEntriesToFeedRows } from "@/components/features/crews/crew-activity-feed"
import type { JournalEntry } from "@/lib/types/journal"
import type { CrewLookup, AgentLookup } from "@/hooks/use-journal-lookup"

const crews = new Map<string, CrewLookup>([
  ["crew1", { id: "crew1", name: "Backend", slug: "backend", icon: null, color: "#3b82f6" }],
])
const agents = new Map<string, AgentLookup>([
  ["ag_lead", { id: "ag_lead", name: "Lead", slug: "lead", crew_id: "crew1", avatar_seed: null, avatar_style: null }],
])

function entry(over: Partial<JournalEntry> & Pick<JournalEntry, "entry_type">): JournalEntry {
  return {
    id: "j1",
    workspace_id: "ws1",
    ts: "2026-07-13T08:00:00Z",
    severity: "info",
    actor_type: "agent",
    summary: "did a thing",
    ...over,
  }
}

describe("journalEntriesToFeedRows", () => {
  it("assignment.created → 'assignment' row, TO from target_slug, FROM from actor_id lookup, crew + detail resolved", () => {
    const rows = journalEntriesToFeedRows(
      [entry({
        entry_type: "assignment.created",
        crew_id: "crew1",
        actor_id: "ag_lead",
        summary: "assigned db: fix it",
        payload: { target_slug: "db", target_id: "ag_db", task: "please fix the migration" },
      })],
      crews,
      agents,
    )
    expect(rows).toHaveLength(1)
    const r = rows[0]
    expect(r.type).toBe("assignment")
    expect(r.from_slug).toBe("lead")       // resolved from actor_id via lookup
    expect(r.to_slug).toBe("db")           // from payload.target_slug
    expect(r.crew_name).toBe("Backend")
    expect(r.crew_color).toBe("#3b82f6")
    expect(r.detail).toBe("please fix the migration")
  })

  it("peer.conversation → 'peer_conversation', slugs + question detail from payload", () => {
    const rows = journalEntriesToFeedRows(
      [entry({
        entry_type: "peer.conversation",
        crew_id: "crew1",
        payload: { from_slug: "api", target_slug: "db", question: "is the index there?" },
      })],
      crews,
      agents,
    )
    expect(rows[0].type).toBe("peer_conversation")
    expect(rows[0].from_slug).toBe("api")
    expect(rows[0].to_slug).toBe("db")
    expect(rows[0].detail).toBe("is the index there?")
  })

  it("peer.escalation → 'escalation', FROM only, reason as detail", () => {
    const rows = journalEntriesToFeedRows(
      [entry({
        entry_type: "peer.escalation",
        crew_id: "crew1",
        payload: { from_slug: "api", reason: "spend cap hit" },
      })],
      crews,
      agents,
    )
    expect(rows[0].type).toBe("escalation")
    expect(rows[0].from_slug).toBe("api")
    expect(rows[0].to_slug).toBeNull()
    expect(rows[0].detail).toBe("spend cap hit")
  })

  it("degrades gracefully when lookup maps are empty (id-only, no crash)", () => {
    const rows = journalEntriesToFeedRows(
      [entry({ entry_type: "assignment.running", crew_id: "unknown", actor_id: "missing" })],
      new Map(),
      new Map(),
    )
    expect(rows[0].type).toBe("assignment")
    expect(rows[0].crew_name).toBeNull()
    expect(rows[0].from_slug).toBeNull()
  })

  it("drops non-activity entry types", () => {
    const rows = journalEntriesToFeedRows(
      [
        entry({ entry_type: "llm.call" }),
        entry({ entry_type: "pipeline.step.completed" }),
        entry({ entry_type: "run.started" }),
      ],
      crews,
      agents,
    )
    expect(rows).toHaveLength(0)
  })
})
