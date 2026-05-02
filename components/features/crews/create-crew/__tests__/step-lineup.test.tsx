import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { StepLineup } from "../step-lineup"
import { INITIAL_STATE, type WizardState } from "../types"
import type { CrewTemplate } from "../api"

// =============================================================================
// Test fixtures — small but representative template list
// =============================================================================

const TPL_BUILTIN_ENG: CrewTemplate = {
  id: "1",
  slug: "software-development",
  name: "Software Development",
  description: "Tech Lead + Backend + Frontend + QA",
  icon: "code",
  color: "blue",
  category: "ENGINEERING",
  is_builtin: true,
  created_at: "2026-01-01T00:00:00Z",
  agents: [
    { name: "Tech Lead", slug: "tech-lead", role_title: "Lead", agent_role: "LEAD", cli_adapter: "CLAUDE_CODE", llm_provider: "ANTHROPIC", llm_model: "claude", tool_profile: "FULL", system_prompt: "" },
    { name: "Backend", slug: "backend", role_title: "BE", agent_role: "AGENT", cli_adapter: "CLAUDE_CODE", llm_provider: "ANTHROPIC", llm_model: "claude", tool_profile: "FULL", system_prompt: "" },
  ],
}

const TPL_BUILTIN_RESEARCH: CrewTemplate = {
  ...TPL_BUILTIN_ENG,
  id: "2",
  slug: "research-analysis",
  name: "Research & Analysis",
  category: "RESEARCH",
  description: "Research, Data Collector, Analyst",
  icon: "search",
  color: "cyan",
}

const TPL_WORKSPACE: CrewTemplate = {
  ...TPL_BUILTIN_ENG,
  id: "3",
  slug: "my-custom",
  name: "My Custom Template",
  is_builtin: false,
  category: "CUSTOM",
}

function harness(initial: Partial<WizardState> = {}, templates: CrewTemplate[] = []) {
  let state: WizardState = { ...INITIAL_STATE, ...initial }
  const setState = vi.fn((patch: Partial<WizardState>) => {
    state = { ...state, ...patch }
  })

  // Mock /api/v1/crew-templates fetch
  const fetchMock = vi.fn(async (url: string | URL) => {
    const u = typeof url === "string" ? url : url.toString()
    if (u.includes("/crew-templates")) {
      return { ok: true, json: async () => templates } as Response
    }
    return { ok: false, json: async () => ({}) } as Response
  })
  vi.stubGlobal("fetch", fetchMock)

  const r = render(<StepLineup state={state} setState={setState} />)
  return {
    ...r,
    setState,
    rerenderWith: (patch: Partial<WizardState>) => {
      state = { ...state, ...patch }
      r.rerender(<StepLineup state={state} setState={setState} />)
    },
  }
}

beforeEach(() => { /* fresh fetch stub per test */ })
afterEach(() => { vi.unstubAllGlobals() })

describe("<StepLineup> — mode tabs", () => {
  it("renders the two mode tabs (Browse + Empty) — no AI Suggest", () => {
    harness()
    expect(screen.getByRole("button", { name: /Browse templates/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Empty crew/ })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /AI Suggest/ })).toBeNull()
  })

  it("clicking the Empty tab sets mode to 'empty' and hides browser", async () => {
    const { setState, rerenderWith } = harness({ mode: "browse" }, [TPL_BUILTIN_ENG])
    await waitFor(() => {
      expect(screen.queryByText(/Loading templates/)).toBeNull()
    })

    fireEvent.click(screen.getByRole("button", { name: /Empty crew/ }))
    expect(setState).toHaveBeenCalledWith({ mode: "empty" })

    rerenderWith({ mode: "empty" })
    expect(screen.getByText(/Crew will be created with no agents/)).toBeInTheDocument()
  })
})

describe("<StepLineup> — empty mode", () => {
  it("shows the empty-state explanation", () => {
    harness({ mode: "empty" })
    // "Empty crew" appears twice — once in the mode tab, once as the heading.
    expect(screen.getAllByText("Empty crew").length).toBeGreaterThanOrEqual(2)
    // Body paragraph is split across <code> elements; matcher fires on every
    // parent, so allow multiple matches.
    const matches = screen.getAllByText((_, node) =>
      !!node?.textContent?.startsWith("Crew will be created with no agents"),
    )
    expect(matches.length).toBeGreaterThanOrEqual(1)
  })
})

describe("<StepLineup> — browse mode (template fetch)", () => {
  it("shows loading state while fetching", () => {
    harness({ mode: "browse" }, [])
    expect(screen.getByText(/Loading templates/)).toBeInTheDocument()
  })

  it("renders templates after fetch resolves", async () => {
    harness({ mode: "browse" }, [TPL_BUILTIN_ENG, TPL_BUILTIN_RESEARCH])

    await waitFor(() => {
      expect(screen.getAllByText("Software Development")).not.toHaveLength(0)
    })
  })

  it("hits /api/v1/crew-templates", async () => {
    harness({ mode: "browse" }, [TPL_BUILTIN_ENG])
    await waitFor(() => {
      expect((globalThis.fetch as unknown as ReturnType<typeof vi.fn>)).toHaveBeenCalledWith(
        expect.stringContaining("/api/v1/crew-templates"),
      )
    })
  })

  it("auto-picks the first template so preview renders without manual click", async () => {
    const { setState } = harness({ mode: "browse", pickedTemplateSlug: null }, [TPL_BUILTIN_ENG])

    await waitFor(() => {
      expect(setState).toHaveBeenCalledWith(
        expect.objectContaining({
          pickedTemplateSlug: "software-development",
          pickedTemplateMeta: expect.objectContaining({
            name: "Software Development",
            agentCount: 2,
          }),
        }),
      )
    })
  })

  it("clicking a template patches pickedTemplateSlug + meta", async () => {
    const { setState } = harness(
      { mode: "browse", pickedTemplateSlug: "software-development" },
      [TPL_BUILTIN_ENG, TPL_BUILTIN_RESEARCH],
    )

    await waitFor(() => {
      expect(screen.getAllByText("Research & Analysis")).not.toHaveLength(0)
    })

    setState.mockClear()
    fireEvent.click(screen.getAllByText("Research & Analysis")[0])

    expect(setState).toHaveBeenCalledWith(
      expect.objectContaining({
        pickedTemplateSlug: "research-analysis",
        pickedTemplateMeta: expect.objectContaining({ name: "Research & Analysis", agentCount: 2 }),
      }),
    )
  })

  it("preview pane renders the lineup of the selected template (LEAD first)", async () => {
    harness(
      { mode: "browse", pickedTemplateSlug: "software-development" },
      [TPL_BUILTIN_ENG],
    )

    await waitFor(() => {
      expect(screen.getAllByText("Tech Lead")).not.toHaveLength(0)
    })
    // LEAD pill is rendered for Tech Lead
    expect(screen.getByText("LEAD")).toBeInTheDocument()
  })
})

describe("<StepLineup> — search + filter", () => {
  it("search filters templates by name (case-insensitive)", async () => {
    harness({ mode: "browse" }, [TPL_BUILTIN_ENG, TPL_BUILTIN_RESEARCH])
    await waitFor(() => {
      expect(screen.getAllByText("Software Development")).not.toHaveLength(0)
    })

    const searchInput = document.querySelector('input[placeholder*="Search templates"]') as HTMLInputElement
    fireEvent.change(searchInput, { target: { value: "research" } })

    expect(screen.getAllByText("Research & Analysis")).not.toHaveLength(0)
    expect(screen.queryByText("Software Development")).toBeNull()
  })

  it("source-tab Marketplace is disabled and shows 'soon'", async () => {
    harness({ mode: "browse" }, [TPL_BUILTIN_ENG])
    await waitFor(() => {
      expect(screen.getAllByText("Software Development")).not.toHaveLength(0)
    })
    const marketplaceTab = screen.getByRole("button", { name: /Marketplace/ })
    expect(marketplaceTab).toBeDisabled()
    expect(screen.getByText("soon")).toBeInTheDocument()
  })

  it("Built-in tab shows only is_builtin=true templates", async () => {
    harness({ mode: "browse" }, [TPL_BUILTIN_ENG, TPL_WORKSPACE])
    await waitFor(() => {
      expect(screen.getAllByText("Software Development")).not.toHaveLength(0)
    })

    // Built-in is the default tab; My Custom Template should NOT be visible.
    expect(screen.queryByText("My Custom Template")).toBeNull()
  })

  it("Workspace tab shows only non-builtin templates", async () => {
    harness({ mode: "browse" }, [TPL_BUILTIN_ENG, TPL_WORKSPACE])
    await waitFor(() => {
      expect(screen.getAllByText("Software Development")).not.toHaveLength(0)
    })

    fireEvent.click(screen.getByRole("button", { name: /Workspace/ }))
    expect(screen.queryByText("Software Development")).toBeNull()
    expect(screen.getAllByText("My Custom Template")).not.toHaveLength(0)
  })

  it("category chips filter by category", async () => {
    harness({ mode: "browse" }, [TPL_BUILTIN_ENG, TPL_BUILTIN_RESEARCH])
    await waitFor(() => {
      expect(screen.getAllByText("Software Development")).not.toHaveLength(0)
    })

    // Category chips include name + count, e.g. "research 1". Use accessible
    // name + count to disambiguate from the result rows that also say "research".
    const buttons = screen.getAllByRole("button")
    const researchChip = buttons.find((b) =>
      /^research\s+\d/.test(b.textContent ?? "") || b.textContent === "research 1",
    )
    expect(researchChip).toBeDefined()
    fireEvent.click(researchChip!)

    expect(screen.queryByText("Software Development")).toBeNull()
    expect(screen.getAllByText("Research & Analysis")).not.toHaveLength(0)
  })
})

describe("<StepLineup> — fetch failure", () => {
  it("shows fail-to-load fallback when fetch errors", async () => {
    const fetchMock = vi.fn(async () => ({
      ok: false,
      status: 500,
      json: async () => ({}),
    } as Response))
    vi.stubGlobal("fetch", fetchMock)

    let state: WizardState = { ...INITIAL_STATE, mode: "browse" }
    render(<StepLineup state={state} setState={(p) => { state = { ...state, ...p } }} />)

    await waitFor(() => {
      expect(screen.getByText(/Failed to load/)).toBeInTheDocument()
    })
  })
})
