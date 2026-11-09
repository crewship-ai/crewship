// Library publication and consumer pins (#5189): owner-only Publish, and an
// explicit review before a consumer applies a newer publication.
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const api = vi.hoisted(() => ({ calls: [] as { url: string; method?: string; body?: unknown }[], replies: new Map<string, unknown>() }))
vi.mock("@/components/features/chat/artifact-context/projects/client", () => ({
  artifactRequest: vi.fn(async (url: string, parse: (raw: unknown) => unknown, init: { method?: string; body?: unknown } = {}) => {
    api.calls.push({ url, method: init.method, body: init.body })
    const key = `${init.method ?? "GET"} ${url.split("?")[0]}`
    if (!api.replies.has(key)) throw Object.assign(new Error("not found"), { status: 404 })
    const reply = api.replies.get(key)
    if (reply instanceof Error) throw reply
    return parse(reply)
  }),
}))
vi.mock("@/components/features/chat/artifact-context/projects/artifact-viewer-context", () => ({
  useArtifactViewer: () => ({ workspaceId: "ws", target: { slug: "deck" } }),
}))

import { LibraryPinNotice, LibraryPublishControl, parseUpdate } from "../library-publication"

const base = "/api/v1/pages/deck/artifact"
beforeEach(() => { api.calls.length = 0; api.replies.clear() })
afterEach(() => cleanup())

describe("LibraryPublishControl", () => {
  it("offers Publish only to the owner, for the shown version", async () => {
    api.replies.set(`GET ${base}/library`, { published_version: 1, can_publish: true })
    api.replies.set(`POST ${base}/library/publish`, { published_version: 3, can_publish: true })
    render(<LibraryPublishControl shown={3} />)
    fireEvent.click(await screen.findByRole("button", { name: "Publish v3" }))
    await screen.findByText("Published v3")
    expect(api.calls.at(-1)).toMatchObject({ method: "POST", body: { version: 3 } })
    expect(screen.queryByRole("button", { name: /Publish/ })).toBeNull()
  })

  it("shows the publication without a Publish action to editors and readers", async () => {
    api.replies.set(`GET ${base}/library`, { published_version: 2, can_publish: false })
    render(<LibraryPublishControl shown={3} />)
    await screen.findByText("Published v2")
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("says Not published before the first publication", async () => {
    api.replies.set(`GET ${base}/library`, { published_version: 0, can_publish: false })
    render(<LibraryPublishControl shown={1} />)
    await screen.findByText("Not published")
  })
})

describe("LibraryPinNotice", () => {
  const pin = { library_page_id: "lib-1", library_version: 1, library_name: "Brand", copied_at: "2026-11-09T00:00:00.000Z" }

  it("renders nothing for a project without a pin", async () => {
    const { container } = render(<LibraryPinNotice />)
    await waitFor(() => expect(api.calls.length).toBe(1))
    expect(container.innerHTML).toBe("")
  })

  it("reviews the token diff and applies the update with the reviewed version", async () => {
    api.replies.set(`GET ${base}/library-pin`, pin)
    api.replies.set(`GET ${base}/library-pin/update`, { pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: ["colors.accent"], removed: [], changed: ["colors.primary"] } })
    api.replies.set(`PUT ${base}/library-pin`, { ...pin, library_version: 2 })
    render(<LibraryPinNotice />)
    await screen.findByText("Update available: v2")
    expect(screen.queryByText("colors.primary")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Review update" }))
    expect(screen.getByText("colors.accent")).toBeTruthy()
    expect(screen.getByText("colors.primary")).toBeTruthy()
    expect(api.calls.some(call => call.method === "PUT")).toBe(false)
    fireEvent.click(screen.getByRole("button", { name: "Apply v2" }))
    await waitFor(() => expect(api.calls.some(call => call.method === "PUT")).toBe(true))
    expect(api.calls.find(call => call.method === "PUT")?.body).toEqual({ library: "@lib-1", version: 2, if_pinned_library: "@lib-1", if_pinned_version: 1 })
  })

  it("keeps the pinned copy when the library is no longer readable", async () => {
    api.replies.set(`GET ${base}/library-pin`, pin)
    api.replies.set(`GET ${base}/library-pin/update`, { pinned_version: 1, published_version: 0, library_available: false, update_available: false, changes: { added: [], removed: [], changed: [] } })
    render(<LibraryPinNotice />)
    await screen.findByText(/keeps its pinned copy/)
    expect(screen.getByText("Brand")).toBeTruthy()
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("shows a failed apply without losing the review", async () => {
    api.replies.set(`GET ${base}/library-pin`, pin)
    api.replies.set(`GET ${base}/library-pin/update`, { pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: [], removed: ["radii.sm"], changed: [] } })
    api.replies.set(`PUT ${base}/library-pin`, new Error("This project changed."))
    render(<LibraryPinNotice />)
    fireEvent.click(await screen.findByRole("button", { name: "Review update" }))
    fireEvent.click(screen.getByRole("button", { name: "Apply v2" }))
    expect((await screen.findByRole("alert")).textContent).toBe("This project changed.")
    expect(screen.getByText("radii.sm")).toBeTruthy()
  })
})

describe("parseUpdate", () => {
  it("rejects answers without change lists", () => {
    expect(parseUpdate({ pinned_version: 1, published_version: 2, library_available: true, update_available: true })).toBeNull()
    expect(parseUpdate({ pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: [1], removed: [], changed: [] } })).toBeNull()
  })
})
