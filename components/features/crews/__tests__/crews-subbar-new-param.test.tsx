import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, waitFor } from "@testing-library/react"

// Creating a crew or an agent is a DIALOG on /crews and has no route of its
// own — /crews/new and /crews/agents/new were deleted with the redesign. The
// command palette's two "Create new …" rows went on pointing at them for as
// long as they did precisely because there was nothing else to point at.
//
// ?new=crew | ?new=agent is that something. These pin it, because the palette
// rows are worthless if this end does not answer.

const h = vi.hoisted(() => ({ search: "", replace: vi.fn() }))

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: h.replace, push: vi.fn(), prefetch: vi.fn(), back: vi.fn(), forward: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(h.search),
  usePathname: () => "/crews",
}))

vi.mock("../create-crew-dialog", () => ({
  CreateCrewDialog: ({ open }: { open: boolean }) =>
    open ? <div data-testid="create-crew-dialog" /> : null,
}))
vi.mock("../create-agent", () => ({
  CreateAgentDialog: ({ open }: { open: boolean }) =>
    open ? <div data-testid="create-agent-dialog" /> : null,
}))

import { CrewsSubbar } from "../crews-subbar"

function renderSubbar() {
  return render(
    <CrewsSubbar
      workspaceId="ws-1"
      crewSlug={null}
      crewName={null}
      agentName={null}
      onCrewCreated={vi.fn()}
      onAgentCreated={vi.fn()}
      crews={[{ id: "c1", name: "Ops", slug: "ops" }]}
    />,
  )
}

beforeEach(() => {
  h.search = ""
  h.replace.mockClear()
  window.history.replaceState(null, "", "/crews")
})
afterEach(cleanup)

describe("CrewsSubbar — ?new=", () => {
  it("opens the crew dialog for ?new=crew", async () => {
    h.search = "new=crew"
    window.history.replaceState(null, "", "/crews?new=crew")
    renderSubbar()
    expect(await screen.findByTestId("create-crew-dialog")).toBeInTheDocument()
    expect(screen.queryByTestId("create-agent-dialog")).not.toBeInTheDocument()
  })

  it("opens the agent dialog for ?new=agent", async () => {
    h.search = "new=agent"
    window.history.replaceState(null, "", "/crews?new=agent")
    renderSubbar()
    expect(await screen.findByTestId("create-agent-dialog")).toBeInTheDocument()
    expect(screen.queryByTestId("create-crew-dialog")).not.toBeInTheDocument()
  })

  it("consumes the param, so a reload does not reopen the dialog", async () => {
    h.search = "new=crew"
    window.history.replaceState(null, "", "/crews?new=crew")
    renderSubbar()
    await screen.findByTestId("create-crew-dialog")
    await waitFor(() => expect(h.replace).toHaveBeenCalledWith("/crews", { scroll: false }))
  })

  it("opens nothing without the param, and nothing for a value it does not know", async () => {
    renderSubbar()
    expect(screen.queryByTestId("create-crew-dialog")).not.toBeInTheDocument()
    expect(screen.queryByTestId("create-agent-dialog")).not.toBeInTheDocument()
    expect(h.replace).not.toHaveBeenCalled()

    cleanup()
    h.search = "new=banana"
    window.history.replaceState(null, "", "/crews?new=banana")
    renderSubbar()
    expect(screen.queryByTestId("create-crew-dialog")).not.toBeInTheDocument()
    expect(screen.queryByTestId("create-agent-dialog")).not.toBeInTheDocument()
    // An unknown value is left in the URL rather than silently rewritten —
    // this hook owns two values and nothing else.
    expect(h.replace).not.toHaveBeenCalled()
  })
})
