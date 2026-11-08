// The mirrored "Template Crewship Docs" (cmd/crewship/seeddata/showcase/mirror/qakf) as the seed
// initializes it: the body passes the real schema 3 validator and opens in the Docs page.
vi.mock("@/lib/protected-document-drafts", () => ({ openProtectedDocumentDrafts: async (...args: Parameters<typeof import("@/lib/document-queue").openDocumentDrafts>) => (await import("@/lib/document-queue")).openDocumentDrafts(...args) }))
vi.mock("@/lib/local-draft-seal", () => ({
  createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
  isSealedLocalDraft: () => false, LocalDraftProtectionError: class extends Error {}, LocalDraftReadRevoked: class extends Error {},
}))
import * as React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { encodeUpdate } from "@/hooks/use-page-document"
import { documentSchema } from "@/lib/documents/schema"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../../frame"
import type { LibraryItem } from "../../../library-items"
import { DocsBody } from "../docs-view"
import mirrored from "../../../../../../cmd/crewship/seeddata/showcase/mirror/qakf/document.json"

const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({ data: true, isPending: false }) }))
vi.mock("@/lib/document-queue", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/document-queue")>(), openDocumentDrafts: async () => ({ load: async () => [], put: async () => {}, remove: async () => {}, close: () => {}, exclusive: true }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "docs-human" } } }) }))

afterEach(() => { cleanup(); request.mockReset() })

const item = {
  slug: "ref-template-crewship-docs", pageId: "page-mirror", name: "Template Crewship Docs", kind: "word", family: "document", sharedVersion: null, native: false,
  projectBacked: false, empty: false, private: false, ownerLabel: "You", folder: null, updatedAt: null,
} as unknown as LibraryItem

/** The engine's own path: validate the JSON, build the Yjs document, project it back. */
function initialized() {
  const doc = documentSchema.create(structuredClone(mirrored), 3) as Y.Doc
  const snapshot = encodeUpdate(Y.encodeStateAsUpdate(doc))
  const projection = documentSchema.validate(doc, 3)
  doc.destroy()
  return { snapshot, projection }
}

function open(layout: FrameLayout) {
  const { snapshot } = initialized()
  request.mockImplementation(async (url: string) => {
    if (url.includes("/document/checkpoints")) return { ok: true, json: async () => ({ checkpoints: [] }) }
    if (url.includes("/proposals")) return { ok: true, json: async () => ({ proposals: [] }) }
    return { ok: true, json: async () => ({ page_id: "page-mirror", schema: 3, rev: 1, epoch: "go:node:1", can_write: false, snapshot }) }
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(<QueryClientProvider client={client}>
    <ArtifactFrame name={item.name} kind={{ kind: "docs", mode: "page" }} folder={null} layout={layout} onHome={() => {}} caps={{ ...NO_FRAME_CAPABILITIES, agent: { ask: { chatHref: () => "/chat" } } }}>
      <DocsBody workspaceId="ws1" item={item} onFrame={() => {}} fallback={<p>saved version</p>} />
    </ArtifactFrame>
  </QueryClientProvider>)
}

describe("mirrored Template Crewship Docs", () => {
  it("is a body the schema 3 engine accepts unchanged", () => {
    const { projection } = initialized()
    const text = (node: { text?: string; content?: unknown[] }): string => (node.text ?? "") + (node.content ?? []).map(child => text(child as never)).join("")
    // Nothing normalized away: same block count and the same words.
    expect(projection.content).toHaveLength(mirrored.content.length)
    expect(text(projection)).toBe(text(mirrored as never))
  })

  it("opens as a Docs page: headings in the outline, tables, code blocks and links", async () => {
    open("desktop")
    const editor = await screen.findByLabelText("Document editor")
    expect(await within(editor).findByText("Co je Crewship")).toBeTruthy()
    const outline = within(document.querySelector('[data-artifact-slot="panel"]') as HTMLElement).getByRole("navigation", { name: "Headings" })
    expect(within(outline).getAllByRole("button").map(button => button.textContent)).toEqual([
      "Template Crewship Docs", "Co je Crewship", "Ukázka funkcí dokumentu", "Diagram a graf", "Formátování textu", "Nadpis třetí úrovně",
      "Seznamy", "Bloky kódu", "CSV a Markdown ukázky", "Tabulka s barvami a odkazy", "Záložky a odkazy", "Otevřené otázky", "Pracovní poznámky",
    ])
    expect(editor.querySelectorAll("table")).toHaveLength(4)
    expect(editor.querySelectorAll("th")).toHaveLength(3 + 4 + 6 + 3)
    const code = [...editor.querySelectorAll("pre")].map(pre => pre.textContent)
    expect(code).toHaveLength(10)
    expect(code[2]).toContain("\tfmt.Printf(\"%.0f%%\\n\", Task{\"Support\", 42, 3}.SuccessRate()*100)")
    expect([...editor.querySelectorAll("a")].map(a => a.getAttribute("href"))).toEqual([
      "https://claude.ai/", "https://claude.ai/artifact/CBBoNZhbaHvUUUz2aQ2Ccv", "https://claude.ai/artifact/RwrX5Qdjw7zLHdvxjEABKL",
      "https://claude.ai/artifact/NwPV8rqSjHWDtpZPYUb2ko", "https://claude.ai/artifact/E8HYX1pSVKSAbLqgvTCZ8Z",
    ])
    expect([...editor.querySelectorAll(":not(pre) > code")].map(c => c.textContent)).toEqual(["inline kód", "crew ls"])
  })

  it("keeps long code lines inside the reading column on a phone", async () => {
    open("phone")
    const editor = await screen.findByLabelText("Document editor")
    await within(editor).findByText("Co je Crewship")
    // A grid track sized to min-content grew past a 390 px screen on an unbreakable code line, clipping
    // every paragraph; the track is capped and code may break anywhere (checked in a browser too).
    expect(editor.closest("article")!.className).toContain("grid-cols-[minmax(0,1fr)]")
    expect(editor.className).toContain("[&_pre]:[overflow-wrap:anywhere]")
  })
})
