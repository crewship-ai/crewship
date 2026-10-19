import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { ComposerArtifactContextStrip } from "../composer/composer-artifact-context"
import { sendBlock, type AttachedArtifact, type ComposerArtifactContext } from "../composer/composer-context-model"

afterEach(cleanup)
const item = (pageId: string, revision?: number): AttachedArtifact => ({ title: pageId.toUpperCase(), target: revision === undefined ? { workspaceId: "w", pageId, mode: "latest" } : { workspaceId: "w", pageId, mode: "revision", revision } })
const ctx = (patch: Partial<ComposerArtifactContext> = {}): ComposerArtifactContext => ({ attachments: [item("a", 3)], mode: null, updateTarget: null, executor: "Ada", audience: { kind: "personal" }, ...patch })

it("plain messages are unaffected", () => { expect(sendBlock(ctx({ attachments: [] }))).toBeNull() })
it("an attached item requires a choice before anything runs", () => {
  expect(sendBlock(ctx())).toBe("choose_mode")
  expect(sendBlock(ctx({ mode: "update" }))).toBeNull()
})
it("several attachments make update ambiguous until a target is chosen", () => {
  const two = [item("a", 1), item("b")]
  expect(sendBlock(ctx({ attachments: two, mode: "update" }))).toBe("choose_target")
  expect(sendBlock(ctx({ attachments: two, mode: "update", updateTarget: 1 }))).toBeNull()
})
it("create/update need an executor and a confirmed audience; reply-only needs neither", () => {
  expect(sendBlock(ctx({ mode: "create", executor: null }))).toBe("no_executor")
  expect(sendBlock(ctx({ mode: "create", audience: { kind: "unknown" } }))).toBe("audience_unknown")
  expect(sendBlock(ctx({ mode: "reply_only", executor: null, audience: { kind: "unknown" } }))).toBeNull()
})
it("shows executor, location and real audience, and a proposal note for shared folders", () => {
  render(<ComposerArtifactContextStrip context={ctx({ mode: "update", audience: { kind: "folder", name: "Finance", people: 5 } })} onModeChange={() => {}} onUpdateTargetChange={() => {}} onDetach={() => {}} />)
  expect(screen.getByText("Ada")).toBeTruthy()
  expect(screen.getByText(/Saved to Finance\. 5 people can see it\. The agent prepares a proposal/)).toBeTruthy()
  expect(screen.getByText("Revision 3")).toBeTruthy()
})
it("blocks with an alert until a mode is chosen and reports choices", () => {
  const onModeChange = vi.fn(), onDetach = vi.fn()
  render(<ComposerArtifactContextStrip context={ctx()} onModeChange={onModeChange} onUpdateTargetChange={() => {}} onDetach={onDetach} />)
  expect(screen.getByRole("alert").textContent).toMatch(/Choose what the agent should do/)
  fireEvent.click(screen.getByRole("radio", { name: "Reply only" }))
  expect(onModeChange).toHaveBeenCalledWith("reply_only")
  fireEvent.click(screen.getByRole("button", { name: "Remove A" }))
  expect(onDetach).toHaveBeenCalledWith(0)
})
