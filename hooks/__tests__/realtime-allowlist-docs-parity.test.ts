import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { describe, expect, it } from "vitest"

import { VALID_REALTIME_TYPES } from "@/hooks/use-realtime"

/**
 * #2125: the allowlist and the documented event vocabulary had drifted —
 * 40+ types documented in docs/api-reference/websocket.mdx's Workspace
 * Channel table were emitted server-side and silently dropped by
 * VALID_REALTIME_TYPES, with no error and no log.
 *
 * realtime-allowlist-issue-events.test.ts already does the narrower,
 * stronger version of this for the issue.* subset (it scans the Go source
 * of the emitting handlers directly). This test does the broader version
 * the issue's own text names as the acceptable alternative: read the
 * vocabulary from docs/api-reference/websocket.mdx rather than every Go
 * broadcast call site — the workspace channel's ~15 different calling
 * conventions (BroadcastWorkspace, broadcastWorkspaceEvent,
 * broadcastIssueEvent, broadcastAgentEvent, broadcastCrewEvent,
 * h.broadcastEvent, a couple of raw hub.Broadcast("workspace:"+id, ...)
 * call sites, ...) make a single reliable Go-source regex impractical
 * without a much larger scanner than this fix's scope; the doc table is
 * the one place all of them are already supposed to agree.
 *
 * This does NOT hardcode the event-type list. It parses the "Event Type"
 * column out of every markdown table under the "### Workspace Channel"
 * heading (up to the next "### " heading) and asserts every one is
 * registered. A future PR that documents a new workspace event without
 * registering it in VALID_REALTIME_TYPES fails this test — that is the
 * durable half. Registering today's ~40 known offenders is the one-time
 * half (see hooks/use-realtime.tsx, the "#2125: the rest of the
 * documented..." block in both the RealtimeEventType union and the Set).
 */

const DOCS_PATH = resolve(process.cwd(), "docs/api-reference/websocket.mdx")

// Matches a markdown table row's first cell when it's an inline-code event
// type: "| `issue.status_changed` | ... |". Deliberately anchored on the
// backtick-wrapped-first-cell shape rather than "any backticked token in
// the section" — the Payload column also uses backticks (`{ id, ... }`)
// and would otherwise be misread as event types.
const TABLE_EVENT_TYPE_ROW = /^\|\s*`([a-zA-Z0-9_.]+)`\s*\|/gm

function workspaceChannelEventTypes(): string[] {
  const doc = readFileSync(DOCS_PATH, "utf8")
  const start = doc.indexOf("### Workspace Channel")
  if (start === -1) {
    throw new Error('docs/api-reference/websocket.mdx: no "### Workspace Channel" heading found')
  }
  // Up to the next h3 (or h2) heading — stops at "### Journal Channel"
  // (a subsection under Workspace, but its journal.entry type lives on a
  // dedicated opt-in channel, not workspace:{id}, and is already
  // allowlisted separately) as well as "### Crew Channel" etc.
  const journalSubStart = doc.indexOf("#### Journal Channel", start)
  const nextH3 = doc.indexOf("\n### ", start + 1)
  const end = journalSubStart !== -1 ? journalSubStart : nextH3 === -1 ? doc.length : nextH3
  const section = doc.slice(start, end)

  const types = new Set<string>()
  for (const m of section.matchAll(TABLE_EVENT_TYPE_ROW)) types.add(m[1])
  return [...types]
}

describe("realtime allowlist — docs/api-reference/websocket.mdx workspace-channel parity (#2125)", () => {
  const documented = workspaceChannelEventTypes()

  it("found real documented event types — an empty result means the section heading or table format moved, not that the doc has no events", () => {
    expect(documented.length).toBeGreaterThan(40)
    expect(documented).toEqual(
      expect.arrayContaining(["issue.created", "issue.updated", "mission.updated", "crew.created"]),
    )
  })

  it.each(workspaceChannelEventTypes().map((t) => [t] as const))(
    "%s is registered in VALID_REALTIME_TYPES",
    (type) => {
      expect(VALID_REALTIME_TYPES.has(type)).toBe(true)
    },
  )
})
