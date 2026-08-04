import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", async (importActual) => ({
  ...(await importActual<Record<string, unknown>>()),
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

import { RoutineProposalDiff } from "../routine-proposal-diff"

// The card answers one question — "what am I approving?" — so the
// tests are about what a reviewer can read off it, not about markup.
//
// The v1 branch matters more than it looks: asking the diff endpoint
// for a predecessor that never existed is a 400, so a first-version
// proposal must not fetch at all.

describe("<RoutineProposalDiff>", () => {
  beforeEach(() => apiFetch.mockReset())

  it("asks for the two versions named in the proposal", async () => {
    apiFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ from_version: 2, to_version: 3, identical: false, unified_diff: "" }),
    })
    render(<RoutineProposalDiff workspaceId="ws-1" slug="nightly" fromVersion={2} toVersion={3} />)

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    const url = String(apiFetch.mock.calls[0][0])
    expect(url).toContain("/pipelines/nightly/diff")
    expect(url).toContain("from=2")
    expect(url).toContain("to=3")
    expect(screen.getByText("v2 → v3")).toBeTruthy()
  })

  it("does not fetch a diff for a first version", () => {
    render(<RoutineProposalDiff workspaceId="ws-1" slug="nightly" fromVersion={null} toVersion={1} />)
    expect(apiFetch).not.toHaveBeenCalled()
    expect(screen.getByText(/first version/i)).toBeTruthy()
  })

  it("colours added and removed lines apart", async () => {
    apiFetch.mockResolvedValue({
      ok: true,
      json: async () => ({
        from_version: 1,
        to_version: 2,
        identical: false,
        unified_diff: ["--- v1", "+++ v2", "@@ -3,4 +3,5 @@", "-  type: agent_run", "+  type: http"].join(
          "\n",
        ),
      }),
    })
    render(<RoutineProposalDiff workspaceId="ws-1" slug="nightly" fromVersion={1} toVersion={2} />)

    // Identity normalizer: the default one collapses the leading
    // whitespace a diff line depends on.
    const raw = { normalizer: (v: string) => v }
    const added = await screen.findByText("+  type: http", raw)
    const removed = screen.getByText("-  type: agent_run", raw)
    expect(added.className).toContain("text-success")
    expect(removed.className).toContain("text-destructive")
    // The file headers start with +++/--- and must NOT read as changes.
    // Asserted on the BACKGROUND: cn() merges conflicting text colours,
    // so a header wrongly treated as an addition still loses its green
    // text to the dimming class and only the tint gives it away.
    expect(screen.getByText("+++ v2").className).not.toContain("bg-success")
    expect(screen.getByText("--- v1").className).not.toContain("bg-destructive")
  })

  it("says so when only the risk classification changed", async () => {
    apiFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ from_version: 4, to_version: 5, identical: true, unified_diff: "" }),
    })
    render(<RoutineProposalDiff workspaceId="ws-1" slug="nightly" fromVersion={4} toVersion={5} />)
    expect(await screen.findByText(/byte-identical to v4/i)).toBeTruthy()
  })

  it("reports a failed lookup instead of rendering an empty box", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 404, json: async () => ({}) })
    render(<RoutineProposalDiff workspaceId="ws-1" slug="nightly" fromVersion={1} toVersion={2} />)
    expect(await screen.findByText(/could not load the diff \(404\)/i)).toBeTruthy()
  })
})
