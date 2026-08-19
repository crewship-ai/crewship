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

  it("offers Edit only on a page, and opens it on that page's document", async () => {
    renderLayout([FLEET])
    expect(screen.queryByRole("button", { name: /^edit$/i })).toBeNull()

    cleanup()
    renderLayout([FLEET], "fleet-201")
    await waitFor(() => expect(screen.getByRole("button", { name: /^edit$/i })).toBeEnabled())
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }))
    const buffer = (await screen.findByTestId("editor")).textContent ?? ""
    expect(buffer).toContain("slug: fleet-201")
    // Rendered back into the sugar a human wrote, not the integer stored.
    expect(buffer).toContain("sla: 5m")
  })

  it("will not open Edit on a page carrying a panel the viewer may not see", async () => {
    // §11b.14: the document cannot describe a sealed placeholder, so a save
    // built from it would delete another crew's panel.
    renderLayout([SEALED], "mixed")
    await waitFor(() => expect(screen.getByRole("button", { name: /^edit$/i })).toBeDisabled())
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
