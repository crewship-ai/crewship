import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CreateAgentDialog } from "../create-agent-dialog"

// Stub next/navigation — the dialog calls router.replace on success.
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
}))

// Stub sonner toasts so we can assert without rendering them.
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

// Avoid loading the real DiceBear styles (large) — getAgentAvatarUrl is fine
// to call, it just returns a data URI; nothing to mock.

const CREWS = [
  { id: "c1", slug: "engineering", name: "Engineering" },
  { id: "c2", slug: "research", name: "Research" },
]

/**
 * The dialog reads two catalogues on mount — GET /api/v1/integrations and
 * GET /api/v1/notification-channels — for the Tools & notifications section.
 * vitest.setup.ts fails any unmocked network call, so every test needs an
 * answer for them whether or not it cares about the section.
 *
 * Answering them here rather than per-test also keeps the submit assertions
 * honest: they are about the POST /api/v1/agents call, not about the dialog
 * happening to make exactly one request in its whole life.
 */
function catalogueResponse(url: string): Response | null {
  if (url.includes("/api/v1/integrations")) {
    return new Response("[]", { status: 200, headers: { "Content-Type": "application/json" } })
  }
  if (url.includes("/api/v1/notification-channels")) {
    return new Response('{"channels":[]}', { status: 200, headers: { "Content-Type": "application/json" } })
  }
  return null
}

/** Stub fetch: catalogues answered, everything else served by `rest`. */
function stubFetch(rest: () => Response | Promise<Response>) {
  return vi.spyOn(global, "fetch").mockImplementation(async (url) => {
    return catalogueResponse(String(url)) ?? (await rest())
  })
}

/** The POST the dialog exists to make, picked out of the catalogue traffic. */
function agentsPost(spy: ReturnType<typeof vi.spyOn>) {
  return spy.mock.calls.find(
    ([url, init]) => String(url).includes("/api/v1/agents") && (init as RequestInit | undefined)?.method === "POST",
  )
}

describe("CreateAgentDialog", () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    // Default: catalogues answered, anything else is a test that forgot to
    // say what it expects.
    stubFetch(() => new Response("{}", { status: 200 }))
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  function renderDialog(
    overrides: Partial<Parameters<typeof CreateAgentDialog>[0]> = {},
  ) {
    const props = {
      workspaceId: "ws-1",
      open: true,
      onOpenChange: vi.fn(),
      defaultCrewSlug: "engineering",
      crews: CREWS,
      onCreated: vi.fn(),
      ...overrides,
    }
    const utils = render(<CreateAgentDialog {...props} />)
    return { ...utils, props }
  }

  it("renders header + footer with disabled Create when name is empty", () => {
    renderDialog()
    expect(screen.getByText("New agent")).toBeInTheDocument()
    const createBtn = screen.getByRole("button", { name: /create agent/i })
    expect(createBtn).toBeDisabled()
  })

  it("shows the empty-crews banner when crews list is empty", () => {
    renderDialog({ crews: [], defaultCrewSlug: null })
    expect(screen.getByText(/no crews yet/i)).toBeInTheDocument()
  })

  it("does NOT show empty-crews banner when crews are present", () => {
    renderDialog()
    expect(screen.queryByText(/no crews yet/i)).not.toBeInTheDocument()
  })

  // ── The shared shell ────────────────────────────────────────────────
  // This surface used to draw its own modal: a bare DialogContent pinned
  // to sm:max-w-[640px] with a hand-rolled header, a scrollport that
  // swallowed the footer's height budget, and a window-level ⌘↵ listener.
  // It now mounts components/layout/create-surface.tsx like every other
  // create door, so the assertions below are about the SHELL, not about
  // anything this dialog draws itself.
  it("mounts the shared CreateSurface shell at size lg", () => {
    renderDialog()
    const content = document.querySelector('[data-slot="dialog-content"]')
    expect(content).not.toBeNull()
    const cls = content!.className
    // The shell's own geometry: the surface group, the fixed lg width,
    // and the bottom-sheet breakpoint it brings for free.
    expect(cls).toContain("group/surface")
    expect(cls).toContain("sm:max-w-[800px]")
    expect(cls).toContain("max-sm:rounded-t-2xl")
  })

  it("still issues the same POST from inside the shell", async () => {
    const fetchSpy = stubFetch(() =>
      new Response(JSON.stringify({ id: "a1", name: "Filip", slug: "filip" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )
    renderDialog()

    // The primary lives in the shell's footer now, outside the scrollport.
    const content = document.querySelector('[data-slot="dialog-content"]')!
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Filip" } })
    const createBtn = screen.getByRole("button", { name: /create agent/i })
    expect(content.contains(createBtn)).toBe(true)
    fireEvent.click(createBtn)

    await waitFor(() => expect(agentsPost(fetchSpy)).toBeDefined())
    const [url, init] = agentsPost(fetchSpy)!
    expect(String(url)).toContain("/api/v1/agents")
    expect((init as RequestInit).method).toBe("POST")
  })

  it("shows a refusal band — not just a toast — when the server says no", async () => {
    stubFetch(() => new Response("slug taken", { status: 409 }))
    renderDialog()
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Filip" } })
    fireEvent.click(screen.getByRole("button", { name: /create agent/i }))

    // The band sits between the body and the footer, so it cannot be
    // scrolled past the way the old toast could be missed entirely.
    const band = await screen.findByRole("alert")
    expect(band).toHaveTextContent(/slug taken/i)
  })

  it("asks before throwing away typed input on Esc", async () => {
    const onOpenChange = vi.fn()
    renderDialog({ onOpenChange })
    fireEvent.change(screen.getByPlaceholderText("Filip"), { target: { value: "Filip" } })

    fireEvent.keyDown(document.body, { key: "Escape", code: "Escape" })

    // The shell's discard guard: the dialog does not close until confirmed.
    await screen.findByText(/unsaved input/i)
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
  })

  it("shows validation hint when name is too short", () => {
    renderDialog()
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "X" } })
    expect(screen.getByText(/at least 2 characters/i)).toBeInTheDocument()
  })

  it("auto-derives slug from name", () => {
    renderDialog()
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "Filip Test" } })
    const slugInput = screen.getByPlaceholderText("filip") as HTMLInputElement
    expect(slugInput.value).toBe("filip-test")
  })

  it("preserves user-typed slug when manually edited", () => {
    renderDialog()
    const slugInput = screen.getByPlaceholderText("filip") as HTMLInputElement
    fireEvent.change(slugInput, { target: { value: "manual-slug" } })
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "Different Name" } })
    expect(slugInput.value).toBe("manual-slug")
  })

  it("submits the right body shape and signals onCreated on success", async () => {
    const fetchSpy = stubFetch(() =>
      new Response(JSON.stringify({ id: "a1", name: "Filip", slug: "filip" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )

    const { props } = renderDialog()

    // Fill the minimum required fields.
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "Filip" } })

    // Submit.
    const createBtn = screen.getByRole("button", { name: /create agent/i })
    expect(createBtn).not.toBeDisabled()
    fireEvent.click(createBtn)

    await waitFor(() => expect(agentsPost(fetchSpy)).toBeDefined())

    const [url, rawInit] = agentsPost(fetchSpy)!
    const init = rawInit as RequestInit
    expect(String(url)).toContain("/api/v1/agents")
    expect(String(url)).toContain("workspace_id=ws-1")
    expect(init.method).toBe("POST")

    const body = JSON.parse(init.body as string)
    // The drift-detector list — must exactly match agents_create.go's JSON
    // tags. Same set as in agent-draft.test.ts but verified at the
    // component layer through a real submit.
    expect(Object.keys(body).sort()).toEqual([
      "agent_role",
      "avatar_seed",
      "avatar_style",
      "cli_adapter",
      "crew_id",
      "description",
      "lead_mode",
      "llm_model",
      "llm_provider",
      "memory_enabled",
      "name",
      "role_title",
      "slug",
      "system_prompt",
      "timeout_seconds",
      "tool_profile",
    ])
    expect(body.name).toBe("Filip")
    expect(body.slug).toBe("filip")
    expect(body.crew_id).toBe("c1") // resolved from defaultCrewSlug "engineering"
    expect(body.agent_role).toBe("AGENT")
    expect(body.lead_mode).toBeNull() // not LEAD → null
    expect(props.onCreated).toHaveBeenCalledWith("filip")
    expect(props.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("does NOT submit when validation fails (name too short)", () => {
    const fetchSpy = stubFetch(() => new Response("{}", { status: 200 }))
    renderDialog()
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "X" } })
    const createBtn = screen.getByRole("button", { name: /create agent/i })
    expect(createBtn).toBeDisabled()
    fireEvent.click(createBtn)
    // The catalogue reads fire on mount regardless; what must not happen is
    // the create.
    expect(agentsPost(fetchSpy)).toBeUndefined()
  })

  it("does NOT close on backend 4xx — keeps the form so the user can retry", async () => {
    stubFetch(() => new Response("slug taken", { status: 409 }))
    const { props } = renderDialog()
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "Filip" } })
    fireEvent.click(screen.getByRole("button", { name: /create agent/i }))
    await waitFor(() => {
      expect(props.onCreated).not.toHaveBeenCalled()
      expect(props.onOpenChange).not.toHaveBeenCalled()
    })
  })

  // -- Role is a choice, not a dropdown -----------------------------------
  //
  // Two options, both short. The kit's own note on CreateSurfaceChoice says
  // that beats a <select>: every option is visible without opening anything,
  // and on a phone the target is the whole chip rather than a 16px caret.
  // Tool profile further down this same form already used it, so the form
  // disagreed with itself — and /design's specimen for this door shows the
  // chip row.
  describe("the Role control", () => {
    it("offers both roles as visible options rather than a dropdown", async () => {
      renderDialog()
      await waitFor(() => expect(screen.getByRole("radio", { name: /^Agent/ })).toBeInTheDocument())

      expect(screen.getByRole("radio", { name: /^Agent/ })).toHaveAttribute("aria-checked", "true")
      expect(screen.getByRole("radio", { name: /^Lead/ })).toHaveAttribute("aria-checked", "false")
      // The <select> it replaces.
      expect(document.querySelector("#agent-role")).toBeNull()
    })

    it("says what each role does instead of parenthesising a limit", async () => {
      renderDialog()
      const lead = await screen.findByRole("radio", { name: /^Lead/ })
      // CreateSurfaceChoice carries a hint as `title`, so this is a tooltip
      // rather than visible text — a real limitation on touch, but it is the
      // kit's behaviour for all twelve doors and not something to fork here.
      // What matters for this door is that the hint states the capability;
      // "Lead (1 per crew)" stated the limit and not what a Lead can do.
      expect(lead).toHaveAttribute("title", expect.stringMatching(/plan and delegate/i))
    })

    it("switches role on click", async () => {
      renderDialog()
      fireEvent.click(await screen.findByRole("radio", { name: /^Lead/ }))
      await waitFor(() =>
        expect(screen.getByRole("radio", { name: /^Lead/ })).toHaveAttribute("aria-checked", "true"),
      )
    })
  })

  // -- The crew field is a picker, not a native list ----------------------
  //
  // It was a <select> of every crew in the workspace: one alphabetical
  // column of names, no icon, no colour, no search. A dev box accumulates
  // dozens of crews and they are told apart everywhere else in the product
  // by exactly the two things this control dropped.
  describe("the Crew picker", () => {
    it("is a searchable combobox rather than a <select>", async () => {
      renderDialog()
      await waitFor(() =>
        expect(screen.getByRole("combobox", { name: "Crew" })).toBeInTheDocument(),
      )
      expect(document.querySelector("select#agent-crew")).toBeNull()
    })

    it("separates crews that have agents from empty ones", async () => {
      renderDialog({
        defaultCrewSlug: null,
        crews: [
          { id: "c1", slug: "engineering", name: "Engineering", agentCount: 3 },
          { id: "c2", slug: "e2e-empty", name: "E2E Empty", agentCount: 0 },
        ],
      })
      fireEvent.click(await screen.findByRole("combobox", { name: "Crew" }))

      // Without the split, an operator's real crews sit interleaved with a
      // hundred throwaway ones in alphabetical order.
      expect(await screen.findByText("With agents")).toBeInTheDocument()
      expect(screen.getByText("Empty")).toBeInTheDocument()
    })

    it("stays one flat list when no caller supplied counts", async () => {
      renderDialog({ defaultCrewSlug: null })
      fireEvent.click(await screen.findByRole("combobox", { name: "Crew" }))

      await waitFor(() => expect(screen.getByPlaceholderText(/search crews/i)).toBeInTheDocument())
      // A heading that always says the same thing is noise.
      expect(screen.queryByText("With agents")).toBeNull()
    })

    it("selects a crew by slug, which is what the draft keys on", async () => {
      renderDialog({ defaultCrewSlug: null })
      fireEvent.click(await screen.findByRole("combobox", { name: "Crew" }))
      fireEvent.click(await screen.findByText("Research"))

      await waitFor(() =>
        expect(screen.getByRole("combobox", { name: "Crew" }).textContent).toContain("Research"),
      )
    })
  })
})
