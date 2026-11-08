// The mirrored "Template Crewship Docs" (cmd/crewship/seeddata/showcase/mirror/qakf) as the seed
// initializes it: the body passes the real schema 4 validator and opens in the Docs page.
vi.mock("@/lib/protected-document-drafts", () => ({ openProtectedDocumentDrafts: async (...args: Parameters<typeof import("@/lib/document-queue").openDocumentDrafts>) => (await import("@/lib/document-queue")).openDocumentDrafts(...args) }))
vi.mock("@/lib/local-draft-seal", () => ({
  createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
  isSealedLocalDraft: () => false, LocalDraftProtectionError: class extends Error {}, LocalDraftReadRevoked: class extends Error {},
}))
import * as React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, render, screen, waitFor, within } from "@testing-library/react"
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
  const doc = documentSchema.create(structuredClone(mirrored), 4) as Y.Doc
  const snapshot = encodeUpdate(Y.encodeStateAsUpdate(doc))
  const projection = documentSchema.validate(doc, 4)
  doc.destroy()
  return { snapshot, projection }
}

function open(layout: FrameLayout) {
  const { snapshot } = initialized()
  request.mockImplementation(async (url: string) => {
    if (url.includes("/document/checkpoints")) return { ok: true, json: async () => ({ checkpoints: [] }) }
    if (url.includes("/proposals")) return { ok: true, json: async () => ({ proposals: [] }) }
    return { ok: true, json: async () => ({ page_id: "page-mirror", schema: 4, rev: 1, epoch: "go:node:1", can_write: false, snapshot }) }
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(<QueryClientProvider client={client}>
    <ArtifactFrame name={item.name} kind={{ kind: "docs", mode: "page" }} folder={null} layout={layout} onHome={() => {}} caps={{ ...NO_FRAME_CAPABILITIES, agent: { ask: { chatHref: () => "/chat" } } }}>
      <DocsBody workspaceId="ws1" item={item} onFrame={() => {}} fallback={<p>saved version</p>} />
    </ArtifactFrame>
  </QueryClientProvider>)
}

describe("mirrored Template Crewship Docs", () => {
  it("is a body the schema 4 engine accepts unchanged, and schema 3 refuses", () => {
    const { projection } = initialized()
    const text = (node: { text?: string; content?: unknown[] }): string => (node.text ?? "") + (node.content ?? []).map(child => text(child as never)).join("")
    // Nothing normalized away: same block count and the same words.
    expect(projection.content).toHaveLength(mirrored.content.length)
    expect(text(projection)).toBe(text(mirrored as never))
    expect(() => documentSchema.create(structuredClone(mirrored), 3)).toThrow()
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
    // Document content only: rendered CSV/Markdown previews are view chrome outside the text.
    const own = <T extends Element>(selector: string) => [...editor.querySelectorAll<T>(selector)].filter(node => !node.closest("[data-code-preview]"))
    expect(own("table")).toHaveLength(4)
    expect(own("th")).toHaveLength(3 + 4 + 6 + 3)
    const code = own("pre").map(pre => pre.textContent)
    expect(code).toHaveLength(10)
    expect(code[2]).toContain("\tfmt.Printf(\"%.0f%%\\n\", Task{\"Support\", 42, 3}.SuccessRate()*100)")
    expect([...editor.querySelectorAll("a")].map(a => a.getAttribute("href"))).toEqual([
      "https://claude.ai/", "https://claude.ai/artifact/CBBoNZhbaHvUUUz2aQ2Ccv", "https://claude.ai/artifact/RwrX5Qdjw7zLHdvxjEABKL",
      "https://claude.ai/artifact/NwPV8rqSjHWDtpZPYUb2ko", "https://claude.ai/artifact/E8HYX1pSVKSAbLqgvTCZ8Z",
    ])
    expect(own(":not(pre) > code").map(c => c.textContent)).toEqual(["inline kód", "crew ls"])
    // Schema 4 formatting: emphasis, code languages, checklists and a quote, with no text stand-ins.
    expect(editor.querySelectorAll("strong").length).toBeGreaterThanOrEqual(5)
    expect(editor.querySelectorAll("em").length).toBeGreaterThanOrEqual(2)
    expect(editor.querySelectorAll("s")).toHaveLength(1)
    expect([...editor.querySelectorAll("strong")].some(strong => strong.closest("em") || strong.querySelector("em"))).toBe(true)
    expect(own("pre").map(pre => pre.getAttribute("data-language"))).toEqual(["latex", "python", "go", "typescript", "bash", "sql", "json", "yaml", "csv", "markdown"])
    // The mirrored LaTeX, CSV and Markdown samples render as previews; the other languages are coloured code.
    const block = (language: string) => editor.querySelector<HTMLElement>(`[data-code-block][data-language="${language}"]`)!
    await waitFor(() => expect(block("latex").querySelector("[data-code-preview] .katex")).not.toBeNull())
    await waitFor(() => expect(block("csv").querySelector("[data-code-preview] table")).not.toBeNull())
    await waitFor(() => expect(block("markdown").querySelector("[data-code-preview] > *")).not.toBeNull())
    expect(block("markdown").querySelector("[data-code-preview][data-error]")).toBeNull()
    expect(block("csv").querySelector("[data-code-preview][data-error]")).toBeNull()
    expect(block("python").querySelector("pre [class^=hljs-]")).not.toBeNull()
    expect(editor.querySelectorAll('ul[data-type="taskList"]')).toHaveLength(2)
    const boxes = [...editor.querySelectorAll<HTMLInputElement>('li[data-type="taskItem"] input[type="checkbox"]')]
    expect(boxes.map(box => box.checked)).toEqual([true, false, false, false])
    // Read-only here: the boxes show state but cannot be changed.
    expect(boxes.every(box => box.disabled)).toBe(true)
    expect(editor.querySelectorAll("blockquote")).toHaveLength(1)
    expect(editor.textContent).not.toMatch(/[☑☐]/)
  })

  it("keeps long code lines inside the reading column on a phone", async () => {
    open("phone")
    const editor = await screen.findByLabelText("Document editor")
    await within(editor).findByText("Co je Crewship")
    // A grid track sized to min-content grew past a 390 px screen on an unbreakable code line, clipping
    // every paragraph; the track is capped and a long code line scrolls inside its own block.
    expect(editor.closest("article")!.className).toContain("grid-cols-[minmax(0,1fr)]")
    expect(editor.className).toContain("[&_pre]:overflow-x-auto")
  })
})
