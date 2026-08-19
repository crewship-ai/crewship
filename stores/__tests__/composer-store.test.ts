import { describe, it, expect, beforeEach } from "vitest"
import {
  useComposerStore,
  attachmentsForOwner,
  messageOwnAttachments,
  type ComposerAttachment,
} from "@/stores/composer-store"

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

// =============================================================================
// Which question an upload answers.
//
// The list stays keyed by SESSION — one session, one place a file can be, so
// the abort registry, Retry and the "deleted mid-upload" check keep working on
// one list. What each record now carries is the FIELD it answers, because
// every `file` / `photo` field of an open form used to read the whole list:
// one upload satisfied every required upload field at once.
// =============================================================================

const contract: ComposerAttachment = {
  id: "a1",
  name: "contract.pdf",
  size: 1,
  type: "application/pdf",
  status: "ready",
  path: "attachments/s1/contract.pdf",
  owner: { formId: "intake", field: "contract" },
}
const identity: ComposerAttachment = {
  id: "a2",
  name: "id.jpg",
  size: 1,
  type: "image/jpeg",
  status: "ready",
  path: "attachments/s1/id.jpg",
  owner: { formId: "intake", field: "identity" },
}
/** No owner: the composer's paperclip. The message's own file. */
const notes: ComposerAttachment = {
  id: "a3",
  name: "notes.pdf",
  size: 1,
  type: "application/pdf",
  status: "ready",
  path: "attachments/s1/notes.pdf",
}

describe("attachment ownership", () => {
  it("reads one field's uploads without reading the next field's", () => {
    const list = [contract, identity, notes]
    expect(
      attachmentsForOwner(list, { formId: "intake", field: "contract" }).map((a) => a.id),
    ).toEqual(["a1"])
    expect(
      attachmentsForOwner(list, { formId: "intake", field: "identity" }).map((a) => a.id),
    ).toEqual(["a2"])
    // Same field name, different form: a `document` field in one form is not
    // answered by a `document` uploaded into another.
    expect(attachmentsForOwner(list, { formId: "other", field: "contract" })).toEqual([])
    expect(messageOwnAttachments(list).map((a) => a.id)).toEqual(["a3"])
  })

  it("claims the chips a field's own upload just minted, by File identity", () => {
    const mine = new File(["x"], "contract.pdf")
    const theirs = new File(["y"], "dropped-on-the-composer.pdf")
    useComposerStore.getState().addAttachments("s1", [
      { id: "a1", name: "contract.pdf", size: 1, type: "", status: "uploading", file: mine },
      { id: "a2", name: "dropped-on-the-composer.pdf", size: 1, type: "", status: "uploading", file: theirs },
    ])

    useComposerStore
      .getState()
      .claimAttachmentsForFiles("s1", { formId: "intake", field: "contract" }, [mine])

    const list = useComposerStore.getState().attachments.s1
    expect(list[0].owner).toEqual({ formId: "intake", field: "contract" })
    // Matched on the File itself, so a file attached to the composer at the
    // same moment cannot be swept into a form field.
    expect(list[1].owner).toBeUndefined()
  })

  it("never re-assigns an attachment that already answers a field", () => {
    const f = new File(["x"], "contract.pdf")
    useComposerStore
      .getState()
      .addAttachments("s1", [{ ...contract, file: f } as ComposerAttachment])
    useComposerStore
      .getState()
      .claimAttachmentsForFiles("s1", { formId: "intake", field: "identity" }, [f])
    expect(useComposerStore.getState().attachments.s1[0].owner).toEqual({
      formId: "intake",
      field: "contract",
    })
  })

  it("updateAttachment keeps the field a chip answers", () => {
    useComposerStore.getState().addAttachments("s1", [{ ...contract, status: "uploading" }])
    useComposerStore
      .getState()
      .updateAttachment("s1", "a1", { status: "ready", path: "attachments/s1/contract.pdf" })
    const [a] = useComposerStore.getState().attachments.s1
    expect(a.status).toBe("ready")
    expect(a.owner).toEqual({ formId: "intake", field: "contract" })
  })

  // A message going out consumes the message's OWN attachments. An open form's
  // answers are not the message's to consume: the receipt somebody just
  // photographed into a question above the composer must survive an unrelated
  // send, and would otherwise vanish from a field that still looks answered.
  it("clearAttachments takes the message's own and leaves a form's answers", () => {
    useComposerStore.getState().addAttachments("s1", [contract, notes])
    useComposerStore.getState().clearAttachments("s1")
    expect(useComposerStore.getState().attachments.s1.map((a) => a.id)).toEqual(["a1"])
  })

  it("clearFormAttachments drops one form's answers and nothing else", () => {
    useComposerStore.getState().addAttachments("s1", [contract, identity, notes])
    useComposerStore.getState().clearFormAttachments("s1", "intake")
    expect(useComposerStore.getState().attachments.s1.map((a) => a.id)).toEqual(["a3"])

    // …and the key goes away entirely once nothing is left, so an emptied
    // session is indistinguishable from one that never had an attachment.
    useComposerStore.getState().clearAttachments("s1")
    expect(useComposerStore.getState().attachments.s1).toBeUndefined()
  })
})
