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
