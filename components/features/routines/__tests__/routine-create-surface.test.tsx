import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

// =============================================================================
// New routine / Import routine bundle, on the shared create shell.
//
// These two doors were the last pair still drawing their own modal: a
// hand-rolled `fixed inset-0` with `bg-black/50`, a width that changed three
// times between the entry cards (576px), the fork list (672px) and the editor
// (768px at 90vh), and a footer that was part of the scrolling column. What
// they must NOT lose in the move is the part that is not chrome: the inline
// /test_run that mints the HMAC save_token, and the /save that spends it.
// =============================================================================

const h = vi.hoisted(() => ({
  role: "ADMIN" as string,
  calls: [] as { url: string; body: unknown }[],
}))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role }) }))
vi.mock("./../routine-definition-canvas", () => ({
  RoutineDefinitionCanvas: () => <div data-testid="graph" />,
}))
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: (p: { code: string; language: string }) => (
    <div data-testid="editor" data-language={p.language}>
      {p.code}
    </div>
  ),
}))
vi.mock("@/lib/api-fetch", () => ({
  broadcastSignOut: vi.fn(),
  broadcastSessionExpired: vi.fn(),
  tryRefresh: vi.fn(),
  AUTH_EVENT: "crewship:session-expired",
  AUTH_CHANNEL: "crewship-auth",
  apiFetch: vi.fn(async (url: string, init?: RequestInit) => {
    h.calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    if (url.includes("/test_run")) {
      return { ok: true, json: async () => ({ status: "DRY_RUN_OK", save_token: "tok-1" }) }
    }
    if (url.endsWith("/pipelines/save")) {
      return { ok: true, json: async () => ({ slug: "my-routine" }) }
    }
    if (url.endsWith("/pipelines/import")) {
      return { ok: true, json: async () => ({ imported: 1 }) }
    }
    return { ok: true, json: async () => [] }
  }),
}))

import { RoutineCreateDialog } from "../routine-create-dialog"
import { ImportRoutineDialog } from "../routines-layout"

const PROPS = { workspaceId: "ws-1", open: true, onClose: vi.fn(), onCreated: vi.fn() }

function shell() {
  return document.querySelector('[data-slot="dialog-content"]')
}

describe("New routine on CreateSurface", () => {
  beforeEach(() => {
    cleanup()
    h.calls.length = 0
    h.role = "ADMIN"
    vi.clearAllMocks()
  })

  it("mounts the shared shell at one fixed width", () => {
    render(<RoutineCreateDialog {...PROPS} />)
    const content = shell()
    expect(content).not.toBeNull()
    // lg — 800px — and it does not change between the entry cards, the fork
    // list and the editor. That was three widths and two heights before.
    expect(content!.className).toContain("sm:max-w-[800px]")
    expect(screen.getByRole("dialog")).toHaveTextContent("New routine")
  })

  it("keeps the same width in the editor", () => {
    render(<RoutineCreateDialog {...PROPS} />)
    fireEvent.click(screen.getByText("Write it yourself"))
    expect(shell()!.className).toContain("sm:max-w-[800px]")
    expect(screen.getByTestId("editor")).toBeInTheDocument()
  })

  it("still test-runs inline and spends the minted save_token on save", async () => {
    render(<RoutineCreateDialog {...PROPS} />)
    fireEvent.click(screen.getByText("Write it yourself"))
    fireEvent.click(screen.getByRole("button", { name: /test & save/i }))

    await waitFor(() => {
      expect(h.calls.some((c) => c.url.endsWith("/pipelines/save"))).toBe(true)
    })

    const test = h.calls.find((c) => c.url.includes("/test_run"))!
    expect(test.url).toBe("/api/v1/workspaces/ws-1/pipelines/test_run")
    expect(test.body).toMatchObject({ sample_inputs: {} })
    expect((test.body as { definition: Record<string, unknown> }).definition).toMatchObject({
      name: "my-routine",
    })

    const save = h.calls.find((c) => c.url.endsWith("/pipelines/save"))!
    expect(save.body).toMatchObject({
      slug: "my-routine",
      // The token the test run minted, threaded explicitly — state would not
      // be visible to a save invoked in the same tick.
      save_token: "tok-1",
      skip_test_gate: false,
    })
    // /test_run before /save, never the other way round.
    expect(h.calls.findIndex((c) => c.url.includes("/test_run"))).toBeLessThan(
      h.calls.findIndex((c) => c.url.endsWith("/pipelines/save")),
    )
  })

  it("saves without a test run when an OWNER/ADMIN skips the gate", async () => {
    render(<RoutineCreateDialog {...PROPS} />)
    fireEvent.click(screen.getByText("Write it yourself"))
    fireEvent.click(screen.getByLabelText(/skip test-run gate/i))
    fireEvent.click(screen.getByRole("button", { name: /save \(skip test\)/i }))

    await waitFor(() => {
      expect(h.calls.some((c) => c.url.endsWith("/pipelines/save"))).toBe(true)
    })
    expect(h.calls.some((c) => c.url.includes("/test_run"))).toBe(false)
    expect(h.calls.find((c) => c.url.endsWith("/pipelines/save"))!.body).toMatchObject({
      skip_test_gate: true,
    })
  })
})

describe("Import routine bundle on CreateSurface", () => {
  beforeEach(() => {
    cleanup()
    h.calls.length = 0
    vi.clearAllMocks()
  })

  it("mounts the shared shell at sm", () => {
    render(<ImportRoutineDialog workspaceId="ws-1" onClose={() => {}} onImported={() => {}} />)
    expect(shell()!.className).toContain("sm:max-w-[480px]")
  })

  it("posts the parsed bundle to the same endpoint", async () => {
    const onImported = vi.fn()
    render(<ImportRoutineDialog workspaceId="ws-1" onClose={() => {}} onImported={onImported} />)
    fireEvent.change(screen.getByPlaceholderText(/"slug"/), {
      target: { value: '{"slug":"nightly","definition":{"name":"nightly"}}' },
    })
    fireEvent.click(screen.getByRole("button", { name: /^import$/i }))

    await waitFor(() => expect(onImported).toHaveBeenCalled())
    expect(h.calls[0]).toEqual({
      url: "/api/v1/workspaces/ws-1/pipelines/import",
      body: { slug: "nightly", definition: { name: "nightly" } },
    })
  })

  it("shows a refusal that does not scroll away when the bundle is not JSON", async () => {
    render(<ImportRoutineDialog workspaceId="ws-1" onClose={() => {}} onImported={() => {}} />)
    fireEvent.change(screen.getByPlaceholderText(/"slug"/), { target: { value: "not json" } })
    fireEvent.click(screen.getByRole("button", { name: /^import$/i }))
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument())
  })

  // The copy described a behaviour the endpoint does not have: Import's save
  // returns 409 "slug already exists in workspace" on a collision and writes
  // nothing (internal/api/pipelines_crud.go). Telling an operator their
  // existing routine may be replaced is worse than saying nothing — it invites
  // them to export a backup they do not need, and it would be a data-loss
  // warning if the endpoint ever grew the behaviour it names.
  it("does not promise a replace the endpoint refuses to do", () => {
    render(<ImportRoutineDialog workspaceId="ws-1" onClose={() => {}} onImported={() => {}} />)
    const body = shell()!.textContent ?? ""
    expect(body).not.toMatch(/existing routine is replaced/i)
    expect(body).toMatch(/refused/i)
    expect(body).toMatch(/nothing already saved is overwritten/i)
  })

  it("carries the breadcrumb every other door has", () => {
    render(<ImportRoutineDialog workspaceId="ws-1" onClose={() => {}} onImported={() => {}} />)
    expect(screen.getByText("Routines")).toBeInTheDocument()
  })
})
