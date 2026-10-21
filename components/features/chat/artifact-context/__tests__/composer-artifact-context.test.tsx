import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ComposerArtifactContextStrip } from "../composer/composer-artifact-context"
import { addAttachment, attachmentId, buildIterationIntent, detachAttachment, sendBlock, type AttachedArtifact, type Audience, type ComposerArtifactContext } from "../composer/composer-context-model"

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })
const item = (pageId: string, revision?: number, patch: Partial<AttachedArtifact> = {}): AttachedArtifact => {
  const target = revision === undefined ? { workspaceId: "w", pageId, mode: "latest" as const } : { workspaceId: "w", pageId, mode: "revision" as const, revision, checkpointId: `cp-${pageId}-${revision}` }
  return { id: attachmentId(target), target, title: pageId.toUpperCase(), access: "readable", resultType: "Document", audience: { kind: "personal" }, expected: { schema: 3, rev: revision ?? 5, generation: 2 }, ...patch }
}
const ctx = (patch: Partial<ComposerArtifactContext> = {}): ComposerArtifactContext => ({ attachments: [item("a", 3)], mode: null, updateTargetId: null, executor: "Ada", chatPrivacy: "private", serverConfirmedPrivate: true, unconfirmed: false, createAudience: { kind: "personal" }, createResultType: "Document", online: true, ...patch })
const noop = () => {}
const strip = (c: ComposerArtifactContext, h: Partial<Parameters<typeof ComposerArtifactContextStrip>[0]> = {}) =>
  render(<ComposerArtifactContextStrip context={c} onModeChange={noop} onUpdateTargetChange={noop} onDetach={noop} {...h} />)

it("plain messages are unaffected", () => { expect(sendBlock(ctx({ attachments: [] }))).toBeNull() })
it("an attached item requires a choice before anything runs", () => {
  expect(sendBlock(ctx())).toBe("choose_mode")
  expect(sendBlock(ctx({ mode: "update" }))).toBeNull()
})
it("several attachments make update ambiguous until a target is chosen, and the select proves it", () => {
  const two = [item("a", 1), item("b")]
  const onUpdateTargetChange = vi.fn()
  strip(ctx({ attachments: two, mode: "update" }), { onUpdateTargetChange })
  expect(screen.getByRole("alert").textContent).toMatch(/Choose which attached item/)
  fireEvent.change(screen.getByLabelText("Item to update"), { target: { value: two[1].id } })
  expect(onUpdateTargetChange).toHaveBeenCalledWith(two[1].id)
  expect(sendBlock(ctx({ attachments: two, mode: "update", updateTargetId: two[1].id }))).toBeNull()
})
it("detaching the first item after choosing the second keeps the choice on the second, and detaching the chosen one clears it", () => {
  const three = [item("a"), item("b"), item("c")]
  const chosen = ctx({ attachments: three, mode: "update", updateTargetId: three[1].id })
  const afterFirst = detachAttachment(chosen, three[0].id)
  expect(afterFirst.updateTargetId).toBe(three[1].id)
  expect(afterFirst.attachments.map(a => a.title)).toEqual(["B", "C"])
  const afterChosen = detachAttachment(chosen, three[1].id)
  expect(afterChosen.updateTargetId).toBeNull()
  expect(sendBlock(afterChosen)).toBe("choose_target")
  // A stale id that matches nothing never unblocks.
  expect(sendBlock(ctx({ attachments: three, mode: "update", updateTargetId: "gone" }))).toBe("choose_target")
})
it("attaching the same item twice keeps one attachment", () => {
  const base = ctx({ attachments: [item("a", 1)] })
  expect(addAttachment(base, item("a", 1)).attachments).toHaveLength(1)
  expect(addAttachment(base, item("a", 2)).attachments).toHaveLength(2)
})
it("create/update need an executor and a confirmed audience; reply-only needs neither", () => {
  expect(sendBlock(ctx({ mode: "create", executor: null }))).toBe("no_executor")
  expect(sendBlock(ctx({ mode: "create", createAudience: { kind: "unknown" } }))).toBe("audience_unknown")
  expect(sendBlock(ctx({ mode: "update", attachments: [item("a", 1, { audience: { kind: "unknown" } })] }))).toBe("audience_unknown")
  expect(sendBlock(ctx({ mode: "reply_only", executor: null, createAudience: { kind: "unknown" } }))).toBeNull()
  expect(sendBlock(ctx({ mode: "update", attachments: [] }))).toBe("need_attachment")
})
it("an attachment that lost access drops its name everywhere and blocks sending, even reply-only", () => {
  const lost = item("secret", 1, { access: "lost", title: null })
  const onDetach = vi.fn()
  const view = strip(ctx({ attachments: [lost], mode: "reply_only" }), { onDetach })
  expect(sendBlock(ctx({ attachments: [lost], mode: "reply_only" }))).toBe("attachment_lost")
  expect(view.container.textContent).not.toMatch(/SECRET/i)
  expect(screen.getByRole("alert").textContent).toMatch(/no longer have access/)
  fireEvent.click(screen.getByRole("button", { name: "Remove unavailable item" }))
  expect(onDetach).toHaveBeenCalledWith(lost.id)
})
it("blocks while offline", () => {
  expect(sendBlock(ctx({ mode: "reply_only", online: false }))).toBe("offline")
  strip(ctx({ mode: "reply_only", online: false }))
  expect(screen.getByRole("alert").textContent).toMatch(/offline/)
})
it("shows the audience of the chosen item, not one shared value", () => {
  const folder: Audience = { kind: "folder", name: "Finance", people: 5 }
  const two = [item("a", undefined, { audience: { kind: "personal" } }), item("b", undefined, { audience: folder, resultType: "Sheet" })]
  strip(ctx({ attachments: two, mode: "update", updateTargetId: two[1].id }))
  expect(screen.getByText(/Saved to Finance\. 5 people can see it\. Editing an item in a shared folder is not available yet/)).toBeTruthy()
  expect(screen.getByRole("alert").textContent).toMatch(/shared folder/)
  expect(screen.queryByText(/Workspace owners and admins can still read it/)).toBeNull()
  expect(screen.getByText("Sheet")).toBeTruthy()
})
it("shows result type for create and warns when updating a pinned revision", () => {
  const view = strip(ctx({ mode: "create" }))
  expect(screen.getByText("Result type")).toBeTruthy()
  view.unmount()
  strip(ctx({ mode: "update" }))
  expect(screen.getByText(/applies to the latest revision and may conflict/)).toBeTruthy()
})
it("the mode picker is a native radio group and reports choices", () => {
  const onModeChange = vi.fn(), onDetach = vi.fn()
  strip(ctx(), { onModeChange, onDetach })
  expect(screen.getByRole("alert").textContent).toMatch(/Choose what the agent should do/)
  fireEvent.click(screen.getByRole("radio", { name: "Reply only" }))
  expect(onModeChange).toHaveBeenCalledWith("reply_only")
  fireEvent.click(screen.getByRole("button", { name: "Remove A" }))
  expect(onDetach).toHaveBeenCalledWith(item("a", 3).id)
})
it("renders nothing with the flag off", () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "")
  expect(strip(ctx()).container.innerHTML).toBe("")
})
it("an update needs the confirmed schema, revision and generation", () => {
  expect(sendBlock(ctx({ mode: "update", attachments: [item("a", 1, { expected: null })] }))).toBe("state_unknown")
})
it("builds the intent as exactly the five flat fields, only for an explicit update of one personal target", () => {
  const two = [item("a", 1), item("b")]
  expect(buildIterationIntent(ctx({ attachments: two }), "sub-1")).toBeNull()
  expect(buildIterationIntent(ctx({ attachments: two, mode: "update" }), "sub-1")).toBeNull()
  const intent = buildIterationIntent(ctx({ attachments: two, mode: "update", updateTargetId: two[1].id }), "sub-1")
  expect(intent).toEqual({ submission_id: "sub-1", page_id: "b", schema: 3, rev: 5, generation: 2 })
  expect(Object.keys(intent!).sort()).toEqual(["generation", "page_id", "rev", "schema", "submission_id"])
  expect(JSON.stringify(intent)).not.toMatch(/"A"|"B"|title|mode|principal/)
  // Create, reply-only and plain context are never a mandate.
  expect(buildIterationIntent(ctx({ mode: "create" }), "sub-2")).toBeNull()
  expect(buildIterationIntent(ctx({ mode: "reply_only" }), "sub-2")).toBeNull()
  expect(buildIterationIntent(ctx({ mode: "update" }), "")).toBeNull()
})
it("takes the values of the originally readable target, not the pinned revision number", () => {
  const pinned = item("a", 1, { expected: { schema: 3, rev: 9, generation: 4 } })
  expect(buildIterationIntent(ctx({ attachments: [pinned], mode: "update" }), "s")).toMatchObject({ page_id: "a", rev: 9, generation: 4 })
})
it("only schema 3 can be targeted, and an item in a shared folder cannot be edited", () => {
  expect(buildIterationIntent(ctx({ mode: "update", attachments: [item("a", 1, { expected: { schema: 2, rev: 1, generation: 0 } })] }), "s")).toBeNull()
  const shared = item("a", undefined, { audience: { kind: "folder", name: "Finance", people: 3 } })
  expect(sendBlock(ctx({ mode: "update", attachments: [shared] }))).toBe("shared_folder")
  expect(buildIterationIntent(ctx({ mode: "update", attachments: [shared] }), "s")).toBeNull()
  strip(ctx({ attachments: [shared] }))
  expect(screen.getByRole("radio", { name: "Update attached" })).toBeDisabled()
  expect(screen.getByText(/needs a proposal, which is not available yet/)).toBeTruthy()
})
it("the same id gives the same intent when sending again", () => {
  const c = ctx({ mode: "update" })
  expect(buildIterationIntent(c, "sub-3")).toEqual(buildIterationIntent(c, "sub-3"))
})

it("an update needs a private chat and an instruction", () => {
  expect(sendBlock(ctx({ mode: "update", chatPrivacy: "not_private" }))).toBe("chat_not_private")
  expect(buildIterationIntent(ctx({ mode: "update", chatPrivacy: "not_private" }), "s")).toBeNull()
  expect(sendBlock(ctx({ mode: "update" }), "   ")).toBe("need_instruction")
  expect(sendBlock(ctx({ mode: "update" }), "Shorten it")).toBeNull()
  // Other modes do not depend on either.
  expect(sendBlock(ctx({ mode: "reply_only", chatPrivacy: "not_private" }), "")).toBeNull()
  strip(ctx({ chatPrivacy: "not_private" }))
  expect(screen.getByRole("radio", { name: "Update attached" })).toBeDisabled()
  expect(screen.getByText(/needs a private chat/)).toBeTruthy()
})

it("says why editing is not offered while chat visibility is unknown", () => {
  expect(sendBlock(ctx({ mode: "update", chatPrivacy: "checking" }))).toBe("chat_checking")
  expect(sendBlock(ctx({ mode: "update", chatPrivacy: "unverified" }))).toBe("chat_unverified")
  const view = strip(ctx({ chatPrivacy: "checking" }))
  expect(screen.getByRole("radio", { name: "Update attached" })).toBeDisabled()
  expect(screen.getByText("Checking chat visibility…")).toBeTruthy()
  view.unmount()
  strip(ctx({ chatPrivacy: "unverified" }))
  expect(screen.getByText("Could not verify this chat is private.")).toBeTruthy()
})
it("tells the sender when a send is not confirmed", () => {
  strip(ctx({ mode: "update", unconfirmed: true }))
  expect(screen.getByRole("status").textContent).toMatch(/not confirmed.*same request/i)
})

it("an update is enabled only when the server confirmed a private chat AND the local checks pass; the local estimate never enables it", () => {
  const update = (patch: Partial<ComposerArtifactContext>) => ctx({ mode: "update", ...patch })
  // Seam true and local checks pass: available.
  expect(sendBlock(update({}), "Shorten it")).toBeNull()
  // Seam false: blocked whatever the client thinks, with the honest reason.
  expect(sendBlock(update({ serverConfirmedPrivate: false, chatPrivacy: "private" }), "Shorten it")).toBe("server_unconfirmed")
  expect(buildIterationIntent(update({ serverConfirmedPrivate: false }), "s")).toBeNull()
  // Seam true but a local check fails: still blocked (defence in depth).
  expect(sendBlock(update({ chatPrivacy: "not_private" }), "Shorten it")).toBe("chat_not_private")
  expect(sendBlock(update({ chatPrivacy: "unverified" }), "Shorten it")).toBe("chat_unverified")
  expect(sendBlock(update({ attachments: [item("a", undefined, { audience: { kind: "folder", name: "F", people: 2 } })] }), "Shorten it")).toBe("shared_folder")
  // The radio follows the same rule and says why.
  const off = strip(ctx({ serverConfirmedPrivate: false }))
  expect(screen.getByRole("radio", { name: "Update attached" })).toBeDisabled()
  expect(screen.getByText("Updating a document from chat is not available yet: the server does not confirm this chat's audience.")).toBeTruthy()
  off.unmount()
  strip(ctx({}))
  expect(screen.getByRole("radio", { name: "Update attached" })).toBeEnabled()
})
