import { useState } from "react"
import { toast } from "sonner"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { RoutineVersionsTab } from "../routine-versions-tab"

const { fetcher } = vi.hoisted(() => ({ fetcher: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher, broadcastSessionExpired: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "usr_me" } }, status: "authenticated" }) }))
vi.mock("@/hooks/use-issue-detail", () => ({ useUrlSelection: () => useState<string | null>(null) }))
vi.mock("../routine-definition-canvas", () => ({ RoutineDefinitionCanvas: () => <div>Archived graph</div> }))
vi.mock("../routine-step-definition", () => ({ RoutineStepDefinition: () => null }))

const definition = { steps: [{ id: "old-step", type: "transform" }], display_name: "Recipe" }
const json = (body: unknown) => ({ ok: true, status: 200, json: async () => body })

beforeEach(() => {
  fetcher.mockReset()
  window.confirm = vi.fn(() => true)
})

describe("historical recipe inspection", () => {
  it("compares archives without writing to the server and marks the published one", async () => {
    fetcher.mockImplementation(async (url: string) =>
      json(url.endsWith("/versions") ? [{ version: 2, is_head: true }, { version: 1 }] : url.includes("/diff?") ? { from_version: 1, to_version: 2, identical: false, unified_diff: "-old-step\n+new-step" } : { version: 1, definition }),
    )
    render(<RoutineVersionsTab workspaceId="ws" slug="recipe" />)
    expect(await screen.findByText("Published · Run uses this")).toBeInTheDocument()
    expect(screen.getByTestId("routine-version-2")).toHaveTextContent("Published · Run uses this")
    expect(screen.getByTestId("routine-version-1")).not.toHaveTextContent("Published")
    fireEvent.click(screen.getByRole("button", { name: "Compare with published" }))
    await screen.findByText("Changes from version 1 to version 2")
    await waitFor(() => expect(fetcher.mock.calls.every(([, options]) => !options?.method || options.method === "GET")).toBe(true))
    expect(screen.queryByRole("button", { name: /rollback/i })).toBeNull()
  })

  it("restores an older version as a draft through the drafts API", async () => {
    const changed = vi.fn()
    fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith("/versions")) return json([{ version: 2, is_head: true }, { version: 1 }])
      if (url.endsWith("/versions/1")) return json({ version: 1, definition })
      if (url.endsWith("/recipe/draft")) return json({ id: "", slug: "recipe", revision: 0, base_pipeline_id: "pipe-1", base_revision: 2, document: {} })
      if (url.endsWith("/pipelines/drafts") && init?.method === "POST") return json({ id: "drf_9", slug: "recipe", revision: 1, base_pipeline_id: "pipe-1", base_revision: 2, document: JSON.parse(String(init.body)).document })
      throw new Error(`unexpected ${url}`)
    })
    render(<RoutineVersionsTab workspaceId="ws" slug="recipe" routine={{ name: "Recipe", description: "Does things", author_crew_id: "crew_1" }} onChanged={changed} />)
    fireEvent.click(await screen.findByRole("button", { name: "Restore as draft" }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
    const save = fetcher.mock.calls.find(([, init]) => init?.method === "POST")!
    expect(save[0]).toBe("/api/v1/workspaces/ws/pipelines/drafts")
    const body = JSON.parse(String(save[1].body))
    expect(body).toMatchObject({ id: "", slug: "recipe", revision: 0, base_pipeline_id: "pipe-1", base_revision: 2 })
    expect(body.document).toEqual({ slug: "recipe", name: "Recipe", description: "Does things", definition, author_crew_id: "crew_1" })
    // No rollback call: the live version is untouched.
    expect(fetcher.mock.calls.some(([url]) => String(url).includes("rollback"))).toBe(false)
  })

  it("draws the draft on top with Review and publish and Discard", async () => {
    fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith("/versions")) return json([{ version: 2, is_head: true }])
      if (init?.method === "DELETE") return json({})
      throw new Error(`unexpected ${url}`)
    })
    const publish = vi.fn()
    const changed = vi.fn()
    render(<RoutineVersionsTab workspaceId="ws" slug="recipe" draft={{ id: "drf_1", revision: 3, updated_at: new Date().toISOString(), updated_by: "usr_me" }} onPublish={publish} onChanged={changed} />)
    const row = screen.getByTestId("routine-draft-row")
    expect(row).toHaveTextContent("r3 · saved just now by you")
    fireEvent.click(screen.getByRole("button", { name: "Review and publish" }))
    expect(publish).toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    await waitFor(() => expect(changed).toHaveBeenCalled())
    const del = fetcher.mock.calls.find(([, init]) => init?.method === "DELETE")!
    expect(del[0]).toBe("/api/v1/workspaces/ws/pipelines/recipe/draft")
    expect(JSON.parse(String(del[1].body))).toEqual({ id: "drf_1", revision: 3 })
  })
})

describe("restore draft concurrency", () => {
  it.each([
    { name: "a newer revision", seen: { id: "draft-old", revision: 3 }, loaded: { id: "draft-old", revision: 5 } },
    { name: "a draft created since rendering", seen: undefined, loaded: { id: "draft-new", revision: 1 } },
    { name: "a replacement draft with the same revision", seen: { id: "draft-old", revision: 3 }, loaded: { id: "draft-new", revision: 3 } },
  ])("refuses to overwrite $name that the operator did not confirm", async ({ seen, loaded }) => {
    vi.mocked(toast.error).mockClear()
    const changed = vi.fn()
    fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith("/versions")) return json([{ version: 2, is_head: true }, { version: 1 }])
      if (url.endsWith("/versions/1")) return json({ version: 1, definition })
      if (url.endsWith("/recipe/draft")) return json({ ...loaded, slug: "recipe", base_pipeline_id: "pipe-1", base_revision: 2, document: { definition: { steps: [] } } })
      if (init?.method === "POST") return json({ ...loaded, revision: loaded.revision + 1, document: {} })
      throw new Error(`unexpected ${url}`)
    })
    render(<RoutineVersionsTab workspaceId="ws" slug="recipe" draft={seen ? { ...seen, updated_at: "2026-09-16T00:00:00Z", updated_by: "someone" } : undefined} onChanged={changed} />)
    fireEvent.click(await screen.findByRole("button", { name: "Restore as draft" }))
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(fetcher.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
    expect(changed).not.toHaveBeenCalled()
  })

  it.each([200, 409])("saves the confirmed revision with server CAS (HTTP %i)", async (status) => {
    vi.mocked(toast.error).mockClear()
    const changed = vi.fn()
    fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith("/versions")) return json([{ version: 2, is_head: true }, { version: 1 }])
      if (url.endsWith("/versions/1")) return json({ version: 1, definition })
      if (url.endsWith("/recipe/draft")) return json({ id: "draft-old", slug: "recipe", revision: 3, base_pipeline_id: "pipe-1", base_revision: 2, document: {} })
      if (init?.method === "POST") return status === 409
        ? { ok: false, status, json: async () => ({ error: "Draft changed during save" }) }
        : json({ id: "draft-old", revision: 4, document: {} })
      throw new Error(`unexpected ${url}`)
    })
    render(<RoutineVersionsTab workspaceId="ws" slug="recipe" draft={{ id: "draft-old", revision: 3, updated_at: "2026-09-16T00:00:00Z" }} onChanged={changed} />)
    fireEvent.click(await screen.findByRole("button", { name: "Restore as draft" }))
    await waitFor(() => expect(status === 409 ? toast.error : changed).toHaveBeenCalled())
    const writes = fetcher.mock.calls.filter(([, init]) => init?.method === "POST")
    expect(writes).toHaveLength(1)
    expect(JSON.parse(String(writes[0][1].body))).toMatchObject({ id: "draft-old", revision: 3, base_pipeline_id: "pipe-1", base_revision: 2, document: { definition } })
    if (status === 409) expect(changed).not.toHaveBeenCalled()
  })
})
