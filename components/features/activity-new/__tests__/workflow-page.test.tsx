/**
 * The page, at the level only the page can be wrong at.
 *
 * The ordering, indentation and duration rules are asserted directly against
 * the pure function in lib/__tests__/workflow-timeline.test.ts — mounting a
 * card and grepping its text proves none of them. What is left for this file
 * is the wiring the pure test cannot see: that the rows the shaper produced
 * are the rows that reach the screen, in that order; that clicking one hands
 * the caller a kind and a ref it can act on; and that a row with no recorded
 * duration reaches the DOM as an em dash rather than as 0ms.
 */

import * as React from "react"
import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { ChainSummary } from "@/hooks/use-chains"
import type { TimelineSource } from "@/lib/workflow-timeline"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

// The topology card owns its own request, its own React Flow chunk and its
// own tests. Drawing it here would test that workstream's file, not this one.
vi.mock("../topology-card", () => ({
  TopologyCard: ({ anchor }: { anchor: string }) => <div data-testid="topology">{anchor}</div>,
}))

import { WorkflowPage } from "../workflow-page"

/**
 * A REAL walk, verbatim from a server built at this branch's head against a
 * copy of the dev database, on 2026-08-10:
 *
 *   crewship chain run_cmsmv0tzx001693a3ed64 -f json
 *
 * Two runs are dated and carry a duration; the routine and the rule carry
 * neither, because internal/chain dates events and not nouns. Half a real
 * chain being undatable is the normal case, so it is the fixture.
 */
const LIVE_GRAPH: TimelineSource = {
  nodes: [
    {
      id: "run:run_cmsmv0tzx001693a3ed64",
      kind: "run",
      ref: "run_cmsmv0tzx001693a3ed64",
      key: "on-close-file-followup",
      label: "on-close-file-followup",
      status: "completed",
      depth: 0,
      anchor: true,
      occurred_at: "2026-08-10T06:37:58.366620000Z",
      ended_at: "2026-08-10T06:37:58.379865000Z",
      duration_ms: 13,
    },
    {
      id: "routine:pln_cmsmuxliu0001d886758f",
      kind: "routine",
      ref: "pln_cmsmuxliu0001d886758f",
      key: "on-close-file-followup",
      label: "on-close-file-followup",
      status: "active",
      depth: 1,
    },
    {
      id: "automation:aut_18536c615e644ca4",
      kind: "automation",
      ref: "aut_18536c615e644ca4",
      key: "mission.status_change",
      label: "file a follow-up when an issue closes",
      status: "enabled",
      depth: 1,
    },
    {
      id: "run:run_cmsmuy9f30013ffeaf4f6",
      kind: "run",
      ref: "run_cmsmuy9f30013ffeaf4f6",
      key: "on-close-file-followup",
      label: "on-close-file-followup",
      status: "completed",
      depth: 2,
      occurred_at: "2026-08-10T06:35:58.384405000Z",
      ended_at: "2026-08-10T06:35:58.422991000Z",
      duration_ms: 38,
    },
  ],
  edges: [
    { from: "routine:pln_cmsmuxliu0001d886758f", to: "run:run_cmsmv0tzx001693a3ed64", kind: "runs" },
    { from: "automation:aut_18536c615e644ca4", to: "run:run_cmsmv0tzx001693a3ed64", kind: "triggers" },
    { from: "routine:pln_cmsmuxliu0001d886758f", to: "run:run_cmsmuy9f30013ffeaf4f6", kind: "runs" },
    { from: "automation:aut_18536c615e644ca4", to: "routine:pln_cmsmuxliu0001d886758f", kind: "triggers" },
    { from: "automation:aut_18536c615e644ca4", to: "run:run_cmsmuy9f30013ffeaf4f6", kind: "triggers" },
  ],
}

/** The matching row of GET /api/v1/chains, same instance, same day. */
const CHAIN: ChainSummary = {
  origin: "run_cmsmv0tzx001693a3ed64",
  started_by_kind: "automation",
  started_by_id: "aut_18536c615e644ca4",
  started_by_key: "mission.status_change",
  started_by: "file a follow-up when an issue closes",
  triggered_via: "automation",
  routine_id: "pln_cmsmuxliu0001d886758f",
  routine_slug: "on-close-file-followup",
  runs: 1,
  max_chain_depth: 1,
  failed_runs: 0,
  failed: false,
  first_activity: "2026-08-10T06:37:58.366620000Z",
  last_activity: "2026-08-10T06:37:58.379865000Z",
  duration_ms: 13,
  issue_count: 1,
  issues: [
    {
      id: "cmsmv0u0600150dc1a9b0",
      identifier: "ENG-8",
      title: "Follow-up: verify cmsizlp7q009bc81acdb3 in staging",
      created: true,
    },
  ],
  agent_count: 0,
}

const renderPage = (over: Partial<ChainSummary> = {}, onOpenNode = vi.fn()) => {
  render(
    <WorkflowPage
      workspaceId="ws_1"
      chain={{ ...CHAIN, ...over }}
      onBack={vi.fn()}
      onOpenNode={onOpenNode}
    />,
  )
  return onOpenNode
}

beforeEach(() => {
  apiFetch.mockReset()
  apiFetch.mockResolvedValue({ ok: true, json: async () => LIVE_GRAPH })
})

describe("WorkflowPage", () => {
  it("walks the chain the row identifies, not the routine behind it", async () => {
    renderPage()
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    expect(apiFetch.mock.calls[0][0]).toContain(
      `/api/v1/chains/${encodeURIComponent(CHAIN.origin)}`,
    )
    expect(screen.getByTestId("topology")).toHaveTextContent(CHAIN.origin)
  })

  it("puts what happened on the screen in the order the shaper decided", async () => {
    renderPage()
    // The rule fired the routine, the routine produced the run — read down.
    await waitFor(() => {
      const kinds = screen.getAllByTestId("timeline-kind")
      expect(kinds.map((k) => k.textContent)).toEqual(["automation", "routine", "run", "run"])
    })
  })

  it("hands a clicked step back as a kind and a ref the caller can open", async () => {
    const onOpenNode = renderPage()
    const step = await screen.findByRole("button", { name: /you came from here/ })
    fireEvent.click(step)
    expect(onOpenNode).toHaveBeenCalledWith("run", "run_cmsmv0tzx001693a3ed64")
  })

  // The rule the whole feature turns on, asserted where a user would see it.
  // The rule and the routine are nouns the server does not date, so each
  // contributes a dash in the time column and a dash in the duration column —
  // four in all, and not one 0ms among them. The two runs show real numbers.
  it("renders an undated step as an em dash, never as 0ms", async () => {
    renderPage()
    await screen.findByText("file a follow-up when an issue closes")
    expect(screen.queryByText("0ms")).toBeNull()
    // Scoped to the sequence. The page carries other cards with their own
    // dashes and durations — a page-wide count is an assertion about the whole
    // layout wearing the clothes of one about this list.
    const sequence = within(screen.getByRole("region", { name: "What happened, in sequence" }))
    expect(sequence.getAllByText("—")).toHaveLength(4)
    // 13ms three times, and each one is a different height on the same
    // measurement: the Runs card lists the run, the sequence places it among
    // everything else, and the chain KPI is the whole workflow — which on a
    // one-run chain is that run. Asserted per card rather than as a total, so
    // the next card added to this page does not break an assertion about a
    // number none of its rows changed.
    expect(sequence.getByText("13ms")).toBeInTheDocument()
    expect(
      within(screen.getByRole("region", { name: "Runs" })).getByText("13ms"),
    ).toBeInTheDocument()
    expect(sequence.getByText("38ms")).toBeInTheDocument()
  })

  it("says out loud which steps the order is not a claim about", async () => {
    renderPage()
    expect(await screen.findByText(/2 of 4 steps are not dated/)).toBeInTheDocument()
  })

  it("drops the caveat when the walk happens to date everything", async () => {
    apiFetch.mockResolvedValue({
      ok: true,
      json: async () => ({
        nodes: LIVE_GRAPH.nodes.map((n, i) => ({
          ...n,
          occurred_at: `2026-08-10T06:37:5${i}.000Z`,
          ended_at: `2026-08-10T06:37:5${i}.500Z`,
          duration_ms: 500,
        })),
        edges: LIVE_GRAPH.edges,
      }),
    })
    renderPage()
    await screen.findByText("file a follow-up when an issue closes")
    expect(screen.queryByText(/are not dated/)).toBeNull()
    const sequence = within(screen.getByRole("region", { name: "What happened, in sequence" }))
    expect(sequence.getAllByText("500ms")).toHaveLength(4)
  })

  it("offers the issues it touched as things to open", async () => {
    const onOpenNode = renderPage()
    const issue = await screen.findByRole("button", { name: /ENG-8/ })
    fireEvent.click(issue)
    expect(onOpenNode).toHaveBeenCalledWith("issue", "cmsmv0u0600150dc1a9b0")
  })

  it("says how many refs the row did not carry rather than implying it carried them all", async () => {
    // The server caps a row at MaxChainSummaryRefs (5); issue_count is the truth.
    renderPage({ issue_count: 9 })
    expect(await screen.findByText(/\+8 more, not carried on this row/)).toBeInTheDocument()
  })

  it("reports a chain with no measurable span as running rather than as instant", async () => {
    renderPage({ duration_ms: null })
    const kpi = (await screen.findByText("Duration")).parentElement as HTMLElement
    expect(within(kpi).getByText("running")).toBeInTheDocument()
    expect(within(kpi).queryByText("0ms")).toBeNull()
  })

  // The palette in app/globals.css is finished; "a style" here means layout
  // and interaction, never a new shade. A literal is invisible in review and
  // survives every theme change, so it is caught by reading the source — the
  // same trick lib/__tests__/theme-contrast.test.ts uses on the stylesheet.
  it("takes every colour from a token, never from a literal", () => {
    const files = ["components/features/activity-new/workflow-page.tsx", "lib/workflow-timeline.ts"]
    const HEX = /#[0-9a-fA-F]{3,8}\b/
    const PALETTE_CLASS =
      /\b(?:text|bg|border|fill|stroke|ring)-(?:red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|slate|gray|zinc|neutral|stone)-\d{2,3}\b/
    for (const f of files) {
      const src = readFileSync(resolve(process.cwd(), f), "utf8")
      expect(HEX.test(src), `${f} carries a hex colour literal`).toBe(false)
      expect(PALETTE_CLASS.test(src), `${f} carries a raw palette colour class`).toBe(false)
    }
  })

  it("surfaces a failed walk with a way back rather than an empty card", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 500, json: async () => ({}) })
    renderPage()
    expect(await screen.findByText(/Could not load the sequence/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument()
  })
})
