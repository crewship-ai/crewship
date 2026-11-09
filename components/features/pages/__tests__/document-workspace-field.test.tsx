// The Docs tab strip chooses which Yjs fragment the document editor shows. This drives the real
// editor hook with a minimal session: the editor shows and edits only the chosen fragment.
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(), broadcastSessionExpired: vi.fn() }))
import * as React from "react"
import { act, cleanup, render, waitFor } from "@testing-library/react"
import { EditorContent, type Editor } from "@tiptap/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { useDocumentWorkspace } from "../page-document"

afterEach(cleanup)

function fragmentWith(doc: Y.Doc, name: string, id: string, words: string) {
  const paragraph = new Y.XmlElement("paragraph"); paragraph.setAttribute("id", id)
  const text = new Y.XmlText(); text.insert(0, words); paragraph.insert(0, [text])
  doc.getXmlFragment(name).insert(0, [paragraph])
}

function session(doc: Y.Doc) {
  return {
    doc, state: { page_id: "p", schema: 4, rev: 1, epoch: "e", can_write: true }, editable: true, denied: false, pending: 0, recovery: false,
    status: "Live", error: null, durability: null, storageUnavailable: false, storageBusy: false, storageLockless: false,
    unverifiedDrafts: [], recoveredDrafts: [], deny: () => {},
  } as unknown as Parameters<typeof useDocumentWorkspace>[0]["sync"]
}

let current: Editor | null = null
function Harness({ doc, field }: { doc: Y.Doc; field?: string }) {
  const sync = React.useMemo(() => session(doc), [doc])
  const { editor } = useDocumentWorkspace({ workspaceId: "ws", pageId: "p", slug: "s", sync, onRestored: () => {} }, undefined, field)
  current = editor
  return <EditorContent editor={editor} />
}

describe("document editor fragment", () => {
  it("shows the page by default and a tab's own page when its fragment is chosen, writing only there", async () => {
    const doc = new Y.Doc({ gc: false })
    fragmentWith(doc, "default", "intro", "First page")
    fragmentWith(doc, "tab:plan", "plan-p", "Plan page")
    const view = render(<Harness doc={doc} />)
    await waitFor(() => expect(current?.getText()).toBe("First page"))
    view.rerender(<Harness doc={doc} field="tab:plan" />)
    await waitFor(() => expect(current?.getText()).toBe("Plan page"))
    const before = doc.getXmlFragment("default").toJSON()
    await waitFor(() => expect(current?.isEditable).toBe(true))
    act(() => { current!.chain().setTextSelection(current!.state.doc.content.size - 1).insertContent(" updated").run() })
    expect(doc.getXmlFragment("tab:plan").toString()).toContain("Plan page updated")
    expect(doc.getXmlFragment("default").toJSON()).toBe(before)
    view.rerender(<Harness doc={doc} field="default" />)
    await waitFor(() => expect(current?.getText()).toBe("First page"))
  })
})
