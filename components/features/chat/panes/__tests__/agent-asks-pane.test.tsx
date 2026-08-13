import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// ---------------------------------------------------------------- stubs

let workspaceId: string | null = "ws-1"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId, loading: false }),
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

import { AgentAsksPane } from "../agent-asks-pane"
import { MAX_SUGGESTED_PROMPTS } from "@/lib/agent-suggestions"

// ---------------------------------------------------------------- helpers

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

function agent(extra: Record<string, unknown> = {}) {
  return {
    id: "agent-1",
    slug: "casey",
    name: "Casey",
    crew_id: null,
    role_title: "Engineering",
    agent_role: "engineering",
    suggested_prompts: null,
    ...extra,
  }
}

function pane() {
  return render(<AgentAsksPane agentId="agent-1" agentSlug="casey" />)
}

beforeEach(() => {
  workspaceId = "ws-1"
  apiFetch.mockReset()
})

// ---------------------------------------------------------------- tests

describe("AgentAsksPane", () => {
  it("shows a loading state while the agent record is in flight", async () => {
    apiFetch.mockImplementation(() => new Promise(() => {}))
    pane()
    expect(await screen.findByTestId("asks-pane-loading")).toBeInTheDocument()
  })

  it("renders the agent's configured asks, one row each", async () => {
    apiFetch.mockImplementation(() =>
      ok(agent({ suggested_prompts: "What broke last night?\nSummarise the open PRs\nWho owns billing?" })),
    )
    pane()

    const list = await screen.findByTestId("asks-list")
    expect(list).toBeInTheDocument()
    expect(screen.getAllByTestId("ask-row")).toHaveLength(3)
    expect(screen.getByText("What broke last night?")).toBeInTheDocument()
    expect(screen.getByText("Who owns billing?")).toBeInTheDocument()
  })

  it("caps the rendered asks at MAX_SUGGESTED_PROMPTS and drops blank lines", async () => {
    const lines = Array.from({ length: 12 }, (_, i) => `Question ${i + 1}`)
    // Blank + whitespace-only lines are noise the column may still hold.
    const raw = [lines[0], "", "   ", ...lines.slice(1)].join("\n")
    apiFetch.mockImplementation(() => ok(agent({ suggested_prompts: raw })))
    pane()

    await screen.findByTestId("asks-list")
    expect(screen.getAllByTestId("ask-row")).toHaveLength(MAX_SUGGESTED_PROMPTS)
    expect(screen.getByText("Question 8")).toBeInTheDocument()
    expect(screen.queryByText("Question 9")).not.toBeInTheDocument()
  })

  it("shows the empty state — naming the Chat suggestions card and linking to the config surface — when nothing is configured", async () => {
    apiFetch.mockImplementation(() => ok(agent({ suggested_prompts: null })))
    pane()

    expect(await screen.findByTestId("asks-empty")).toBeInTheDocument()
    expect(screen.getByText(/Chat suggestions/)).toBeInTheDocument()
    const link = screen.getByRole("link", { name: /chat suggestions|open .*config/i })
    expect(link).toHaveAttribute("href", "/crews?agent=casey")
  })

  it("does not fall back to the role packs — configuration, not chat chips, is what this pane shows", async () => {
    // "engineering" is a real ROLE_PACK key in lib/agent-suggestions.ts. The
    // chat composer falls back to it; this pane must not, or an operator would
    // read Crewship's defaults as questions their agent was configured with.
    apiFetch.mockImplementation(() =>
      ok(agent({ agent_role: "engineering", role_title: "Engineering", suggested_prompts: "   \n  \n" })),
    )
    pane()

    expect(await screen.findByTestId("asks-empty")).toBeInTheDocument()
    expect(screen.queryByText("Plan a refactor of the chat module")).not.toBeInTheDocument()
    expect(screen.queryByTestId("ask-row")).not.toBeInTheDocument()
  })

  it("shows an error state naming the failure, and retries on demand", async () => {
    apiFetch.mockImplementationOnce(() => Promise.resolve({ ok: false, status: 503, json: () => Promise.resolve({}) }))
    pane()

    const err = await screen.findByTestId("asks-error")
    expect(err).toHaveTextContent(/503/)

    apiFetch.mockImplementation(() => ok(agent({ suggested_prompts: "Recovered ask" })))
    fireEvent.click(screen.getByRole("button", { name: /retry/i }))

    await waitFor(() => expect(screen.getByText("Recovered ask")).toBeInTheDocument())
    expect(screen.queryByTestId("asks-error")).not.toBeInTheDocument()
  })
})
