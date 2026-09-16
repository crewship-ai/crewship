import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

// =============================================================================
// Import routine bundle, on the shared create shell.
//
// The New routine editor that used to share this file is gone (the operator
// console replaced it with the New / Edit / Publish dialogs, each tested on
// its own); the import door stays, and stays on the shared shell.
// =============================================================================

const h = vi.hoisted(() => ({
  role: "ADMIN" as string,
  calls: [] as { url: string; body: unknown }[],
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ role: h.role }),
}))
vi.mock("./../routine-definition-canvas", () => ({
  RoutineDefinitionCanvas: () => <div data-testid="graph" />,
}))
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: (p: {
    code: string
    language: string
    onDocChange: (value: string) => void
  }) => (
    <textarea
      data-testid="editor"
      data-language={p.language}
      defaultValue={p.code}
      onChange={(event) => p.onDocChange(event.target.value)}
    />
  ),
}))
vi.mock("@/lib/api-fetch", () => ({
  broadcastSignOut: vi.fn(),
  broadcastSessionExpired: vi.fn(),
  tryRefresh: vi.fn(),
  AUTH_EVENT: "crewship:session-expired",
  AUTH_CHANNEL: "crewship-auth",
  apiFetch: vi.fn(async (url: string, init?: RequestInit) => {
    h.calls.push({
      url,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    })
    if (url.endsWith("/draft"))
      return {
        ok: true,
        json: async () => ({
          id: "",
          slug: url.split("/").at(-2),
          revision: 0,
          base_pipeline_id: "",
          base_revision: 0,
          document: {},
        }),
      }
    if (url.endsWith("/drafts") && init?.method === "POST")
      return {
        ok: true,
        json: async () => ({
          ...JSON.parse(String(init.body)),
          id: "draft-1",
          revision: 1,
        }),
      }
    if (url.includes("/test_run")) {
      return {
        ok: true,
        json: async () => ({ status: "DRY_RUN_OK", save_token: "tok-1" }),
      }
    }
    if (url.endsWith("/publish")) {
      return { ok: true, json: async () => ({ slug: "my-routine" }) }
    }
    if (url.endsWith("/pipelines/import")) {
      return { ok: true, json: async () => ({ imported: 1 }) }
    }
    return { ok: true, json: async () => [] }
  }),
}))

import { ImportRoutineDialog } from "../routines-layout"

function shell() {
  return document.querySelector('[data-slot="dialog-content"]')
}

describe("Import routine bundle on CreateSurface", () => {
  beforeEach(() => {
    cleanup()
    h.calls.length = 0
    vi.clearAllMocks()
  })

  it("mounts the shared shell at sm", () => {
    render(
      <ImportRoutineDialog workspaceId="ws-1" onClose={() => {}} onImported={() => {}} />,
    )
    expect(shell()!.className).toContain("sm:max-w-[480px]")
  })

  it("posts the parsed bundle to the same endpoint", async () => {
    const onImported = vi.fn()
    render(
      <ImportRoutineDialog
        workspaceId="ws-1"
        onClose={() => {}}
        onImported={onImported}
      />,
    )
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
    render(
      <ImportRoutineDialog workspaceId="ws-1" onClose={() => {}} onImported={() => {}} />,
    )
    fireEvent.change(screen.getByPlaceholderText(/"slug"/), {
      target: { value: "not json" },
    })
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
    render(
      <ImportRoutineDialog workspaceId="ws-1" onClose={() => {}} onImported={() => {}} />,
    )
    const body = shell()!.textContent ?? ""
    expect(body).not.toMatch(/existing routine is replaced/i)
    expect(body).toMatch(/refused/i)
    expect(body).toMatch(/nothing already saved is overwritten/i)
  })

  it("carries the breadcrumb every other door has", () => {
    render(
      <ImportRoutineDialog workspaceId="ws-1" onClose={() => {}} onImported={() => {}} />,
    )
    expect(screen.getByText("Routines")).toBeInTheDocument()
  })
})
