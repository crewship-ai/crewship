import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { StepLineup } from "../step-lineup"
import { INITIAL_STATE, type WizardState } from "../types"
import type { CrewTemplate } from "../api"

// =============================================================================
// New crew → Lineup, as a grid of tiles.
//
// The step used to be a two-pane browser: a Browse/Empty mode tab strip, four
// source tabs, a row of category chips and a 320px preview pane. These tests
// cover what survived that — the request, the pick, the identity a template
// lends the crew — and the two things the shape change is FOR: every template
// visible as a tile, and "start empty" as one of the options rather than a
// different mode of the screen.
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

  const fetchMock = vi.fn(async (url: string | URL) => {
    const u = typeof url === "string" ? url : url.toString()
    if (u.includes("/crew-templates")) {
      return { ok: true, json: async () => templates } as Response
    }
    return { ok: false, json: async () => ({}) } as Response
  })
  vi.stubGlobal("fetch", fetchMock)

  const onImport = vi.fn()
  const r = render(<StepLineup state={state} setState={setState} workspaceId="ws_probe" onImport={onImport} />)
  return {
    ...r,
    setState,
    onImport,
    getState: () => state,
    rerenderWith: (patch: Partial<WizardState>) => {
      state = { ...state, ...patch }
      r.rerender(<StepLineup state={state} setState={setState} workspaceId="ws_probe" onImport={onImport} />)
    },
  }
}

const tile = (name: RegExp) => screen.getByRole("button", { name })

afterEach(() => { vi.unstubAllGlobals() })

describe("<StepLineup> — the request", () => {
  it("hits /api/v1/crew-templates", async () => {
    harness({}, [TPL_BUILTIN_ENG])
    await waitFor(() => {
      expect(globalThis.fetch as unknown as ReturnType<typeof vi.fn>).toHaveBeenCalledWith(
        expect.stringContaining("/api/v1/crew-templates"),
        expect.objectContaining({ credentials: "include" }),
      )
    })
  })

  // The route is wsCtx-wrapped (router_crews.go) and RequireWorkspace replies
  // 400 "workspace_id is required" when the request carries none in query,
  // path or header. Asserting the path prefix alone let a request the server
  // refuses outright pass — and it did, all the way onto dev2, where this
  // step read "Failed to load: HTTP 400" with an empty catalogue.
  it("sends workspace_id — the route refuses the request without it", async () => {
    harness({}, [TPL_BUILTIN_ENG])
    await waitFor(() => {
      const calls = (globalThis.fetch as unknown as ReturnType<typeof vi.fn>).mock.calls
      const url = String(calls.find((c) => String(c[0]).includes("/crew-templates"))?.[0] ?? "")
      expect(new URL(url, "http://x").searchParams.get("workspace_id")).toBe("ws_probe")
    })
  })

  it("says the catalogue failed, and still offers the empty crew", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 500, json: async () => ({}) } as Response)))
    let state: WizardState = { ...INITIAL_STATE }
    render(<StepLineup state={state} setState={(p) => { state = { ...state, ...p } }} workspaceId="ws_probe" onImport={vi.fn()} />)

    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(/did not load/i))
    // A failed catalogue must not be a dead end: this is the one option that
    // needs no server data at all.
    expect(tile(/^Start empty/)).toBeInTheDocument()
  })
})

describe("<StepLineup> — the tiles", () => {
  it("shows every template as a tile, with its agent count", async () => {
    harness({}, [TPL_BUILTIN_ENG, TPL_BUILTIN_RESEARCH])

    const eng = await screen.findByRole("button", { name: /^Software Development/ })
    expect(eng).toHaveTextContent("Tech Lead + Backend + Frontend + QA")
    expect(eng).toHaveTextContent("2 agents")
    expect(screen.getByRole("button", { name: /^Research & Analysis/ })).toBeInTheDocument()
  })

  it("marks a workspace template as such, so it is not read as built-in", async () => {
    harness({}, [TPL_WORKSPACE])
    const custom = await screen.findByRole("button", { name: /^My Custom Template/ })
    expect(custom).toHaveTextContent(/workspace/i)
  })

  it("picks nothing until asked — the step is a choice, not a default", async () => {
    const { setState } = harness({}, [TPL_BUILTIN_ENG, TPL_BUILTIN_RESEARCH])
    await screen.findByRole("button", { name: /^Software Development/ })

    // The old browser auto-selected filtered[0], so the wizard advanced with a
    // template nobody had looked at.
    expect(setState).not.toHaveBeenCalledWith(
      expect.objectContaining({ pickedTemplateSlug: expect.any(String) }),
    )
  })

  it("clicking a tile records the slug and the lineup meta", async () => {
    const { setState } = harness({}, [TPL_BUILTIN_ENG])
    fireEvent.click(await screen.findByRole("button", { name: /^Software Development/ }))

    expect(setState).toHaveBeenCalledWith(
      expect.objectContaining({
        mode: "browse",
        pickedTemplateSlug: "software-development",
        pickedTemplateMeta: expect.objectContaining({ name: "Software Development", agentCount: 2 }),
      }),
    )
  })

  it("marks the picked tile as pressed", async () => {
    const { rerenderWith } = harness({}, [TPL_BUILTIN_ENG])
    await screen.findByRole("button", { name: /^Software Development/ })

    rerenderWith({ pickedTemplateSlug: "software-development" })
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /^Software Development/ })).toHaveAttribute("aria-pressed", "true"),
    )
  })
})

describe("<StepLineup> — the identity a template lends", () => {
  it("adopts the template's name, slug and face while identity is untouched", async () => {
    const { setState } = harness({ name: "", slug: "" }, [TPL_BUILTIN_ENG])
    fireEvent.click(await screen.findByRole("button", { name: /^Software Development/ }))

    expect(setState).toHaveBeenCalledWith(
      expect.objectContaining({ name: "Software Development", slug: "software-development", icon: "code" }),
    )
  })

  it("leaves a name somebody typed alone", async () => {
    const { setState } = harness({ name: "Platform", slug: "platform" }, [TPL_BUILTIN_ENG])
    fireEvent.click(await screen.findByRole("button", { name: /^Software Development/ }))

    const patch = setState.mock.calls.at(-1)![0]
    expect(patch).not.toHaveProperty("name")
    expect(patch).not.toHaveProperty("slug")
  })
})

describe("<StepLineup> — empty crew", () => {
  it("is a tile beside the templates, not a separate mode of the screen", async () => {
    harness({}, [TPL_BUILTIN_ENG])
    const empty = await screen.findByRole("button", { name: /^Start empty/ })
    expect(empty).toHaveTextContent(/hire into it/i)
    // The tab strip it replaces.
    expect(screen.queryByRole("button", { name: /Browse templates/ })).toBeNull()
  })

  it("clears any template pick when chosen", async () => {
    const { setState } = harness({ pickedTemplateSlug: "software-development" }, [TPL_BUILTIN_ENG])
    fireEvent.click(await screen.findByRole("button", { name: /^Start empty/ }))

    expect(setState).toHaveBeenCalledWith({
      mode: "empty",
      pickedTemplateSlug: null,
      pickedTemplateMeta: null,
    })
  })
})

describe("<StepLineup> — the two ways in that are not a template", () => {
  it("puts Start empty and Import above the grid, not inside it", async () => {
    harness({}, [TPL_BUILTIN_ENG])
    await screen.findByRole("button", { name: /^Software Development/ })

    // Both actions precede every template in DOM order, which is what makes
    // them a row above the grid rather than two more tiles to scan past.
    const buttons = screen.getAllByRole("button").map((b) => b.textContent ?? "")
    const empty = buttons.findIndex((t) => t.startsWith("Start empty"))
    const importIdx = buttons.findIndex((t) => t.startsWith("Import YAML"))
    const template = buttons.findIndex((t) => t.startsWith("Software Development"))

    expect(empty).toBeGreaterThanOrEqual(0)
    expect(importIdx).toBeGreaterThan(empty)
    expect(template).toBeGreaterThan(importIdx)
  })

  it("hands importing to the wizard rather than opening anything here", async () => {
    // Same contract the icon and base-image pickers follow: the step asks for
    // the panel, the surface owns it.
    const { onImport, setState } = harness({}, [TPL_BUILTIN_ENG])
    fireEvent.click(await screen.findByRole("button", { name: /^Import YAML/ }))

    expect(onImport).toHaveBeenCalledTimes(1)
    // Import is not a lineup choice — clicking it must not select anything.
    expect(setState).not.toHaveBeenCalled()
  })

  it("still labels the templates as the other option", async () => {
    harness({}, [TPL_BUILTIN_ENG])
    expect(await screen.findByText(/or start from a template/i)).toBeInTheDocument()
  })
})

describe("<StepLineup> — search", () => {
  it("filters by name, description or category", async () => {
    harness({}, [TPL_BUILTIN_ENG, TPL_BUILTIN_RESEARCH])
    await screen.findByRole("button", { name: /^Software Development/ })

    fireEvent.change(screen.getByLabelText(/search crew templates/i), { target: { value: "research" } })

    expect(screen.getByRole("button", { name: /^Research & Analysis/ })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /^Software Development/ })).toBeNull()
    // The empty option is not a search result and must not be filtered away.
    expect(screen.getByRole("button", { name: /^Start empty/ })).toBeInTheDocument()
  })

  it("drops a pick the search has hidden, rather than deploying it unseen", async () => {
    const { setState } = harness({ pickedTemplateSlug: "software-development" }, [TPL_BUILTIN_ENG, TPL_BUILTIN_RESEARCH])
    await screen.findByRole("button", { name: /^Software Development/ })

    fireEvent.change(screen.getByLabelText(/search crew templates/i), { target: { value: "research" } })

    await waitFor(() =>
      expect(setState).toHaveBeenCalledWith({ pickedTemplateSlug: null, pickedTemplateMeta: null }),
    )
  })

  it("says so when nothing matches", async () => {
    harness({}, [TPL_BUILTIN_ENG])
    await screen.findByRole("button", { name: /^Software Development/ })

    fireEvent.change(screen.getByLabelText(/search crew templates/i), { target: { value: "zzz" } })
    expect(screen.getByText(/Nothing matches/)).toBeInTheDocument()
  })
})
