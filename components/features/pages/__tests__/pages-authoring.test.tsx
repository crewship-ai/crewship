/**
 * The authoring affordances on the /pages shell — PRD §10b.1.
 *
 * "Authoring is therefore: the CLI, the in-app editor, or an agent — three
 * doors onto one document." The surface shipped with two of the three: a page
 * could only be created through the API or the CLI, which is the first thing
 * anyone notices about it. These tests pin the doors, not the editor behind
 * them (that is `page-editor.test.tsx`).
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

const push = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  usePathname: () => "/pages",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}))
// CodeMirror is not what is under test here, and mounting it in happy-dom is
// slow enough to be worth avoiding on a test about two buttons.
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: ({ code }: { code: string }) => <div data-testid="editor">{code}</div>,
}))

// The four editor sections are five other files' worth of behaviour and are
// tested there. This suite is about the shell around them: which door opens,
// what the address says, and that the rail never blinks.
vi.mock("@/components/features/pages/editor/section-content", () => ({
  EditorContentSection: () => <div data-testid="section-content">Content</div>,
}))
vi.mock("@/components/features/pages/editor/section-data-actions", () => ({
  EditorDataActionsSection: () => <div data-testid="section-data">Data & actions</div>,
}))
vi.mock("@/components/features/pages/editor/section-access", () => ({
  EditorAccessSection: () => <div data-testid="section-access">Access</div>,
}))
vi.mock("@/components/features/pages/editor/section-history", () => ({
  EditorHistorySection: () => <div data-testid="section-history">History</div>,
}))

import { PagesLayout } from "@/components/features/pages/pages-layout"
import type { WirePage } from "@/hooks/use-pages"

const NOW = new Date("2026-08-12T12:00:00Z")

const FLEET: WirePage = {
  id: "cpage1",
  slug: "fleet-201",
  name: "Flotila .201",
  owner: "crew/lookout",
  panels: [
    {
      id: "sluzby",
      schema: "status.v1",
      owner: "crew/lookout",
      producer: "script/watch-services.sh",
      sla_seconds: 300,
      span: 8,
      state: "fresh",
      data: { items: [] },
    },
  ],
}

const SEALED: WirePage = {
  id: "cpage2",
  slug: "mixed",
  name: "Mixed crews",
  owner: "crew/lookout",
  panels: [
    {
      id: "sluzby",
      schema: "status.v1",
      owner: "crew/lookout",
      producer: "script/watch.sh",
      sla_seconds: 300,
      span: 8,
      state: "fresh",
      data: { items: [] },
    },
    { panel_id: "finance", span: 4, sealed: true, owner_crew_name: "Finance" },
  ],
}

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    headers: new Headers(),
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

function renderLayout(list: WirePage[], slug?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const mockFetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes("/api/v1/pages/")) {
      const wanted = decodeURIComponent(url.split("/api/v1/pages/")[1].split("?")[0])
      return okJSON(list.find((p) => p.slug === wanted) ?? null)
    }
    return okJSON(list)
  })
  vi.stubGlobal("fetch", mockFetch)
  render(
    <QueryClientProvider client={qc}>
      <PagesLayout workspaceId="ws-1" slug={slug} now={NOW} />
    </QueryClientProvider>,
  )
}

describe("the /pages shell offers the third door", () => {
  beforeEach(() => {
    cleanup()
    push.mockReset()
    // The editor's mode and section live in the address now, so a test that
    // left `?mode=edit` there would hand it to the next one. (resets the
    // address between tests.)
    window.history.replaceState(null, "", "/pages")
  })
  afterEach(() => vi.unstubAllGlobals())

  it("opens the YAML editor from New page, seeded with a Page document", async () => {
    renderLayout([FLEET])
    // The header's button. The rail offers a second one in its empty state.
    fireEvent.click(screen.getAllByRole("button", { name: /new page/i })[0])
    const editor = await screen.findByRole("dialog", { name: /new page/i })
    // The buffer is the manifest envelope — the same document the CLI takes.
    expect(editor.textContent).toContain("kind: Page")
    expect(editor.textContent).toContain("apiVersion: crewship/v1")
  })

  it("seeds the template's panel owner from a crew the workspace already uses", async () => {
    renderLayout([FLEET])
    await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())
    fireEvent.click(screen.getAllByRole("button", { name: /new page/i })[0])
    expect((await screen.findByTestId("editor")).textContent).toContain("owner: crew/lookout")
  })

  it("offers Edit only on a page, and opens the editor on that page", async () => {
    renderLayout([FLEET])
    expect(screen.queryByRole("button", { name: /^edit$/i })).toBeNull()

    cleanup()
    renderLayout([FLEET], "fleet-201")
    await waitFor(() => expect(screen.getByRole("button", { name: /^edit$/i })).toBeEnabled())
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }))

    // Edit is one door into a routed editor now, not five buttons into five
    // dialogs. The address carries it, so a reload lands back here.
    expect(await screen.findByTestId("section-content")).toBeTruthy()
    expect(window.location.search).toBe("?mode=edit")
    expect(screen.getByRole("button", { name: /back to page/i })).toBeTruthy()
    // The list is still beside the editor — replacing it with the sections
    // was the review's U05, and the scroll-and-filters promise is the one
    // that breaks. The name appears twice on purpose: once in the rail, once
    // in the editor's own header, which is what keeps "which Page am I
    // editing" on screen at every width.
    expect(screen.getAllByText("Flotila .201").length).toBeGreaterThanOrEqual(2)
  })

  it("Share opens the same editor already on Access", async () => {
    renderLayout([FLEET], "fleet-201")
    await waitFor(() => expect(screen.getByRole("button", { name: /^share$/i })).toBeEnabled())
    fireEvent.click(screen.getByRole("button", { name: /^share$/i }))
    expect(await screen.findByTestId("section-access")).toBeTruthy()
    expect(window.location.search).toContain("section=access")
  })

  it("still opens the editor on a page carrying a panel the viewer may not see", async () => {
    // The regression this pins: the old gate was one boolean over the whole
    // surface, so a single sealed panel hid Edit, App preview, Source history
    // and Publications at once — and the server would have answered three of
    // them. What a sealed panel makes unsafe is replacing the DOCUMENT
    // (§11b.14), which is now the only thing it closes; the refusal is
    // rendered in Content, beside the control it applies to.
    renderLayout([SEALED], "mixed")
    await waitFor(() => expect(screen.getByRole("button", { name: /^edit$/i })).toBeEnabled())
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }))
    expect(await screen.findByTestId("section-content")).toBeTruthy()
    // Access is reachable on exactly the Page the old gate locked out of it.
    // The rail button carries its one-line summary in its name too.
    fireEvent.click(screen.getByRole("button", { name: /^Access/ }))
    expect(await screen.findByTestId("section-access")).toBeTruthy()
  })

  it("names the New page button in the empty rail, not only the CLI", async () => {
    renderLayout([])
    await waitFor(() => expect(screen.getByText("No pages yet.")).toBeTruthy())
    // Two buttons named "New page" once the rail offers one — the header's and
    // the rail's. Either opens the same editor.
    fireEvent.click(screen.getAllByRole("button", { name: /new page/i })[1])
    expect((await screen.findByTestId("editor")).textContent).toContain("kind: Page")
  })
})
