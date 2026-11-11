// Library publication and consumer pins (#5189): owner-only Publish, and an
// explicit review before a consumer applies a newer publication.
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const api = vi.hoisted(() => ({ calls: [] as { url: string; method?: string; body?: unknown }[], replies: new Map<string, unknown>(), slug: "deck", workspace: "ws", principal: "owner" }))
vi.mock("@/components/features/chat/artifact-context/projects/client", () => ({
  artifactRequest: vi.fn(async (url: string, parse: (raw: unknown) => unknown, init: { method?: string; body?: unknown } = {}) => {
    api.calls.push({ url, method: init.method, body: init.body })
    const key = `${init.method ?? "GET"} ${url.split("?")[0]}`
    if (!api.replies.has(key)) throw Object.assign(new Error("not found"), { status: 404 })
    const reply = await api.replies.get(key)
    if (reply instanceof Error) throw reply
    return parse(reply)
  }),
}))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: api.principal } } }) }))
vi.mock("@/components/features/chat/artifact-context/projects/artifact-viewer-context", () => ({
  useArtifactViewer: () => ({ workspaceId: api.workspace, target: { slug: api.slug } }),
}))

import { LibraryPinNotice, LibraryPublishControl, parseUpdate, usePinnedLibrary } from "../library-publication"

const base = "/api/v1/pages/deck/artifact"
beforeEach(() => { api.calls.length = 0; api.replies.clear(); api.slug = "deck"; api.workspace = "ws"; api.principal = "owner" })
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

  it("does not carry the first project's publication authority into a different project", async () => {
    api.replies.set(`GET ${base}/library`, { published_version: 1, can_publish: true })
    api.replies.set("GET /api/v1/pages/other/artifact/library", { published_version: 7, can_publish: false })
    const view = render(<LibraryPublishControl shown={3} />)
    await screen.findByRole("button", { name: "Publish v3" })
    api.slug = "other"
    view.rerender(<LibraryPublishControl shown={3} />)
    expect(screen.queryByText("Published v1")).toBeNull()
    expect(screen.queryByRole("button", { name: "Publish v3" })).toBeNull()
    await screen.findByText("Published v7")
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
    api.replies.set(`GET ${base}/library-pin/update`, { library_page_id: "lib-1", pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: ["colors.accent"], removed: [], changed: ["colors.primary"] } })
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

  it("drops the previous project's pin review before loading a new workspace", async () => {
    api.replies.set(`GET ${base}/library-pin`, pin)
    api.replies.set(`GET ${base}/library-pin/update`, { library_page_id: "lib-1", pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: ["colors.private-review"], removed: [], changed: [] } })
    const view = render(<LibraryPinNotice />)
    fireEvent.click(await screen.findByRole("button", { name: "Review update" }))
    expect(screen.getByText("colors.private-review")).toBeTruthy()
    api.workspace = "other"
    api.replies.clear()
    view.rerender(<LibraryPinNotice />)
    expect(screen.queryByText("Brand")).toBeNull()
    expect(screen.queryByText("colors.private-review")).toBeNull()
    expect(screen.queryByRole("button", { name: "Apply v2" })).toBeNull()
  })

  it("never offers a review from another Library with the same pinned version", async () => {
    api.replies.set(`GET ${base}/library-pin`, pin)
    let release!: (value: unknown) => void
    api.replies.set(`GET ${base}/library-pin/update`, new Promise(resolve => { release = resolve }))
    render(<LibraryPinNotice />)
    await waitFor(() => expect(api.calls.some(call => call.url.includes("/update"))).toBe(true))
    // The pin changed to a different Library at v1 between the two GETs.
    release({ library_page_id: "lib-2", pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: ["from-the-other-library"], removed: [], changed: [] } })
    await screen.findByRole("status")
    expect(screen.queryByRole("button", { name: "Review update" })).toBeNull()
    expect(screen.queryByText("from-the-other-library")).toBeNull()
    expect(api.calls.some(call => call.method === "PUT")).toBe(false)
  })

  it("drops an open review on a same-page principal change", async () => {
    api.replies.set(`GET ${base}/library-pin`, pin)
    api.replies.set(`GET ${base}/library-pin/update`, { library_page_id: "lib-1", pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: ["private-to-first-person"], removed: [], changed: [] } })
    const view = render(<LibraryPinNotice />)
    fireEvent.click(await screen.findByRole("button", { name: "Review update" }))
    expect(screen.getByText("private-to-first-person")).toBeTruthy()
    api.principal = "different-user"
    api.replies.set(`GET ${base}/library-pin/update`, { library_page_id: "lib-1", pinned_version: 1, published_version: 0, library_available: false, update_available: false, changes: { added: [], removed: [], changed: [] } })
    view.rerender(<LibraryPinNotice />)
    expect(screen.queryByText("private-to-first-person")).toBeNull()
    expect(screen.queryByRole("button", { name: "Apply v2" })).toBeNull()
    await screen.findByText(/keeps its pinned copy/)
  })

  it("lists the fonts an update adds and the ones it leaves behind for their licence", async () => {
    api.replies.set(`GET ${base}/library-pin`, pin)
    api.replies.set(`GET ${base}/library-pin/update`, { library_page_id: "lib-1", pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: [], removed: [], changed: [] }, file_changes: { added: ["fonts/Inter.woff2", "fonts/OFL.txt"], removed: [], changed: [] }, not_copied: ["fonts/Secret.woff2:licence", "fonts/Huge.ttf:too_large"] })
    render(<LibraryPinNotice />)
    fireEvent.click(await screen.findByRole("button", { name: "Review update" }))
    expect(screen.getByText("fonts/Inter.woff2")).toBeTruthy()
    expect(screen.getByText("fonts/Secret.woff2 (no redistributable licence)")).toBeTruthy()
    expect(screen.getByText("fonts/Huge.ttf (too large)")).toBeTruthy()
  })

  it("shows the fonts the current pin left behind, without an update to review", async () => {
    api.replies.set(`GET ${base}/library-pin`, { ...pin, files: [
      { path: "fonts/Inter.woff2", media_type: "font/woff2", status: "copied", size: 300, sha256: "a".repeat(64) },
      { path: "fonts/Secret.woff2", media_type: "font/woff2", status: "licence", size: 0, sha256: "" },
      { path: "fonts/Huge.ttf", media_type: "font/ttf", status: "too_large", size: 0, sha256: "" },
    ] })
    api.replies.set(`GET ${base}/library-pin/update`, { library_page_id: "lib-1", pinned_version: 1, published_version: 1, library_available: true, update_available: false, changes: { added: [], removed: [], changed: [] } })
    render(<LibraryPinNotice />)
    expect(await screen.findByText("fonts/Secret.woff2 (no redistributable licence)")).toBeTruthy()
    expect(screen.getByText("fonts/Huge.ttf (too large)")).toBeTruthy()
    expect(screen.getByText(/Not carried by this pin/)).toBeTruthy()
    expect(screen.queryByText(/fonts\/Inter\.woff2/)).toBeNull()
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("offers the dependency copy for a pin made before fonts travelled, at the same version", async () => {
    api.replies.set(`GET ${base}/library-pin`, pin)
    api.replies.set(`GET ${base}/library-pin/update`, { library_page_id: "lib-1", pinned_version: 1, published_version: 1, library_available: true, update_available: true, dependencies_missing: true, changes: { added: [], removed: [], changed: [] }, file_changes: { added: ["fonts/Inter.woff2", "fonts/OFL.txt"], removed: [], changed: [] }, not_copied: [] })
    api.replies.set(`PUT ${base}/library-pin`, pin)
    render(<LibraryPinNotice />)
    expect(await screen.findByText("Fonts not copied yet")).toBeTruthy()
    expect(screen.queryByText(/Update available/)).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Review update" }))
    expect(screen.getByText("fonts/Inter.woff2")).toBeTruthy()
    expect(screen.getByText(/made before fonts travelled/)).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Copy the fonts of v1" }))
    await waitFor(() => expect(api.calls.find(call => call.method === "PUT")?.body).toEqual({ library: "@lib-1", version: 1, if_pinned_library: "@lib-1", if_pinned_version: 1 }))
  })

  it("keeps the pinned copy when the library is no longer readable", async () => {
    api.replies.set(`GET ${base}/library-pin`, pin)
    api.replies.set(`GET ${base}/library-pin/update`, { library_page_id: "lib-1", pinned_version: 1, published_version: 0, library_available: false, update_available: false, changes: { added: [], removed: [], changed: [] } })
    render(<LibraryPinNotice />)
    await screen.findByText(/keeps its pinned copy/)
    expect(screen.getByText("Brand")).toBeTruthy()
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("shows a failed apply without losing the review", async () => {
    api.replies.set(`GET ${base}/library-pin`, pin)
    api.replies.set(`GET ${base}/library-pin/update`, { library_page_id: "lib-1", pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: [], removed: ["radii.sm"], changed: [] } })
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
    expect(parseUpdate({ library_page_id: "lib-1", pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: [], removed: [], changed: [] }, not_copied: [3] })).toBeNull()
    expect(parseUpdate({ library_page_id: "lib-1", pinned_version: 1, published_version: 2, library_available: true, update_available: true })).toBeNull()
    expect(parseUpdate({ library_page_id: "lib-1", pinned_version: 1, published_version: 2, library_available: true, update_available: true, changes: { added: [1], removed: [], changed: [] } })).toBeNull()
  })
})

describe("usePinnedLibrary", () => {
  function Probe({ enabled = true }: { enabled?: boolean }) {
    const pinned = usePinnedLibrary("ws", "deck", enabled)
    return <pre data-testid="css">{pinned ? pinned.css ?? "unreadable" : "none"}</pre>
  }
  const tokens = { schema_version: 1, colors: { primary: "#1d4ed8" }, typography: {}, spacing: {}, radii: {} }
  const font = { path: "fonts/Inter.woff2", media_type: "font/woff2", status: "copied", size: 8, sha256: "a".repeat(64), family: "Inter", weight: 400, style: "normal", content: "d09GMgAA" }

  it("derives the preview theme from the project's own pinned copy and refreshes after an applied update", async () => {
    const snapshot = { library_page_id: "lib-1", library_version: 1, sha256: "b".repeat(64), tokens, files: [font] }
    api.replies.set(`GET ${base}/library-pin/files`, snapshot)
    render(<Probe />)
    await waitFor(() => expect(screen.getByTestId("css").textContent).toContain("--ds-color-primary:#1d4ed8;"))
    expect(screen.getByTestId("css").textContent).toContain('font-family:"Inter";src:url(data:font/woff2;base64,d09GMgAA)')
    expect(api.calls.every(call => !call.url.includes("/library?") && !call.url.includes("/library/"))).toBe(true)
    expect(api.calls).toHaveLength(1)
    api.replies.set(`GET ${base}/library-pin/files`, { ...snapshot, library_version: 2, tokens: { ...tokens, colors: { primary: "#0f766e" } } })
    window.dispatchEvent(new CustomEvent("crewship:library-pin-changed", { detail: JSON.stringify(["ws", "deck"]) }))
    await waitFor(() => expect(screen.getByTestId("css").textContent).toContain("--ds-color-primary:#0f766e;"))
  })

  it("discards the previous signed-in person's preview immediately", async () => {
    api.replies.set(`GET ${base}/library-pin/files`, { library_page_id: "lib-1", library_version: 1, sha256: "b".repeat(64), tokens, files: [font] })
    const view = render(<Probe />)
    await waitFor(() => expect(screen.getByTestId("css").textContent).toContain("--ds-color-primary"))
    api.principal = "different-user"
    api.replies.clear()
    view.rerender(<Probe />)
    expect(screen.getByTestId("css").textContent).toBe("none")
  })

  it("asks for nothing when the project is itself a Library or the flag is off", async () => {
    render(<Probe enabled={false} />)
    expect(screen.getByTestId("css").textContent).toBe("none")
    expect(api.calls).toHaveLength(0)
  })
})
