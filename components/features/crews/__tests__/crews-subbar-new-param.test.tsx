import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, waitFor, fireEvent } from "@testing-library/react"

// Creating a crew or an agent is a DIALOG on /crews and has no route of its
// own. ?new=crew | ?new=agent is the deep link the command palette and the
// onboarding nudge use; these pin it, and the C / A keys of the New menu.

const h = vi.hoisted(() => ({ search: "", replace: vi.fn() }))

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: h.replace, push: vi.fn(), prefetch: vi.fn(), back: vi.fn(), forward: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(h.search),
  usePathname: () => "/crews",
}))

vi.mock("../create-crew-dialog", () => ({
  CreateCrewDialog: ({ open, initialStart }: { open: boolean; initialStart?: string }) =>
    open ? <div data-testid="create-crew-dialog" data-start={initialStart} /> : null,
}))
vi.mock("../create-agent", () => ({
  CreateAgentDialog: ({ open, defaultCrewSlug }: { open: boolean; defaultCrewSlug: string | null }) =>
    open ? <div data-testid="create-agent-dialog" data-crew={defaultCrewSlug ?? ""} /> : null,
}))

import { CrewsCreateProvider } from "../crews-create"

function renderProvider(currentCrewSlug: string | null = null) {
  return render(
    <CrewsCreateProvider
      workspaceId="ws-1"
      crews={[{ id: "c1", name: "Ops", slug: "ops" }]}
      currentCrewSlug={currentCrewSlug}
      onCrewCreated={vi.fn()}
      onAgentCreated={vi.fn()}
    />,
  )
}

beforeEach(() => {
  h.search = ""
  h.replace.mockClear()
  window.history.replaceState(null, "", "/crews")
})
afterEach(cleanup)

describe("Crews create — ?new=", () => {
  it("opens the crew dialog for ?new=crew", async () => {
    h.search = "new=crew"
    window.history.replaceState(null, "", "/crews?new=crew")
    renderProvider()
    expect(await screen.findByTestId("create-crew-dialog")).toBeInTheDocument()
    expect(screen.queryByTestId("create-agent-dialog")).not.toBeInTheDocument()
  })

  it("opens the agent dialog for ?new=agent", async () => {
    h.search = "new=agent"
    window.history.replaceState(null, "", "/crews?new=agent")
    renderProvider()
    expect(await screen.findByTestId("create-agent-dialog")).toBeInTheDocument()
    expect(screen.queryByTestId("create-crew-dialog")).not.toBeInTheDocument()
  })

  it("Z31 ?new=agent&crew=ops opens New agent in Ops", async () => {
    h.search = "new=agent&crew=ops"
    window.history.replaceState(null, "", "/crews?new=agent&crew=ops")
    renderProvider()
    expect(await screen.findByTestId("create-agent-dialog")).toHaveAttribute("data-crew", "ops")
  })

  it("consumes the param, so a reload does not reopen the dialog", async () => {
    h.search = "new=crew"
    window.history.replaceState(null, "", "/crews?new=crew")
    renderProvider()
    await screen.findByTestId("create-crew-dialog")
    await waitFor(() => expect(h.replace).toHaveBeenCalledWith("/crews", { scroll: false }))
  })

  it("opens nothing without the param, and nothing for a value it does not know", async () => {
    renderProvider()
    expect(screen.queryByTestId("create-crew-dialog")).not.toBeInTheDocument()
    expect(screen.queryByTestId("create-agent-dialog")).not.toBeInTheDocument()
    expect(h.replace).not.toHaveBeenCalled()

    cleanup()
    h.search = "new=banana"
    window.history.replaceState(null, "", "/crews?new=banana")
    renderProvider()
    expect(screen.queryByTestId("create-crew-dialog")).not.toBeInTheDocument()
    expect(screen.queryByTestId("create-agent-dialog")).not.toBeInTheDocument()
    expect(h.replace).not.toHaveBeenCalled()
  })
})

describe("Crews create — keys", () => {
  it("C opens New crew on Suggest a team", () => {
    renderProvider()
    fireEvent.keyDown(document.body, { key: "c" })
    expect(screen.getByTestId("create-crew-dialog")).toHaveAttribute("data-start", "suggest")
  })

  it("A opens New agent in the open crew", () => {
    renderProvider("ops")
    fireEvent.keyDown(document.body, { key: "a" })
    expect(screen.getByTestId("create-agent-dialog")).toHaveAttribute("data-crew", "ops")
  })

  it("keys typed into a field do nothing", () => {
    renderProvider()
    const input = document.createElement("input")
    document.body.appendChild(input)
    fireEvent.keyDown(input, { key: "c" })
    expect(screen.queryByTestId("create-crew-dialog")).not.toBeInTheDocument()
    input.remove()
  })
})
