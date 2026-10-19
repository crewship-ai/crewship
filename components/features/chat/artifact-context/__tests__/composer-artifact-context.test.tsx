import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ComposerArtifactContextStrip } from "../composer/composer-artifact-context"
import { addAttachment, attachmentId, detachAttachment, sendBlock, type AttachedArtifact, type Audience, type ComposerArtifactContext } from "../composer/composer-context-model"

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })
const item = (pageId: string, revision?: number, patch: Partial<AttachedArtifact> = {}): AttachedArtifact => {
  const target = revision === undefined ? { workspaceId: "w", pageId, mode: "latest" as const } : { workspaceId: "w", pageId, mode: "revision" as const, revision }
  return { id: attachmentId(target), target, title: pageId.toUpperCase(), access: "readable", resultType: "Document", audience: { kind: "personal" }, ...patch }
}
const ctx = (patch: Partial<ComposerArtifactContext> = {}): ComposerArtifactContext => ({ attachments: [item("a", 3)], mode: null, updateTargetId: null, executor: "Ada", createAudience: { kind: "personal" }, createResultType: "Document", online: true, ...patch })
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
  expect(screen.getByText(/Saved to Finance\. 5 people can see it\. The agent prepares a proposal/)).toBeTruthy()
  expect(screen.queryByText(/Only you can see it/)).toBeNull()
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
