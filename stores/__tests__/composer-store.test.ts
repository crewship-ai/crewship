import { describe, it, expect, beforeEach } from "vitest"
import { useComposerStore } from "@/stores/composer-store"

beforeEach(() => {
  useComposerStore.setState({ modelId: null, drafts: {}, attachments: {} })
})

describe("useComposerStore", () => {
  it("setModel updates the global model", () => {
    useComposerStore.getState().setModel("claude-haiku-4-5")
    expect(useComposerStore.getState().modelId).toBe("claude-haiku-4-5")
  })

  it("setModel(null) clears the model", () => {
    useComposerStore.getState().setModel("claude-opus-4-7")
    useComposerStore.getState().setModel(null)
    expect(useComposerStore.getState().modelId).toBeNull()
  })

  it("setDraft / clearDraft are scoped per session", () => {
    const store = useComposerStore.getState()
    store.setDraft("s1", "hello")
    store.setDraft("s2", "world")
    expect(useComposerStore.getState().drafts.s1).toBe("hello")
    expect(useComposerStore.getState().drafts.s2).toBe("world")

    useComposerStore.getState().clearDraft("s1")
    const s = useComposerStore.getState()
    expect(s.drafts.s1).toBeUndefined()
    expect(s.drafts.s2).toBe("world")
  })

  it("addAttachments appends per session", () => {
    const att = { id: "a1", filename: "x.txt", mediaType: "text/plain" } as any
    useComposerStore.getState().addAttachments("s1", [att])
    expect(useComposerStore.getState().attachments.s1).toEqual([att])

    useComposerStore.getState().addAttachments("s1", [{ ...att, id: "a2" }])
    expect(useComposerStore.getState().attachments.s1).toHaveLength(2)
  })

  it("removeAttachment by id leaves others untouched", () => {
    const a1 = { id: "a1" } as any
    const a2 = { id: "a2" } as any
    useComposerStore.getState().addAttachments("s1", [a1, a2])
    useComposerStore.getState().removeAttachment("s1", "a1")
    expect(useComposerStore.getState().attachments.s1.map((a: any) => a.id)).toEqual(["a2"])
  })

  it("updateAttachment patches in place, keeping the list order", () => {
    const store = useComposerStore.getState()
    store.addAttachments("s1", [
      { id: "a1", name: "a.pdf", size: 1, type: "application/pdf", status: "uploading" },
      { id: "a2", name: "b.pdf", size: 2, type: "application/pdf", status: "uploading" },
    ])
    // The second one finishes first. It must NOT jump to the front or the
    // back: the message names paths in the order the user attached them.
    useComposerStore.getState().updateAttachment("s1", "a2", {
      status: "ready",
      path: "attachments/s1/b.pdf",
    })
    const list = useComposerStore.getState().attachments.s1
    expect(list.map((a) => a.id)).toEqual(["a1", "a2"])
    expect(list[1].status).toBe("ready")
    expect(list[1].path).toBe("attachments/s1/b.pdf")
    expect(list[0].status).toBe("uploading")
  })

  it("updateAttachment is a no-op for an id that is gone", () => {
    const store = useComposerStore.getState()
    store.addAttachments("s1", [
      { id: "a1", name: "a.pdf", size: 1, type: "application/pdf", status: "uploading" },
    ])
    const before = useComposerStore.getState().attachments
    // A user who deletes a chip mid-upload is authoritative: the response
    // landing afterwards must not resurrect it.
    useComposerStore.getState().updateAttachment("s1", "ghost", { status: "ready" })
    useComposerStore.getState().updateAttachment("s-nope", "a1", { status: "ready" })
    expect(useComposerStore.getState().attachments).toBe(before)
  })

  it("clearAttachments wipes the session's list", () => {
    useComposerStore.getState().addAttachments("s1", [{ id: "a1" } as any])
    useComposerStore.getState().clearAttachments("s1")
    expect(useComposerStore.getState().attachments.s1).toBeUndefined()
  })

  it("attachments per session are isolated", () => {
    useComposerStore.getState().addAttachments("s1", [{ id: "a1" } as any])
    useComposerStore.getState().addAttachments("s2", [{ id: "a2" } as any])
    useComposerStore.getState().clearAttachments("s1")
    expect(useComposerStore.getState().attachments.s2).toHaveLength(1)
  })
})
