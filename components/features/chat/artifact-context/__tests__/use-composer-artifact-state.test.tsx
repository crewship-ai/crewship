import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import * as client from "../client"
import type { LinkedViewerTarget } from "../linked-viewer-targets"
import { useComposerArtifactState } from "../use-composer-artifact-state"

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs(); vi.restoreAllMocks(); vi.useRealTimers() })
const target = (pageId: string): LinkedViewerTarget => ({ workspaceId: "w", pageId, mode: "latest" })
const preview = (rev: number) => ({ status: "ok" as const, data: { create: { audience: { kind: "personal" as const }, resultType: "Document" }, update: { audience: { kind: "personal" as const }, resultType: "Document", expected: { schema: 3, rev, generation: 1 } } } })
const input = { workspaceId: "w", chatId: "c", executor: "Ada", online: true, chatPrivacy: "private" as const }

async function setup(rev = 5) {
  vi.spyOn(client, "fetchArtifactPreview").mockResolvedValue(preview(rev))
  const hook = renderHook(() => useComposerArtifactState(input))
  act(() => hook.result.current.attach(target("a"), "A"))
  await waitFor(() => expect(hook.result.current.context.attachments[0]?.expected).not.toBeNull())
  act(() => hook.result.current.setMode("update"))
  return hook
}
const intentOf = (result: ReturnType<typeof setup> extends Promise<infer H> ? H : never, text: string) => {
  const prepared = result.result.current.prepareSend(text)
  if (!prepared.ok) throw new Error(prepared.message)
  return prepared.intent
}

it("a retry reuses the same submission id and page id after the context was edited and a late preview arrived", async () => {
  const hook = await setup(5)
  const first = intentOf(hook, "Shorten it")!
  act(() => hook.result.current.markSent("Shorten it", first))
  // Edits and a late preview result between the send and the echo must not drop the pending intent.
  act(() => hook.result.current.setMode("reply_only"))
  act(() => hook.result.current.setMode("update"))
  vi.spyOn(client, "fetchArtifactPreview").mockResolvedValue(preview(9))
  act(() => hook.result.current.attach(target("a"), "A"))
  const retry = intentOf(hook, "Shorten it")!
  expect(retry).toEqual(first)
})
it("a changed instruction or a changed target gets a new submission id", async () => {
  const hook = await setup()
  const first = intentOf(hook, "Shorten it")!
  act(() => hook.result.current.markSent("Shorten it", first))
  expect(intentOf(hook, "Make it longer")!.submission_id).not.toBe(first.submission_id)
  act(() => hook.result.current.attach(target("b"), "B"))
  await waitFor(() => expect(hook.result.current.context.attachments).toHaveLength(2))
  await waitFor(() => expect(hook.result.current.context.attachments[1].expected).not.toBeNull())
  act(() => hook.result.current.setUpdateTarget(hook.result.current.context.attachments[1].id))
  const other = intentOf(hook, "Shorten it")!
  expect(other.page_id).toBe("b")
  expect(other.submission_id).not.toBe(first.submission_id)
})
it("only the exact message or the submission id confirms: not a prefix, not another intent, not an unrelated echo", async () => {
  const hook = await setup()
  const first = intentOf(hook, "Shorten it")!
  act(() => hook.result.current.markSent("Shorten it", first))
  act(() => hook.result.current.confirmEcho("Shorten it and add a summary", { author_user_id: "me" }))
  act(() => hook.result.current.confirmEcho("Something else", { author_user_id: "me" }))
  act(() => hook.result.current.confirmEcho("Shorten it", { author_user_id: "me", document_iteration_intent: { submission_id: "another-tab" } }))
  expect(hook.result.current.context.attachments).toHaveLength(1)
  act(() => hook.result.current.confirmEcho("Shorten it", { author_user_id: "me", document_iteration_intent: { submission_id: first.submission_id } }))
  expect(hook.result.current.context.attachments).toHaveLength(0)
})
it("the exact text confirms when the echo carries no id, and an edit made after the send survives the echo", async () => {
  const hook = await setup()
  const first = intentOf(hook, "Shorten it")!
  act(() => hook.result.current.markSent("Shorten it", first))
  act(() => hook.result.current.setMode("reply_only"))
  act(() => hook.result.current.confirmEcho("Shorten it", { author_user_id: "me" }))
  // The sent intent is spent, but the sender's newer choice is not thrown away.
  expect(hook.result.current.context.mode).toBe("reply_only")
  expect(hook.result.current.context.unconfirmed).toBe(false)
})
it("shows a not-confirmed state when no echo comes, and a retry sends the same request", async () => {
  const hook = await setup()
  vi.useFakeTimers()
  const first = intentOf(hook, "Shorten it")!
  act(() => hook.result.current.markSent("Shorten it", first))
  expect(hook.result.current.context.unconfirmed).toBe(false)
  act(() => { vi.advanceTimersByTime(9000) })
  expect(hook.result.current.context.unconfirmed).toBe(true)
  expect(intentOf(hook, "Shorten it")).toEqual(first)
})
