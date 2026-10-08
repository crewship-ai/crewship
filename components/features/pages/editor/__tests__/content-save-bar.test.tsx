/**
 * The Pages editor saves through the page's one floating Save bar, like
 * Settings and Admin (components/ui/page-save-bar). The Content card's name,
 * description and icon are a draft until the bar's Save; nothing is written
 * as it is typed, and leaving the editor with edits asks exactly once.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import * as React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  usePathname: () => "/pages/fleet-overview",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
}))
vi.mock("@/components/features/pages/editor/application-review", () => ({
  EditorApplicationReview: () => <div data-slot="application-review" />,
}))
vi.mock("@/components/features/pages/page-editor", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/components/features/pages/page-editor")>()),
  PageEditor: () => <div data-slot="page-document-editor" />,
}))
vi.mock("@/components/features/pages/editor/section-data-actions", () => ({
  EditorDataActionsSection: () => <div data-testid="section-data" />,
}))
vi.mock("@/components/features/pages/editor/section-access", () => ({
  EditorAccessSection: () => <div data-testid="section-access" />,
}))
vi.mock("@/components/features/pages/editor/section-history", () => ({
  EditorHistorySection: () => <div data-testid="section-history" />,
}))

import { PageEditorShell } from "@/components/features/pages/editor/page-editor-shell"
import { useEditorRoute, type EditorNavigation } from "@/components/features/pages/editor/use-editor-route"
import { derivePageCapabilities } from "@/components/features/pages/editor/use-page-capabilities"
import type { WirePageDetail } from "@/hooks/use-page-grants"

const PAGE = {
  id: "cpage1", slug: "fleet-overview", name: "Fleet overview", description: "Services and container memory",
  owner: "crew/lookout", has_application: false, has_project: false, panels: [],
  created_at: "2026-07-01T08:00:00Z", updated_at: "2026-08-10T08:00:00Z",
} as unknown as WirePageDetail

function json(status: number, body: unknown): Response {
  return { ok: status >= 200 && status < 300, status, headers: { get: () => null }, json: async () => body, text: async () => JSON.stringify(body) } as unknown as Response
}

let calls: Array<{ method: string; body: unknown }> = []
let nav: EditorNavigation | null = null

function Harness() {
  const n = useEditorRoute("fleet-overview")
  nav = n
  const opened = React.useRef(false)
  React.useEffect(() => {
    if (opened.current) return
    opened.current = true
    n.openEditor()
  }, [n])
  if (n.mode !== "edit") return null
  return (
    <PageEditorShell workspaceId="ws-1" slug="fleet-overview" page={PAGE} loading={false}
      capabilities={derivePageCapabilities(PAGE)} navigation={n} />
  )
}

function mount() {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><Harness /></QueryClientProvider>)
}
const bar = () => screen.queryByRole("region", { name: "Unsaved changes" })
const name = () => screen.getByLabelText("Name") as HTMLInputElement
const description = () => screen.getByLabelText("Description") as HTMLTextAreaElement

beforeEach(() => {
  calls = []
  window.history.replaceState(null, "", "/pages/fleet-overview")
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = (init?.method ?? "GET").toUpperCase()
    if (method !== "GET") calls.push({ method, body: init?.body ? JSON.parse(String(init.body)) : null })
    if (method === "PATCH") return json(200, { slug: "fleet-overview" })
    return json(404, { error: `unrouted ${method} ${String(input)}` })
  }))
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("the Pages editor saves through the page bar", () => {
  it("holds name and description as a draft: the bar counts them and nothing is written yet", () => {
    mount()
    fireEvent.change(name(), { target: { value: "Fleet" } })
    fireEvent.change(description(), { target: { value: "Services" } })
    expect(bar()).toHaveTextContent("2 unsaved changes")
    expect(calls).toEqual([])
    // The card's own Save gave way to the bar.
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull()
  })

  it("the bar's Save sends one PATCH with both fields", async () => {
    mount()
    fireEvent.change(name(), { target: { value: "Fleet" } })
    fireEvent.change(description(), { target: { value: "Services" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0]).toMatchObject({ method: "PATCH", body: { name: "Fleet", description: "Services" } })
  })

  it("the bar's Discard puts the saved values back", () => {
    mount()
    fireEvent.change(name(), { target: { value: "Fleet" } })
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(name().value).toBe("Fleet overview")
    expect(bar()).toBeNull()
  })

  it("leaving for another section with edits asks once, and Discard reverts them", () => {
    mount()
    fireEvent.change(name(), { target: { value: "Fleet" } })
    act(() => nav!.setSection("access"))
    expect(screen.getAllByRole("alertdialog")).toHaveLength(1)
    fireEvent.click(screen.getByRole("button", { name: "Discard changes" }))
    expect(screen.queryByRole("alertdialog")).toBeNull()
    expect(name().value).toBe("Fleet overview")
    expect(bar()).toBeNull()
  })
})
