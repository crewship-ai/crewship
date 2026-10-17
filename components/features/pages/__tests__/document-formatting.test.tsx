import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { execFileSync } from "node:child_process"
import { resolve } from "node:path"
import { pathToFileURL } from "node:url"
import { act, cleanup, render as baseRender, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import type { Editor } from "@tiptap/core"
import { PageDocument } from "../page-document"
import { encodeUpdate } from "@/hooks/use-page-document"
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({data:true,isPending:false}) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
afterEach(() => { cleanup(); request.mockReset() })
it("validates actual heading, Enter, list, and table updates including retained stable IDs", async () => {
  const saved = new Y.Doc({ gc: false })
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", "paragraph")
  const text = new Y.XmlText(); text.insert(0, "Alpha Beta"); paragraph.insert(0, [text]); saved.getXmlFragment("default").insert(0, [paragraph])
  let snapshot = encodeUpdate(Y.encodeStateAsUpdate(saved))
  request.mockImplementation(async (url: string) => ({ ok: true, json: async () => url.includes("/comments") ? { comments: [], can_comment: false } : url.includes("/requests") ? { requests: [], can_dispatch: false, eligible_chats: [] } : { page_id:"page", schema: 2, rev: 1, epoch: "epoch", can_write: true, snapshot } }))
  render(<PageDocument workspaceId="w" slug="notes" pageId="page" />)
  await screen.findByText("Alpha Beta")
  const editor = (screen.getByLabelText("Document editor") as HTMLElement & { editor: Editor }).editor
  await waitFor(() => expect(editor.isEditable).toBe(true))
  act(() => { editor.commands.setTextSelection(11); editor.commands.keyboardShortcut("Enter"); editor.commands.toggleHeading({ level: 2 }); editor.commands.insertContent("Acceptance heading"); editor.commands.keyboardShortcut("Enter"); editor.commands.toggleBulletList(); editor.commands.insertContent("Acceptance list item"); editor.commands.keyboardShortcut("Enter"); editor.commands.keyboardShortcut("Enter"); editor.commands.insertTable({ rows: 3, cols: 3, withHeaderRow: true }) })
  // Capture the CRDT directly from the configured collaboration document.
  const collaboration = editor.extensionManager.extensions.find(extension => extension.name === "collaboration")!
  snapshot = encodeUpdate(Y.encodeStateAsUpdate(collaboration.options.document))
  // Validate in the production Node module boundary: its Yjs constructors must
  // decode client bytes, rather than sharing browser objects across packages.
  const schemaURL = pathToFileURL(resolve(process.cwd(), "tools/docs-service/schema.mjs")).href
  const code = `import { load, validate } from ${JSON.stringify(schemaURL)}; let input = ''; for await (const chunk of process.stdin) input += chunk; const doc = load(input); try { process.stdout.write(JSON.stringify(validate(doc))) } finally { doc.destroy() }`
  const projection = JSON.parse(execFileSync("node", ["--input-type=module", "-e", code], { input: snapshot, encoding: "utf8" })) as { content: { type: string }[] }
  expect(projection.content.some(node => node.type === "heading")).toBe(true)
  expect(projection.content.some(node => node.type === "bulletList")).toBe(true)
  expect(projection.content.some(node => node.type === "table")).toBe(true)
  saved.destroy()
})

function render(element:ReactNode) {
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})
  return baseRender(createElement(QueryClientProvider,{client},element))
}
