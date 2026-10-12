import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { useAskForms } from "../asks/use-ask-forms"
import type { AskForm } from "../asks/types"

const api = vi.hoisted(() => ({ fetch: vi.fn(), workspaceId: "workspace-a" }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api.fetch }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: api.workspaceId }) }))

const form: AskForm = { id: "receipt", label: "Add receipt", template: "Receipt from {{supplier}}", fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }] }
const answer = (forms: AskForm[]) => ({ ok: true, json: async () => ({ ask_forms: JSON.stringify(forms) }) })
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
beforeEach(() => { api.workspaceId = "workspace-a"; api.fetch.mockReset() })
afterEach(cleanup)

it.each(["workspace", "agent", "missing workspace", "missing agent"])("clears the old questionnaire when changing %s", async (change) => {
  api.fetch.mockResolvedValueOnce(answer([form])).mockReturnValue(new Promise(() => {}))
  const { result, rerender } = renderHook(({ id }) => useAskForms(id), { initialProps: { id: "agent-a" } })
  await waitFor(() => expect(result.current).toEqual([form]))
  if (change.includes("workspace")) api.workspaceId = change === "workspace" ? "workspace-b" : ""
  rerender({ id: change === "agent" ? "agent-b" : change === "missing agent" ? "" : "agent-a" })
  expect(result.current).toEqual([])
})

it.each(["network", "body", "http"])("does not retain another agent's questionnaire after a %s failure", async (failure) => {
  api.fetch.mockResolvedValueOnce(answer([form]))
  const { result, rerender } = renderHook(({ id }) => useAskForms(id), { initialProps: { id: "agent-a" } })
  await waitFor(() => expect(result.current).toEqual([form]))
  if (failure === "network") api.fetch.mockRejectedValueOnce(new Error("offline"))
  if (failure === "body") api.fetch.mockResolvedValueOnce({ ok: true, json: async () => { throw new Error("invalid JSON") } })
  if (failure === "http") api.fetch.mockResolvedValueOnce({ ok: false })
  await act(async () => rerender({ id: "agent-b" }))
  expect(result.current).toEqual([])
})

it("uses provided forms immediately and treats an empty inventory as authoritative", () => {
  const { result, rerender } = renderHook(({ provided }: { provided: AskForm[] }) => useAskForms("agent-a", provided), { initialProps: { provided: [form] } })
  expect(result.current).toEqual([form])
  rerender({ provided: [] })
  expect(result.current).toEqual([])
  expect(api.fetch).not.toHaveBeenCalled()
})

it("aborts a pending fallback and ignores its late body after forms become provided", async () => {
  const body = deferred<{ ask_forms: string }>()
  api.fetch.mockResolvedValue({ ok: true, json: () => body.promise })
  const { result, rerender } = renderHook(({ provided }: { provided: AskForm[] | undefined }) => useAskForms("agent-a", provided), { initialProps: { provided: undefined as AskForm[] | undefined } })
  await act(async () => {})
  const signal = api.fetch.mock.calls[0][1].signal as AbortSignal
  rerender({ provided: [] })
  expect(signal.aborted).toBe(true)
  await act(async () => body.resolve({ ask_forms: JSON.stringify([form]) }))
  expect(result.current).toEqual([])
})

it("encodes identifiers and discards late results from a previous workspace", async () => {
  const old = deferred<ReturnType<typeof answer>>()
  api.fetch.mockReturnValueOnce(old.promise).mockResolvedValueOnce(answer([form]))
  const { result, rerender } = renderHook(() => useAskForms("agent/a?b"))
  const signal = api.fetch.mock.calls[0][1].signal as AbortSignal
  api.workspaceId = "workspace/b&c"
  rerender()
  expect(api.fetch).toHaveBeenLastCalledWith("/api/v1/agents/agent%2Fa%3Fb?workspace_id=workspace%2Fb%26c", expect.anything())
  await waitFor(() => expect(result.current).toEqual([form]))
  expect(signal.aborted).toBe(true)
  await act(async () => old.resolve(answer([])))
  expect(result.current).toEqual([form])
})

it("aborts its fallback on unmount", () => {
  api.fetch.mockReturnValue(new Promise(() => {}))
  const { unmount } = renderHook(() => useAskForms("agent-a"))
  const signal = api.fetch.mock.calls[0][1].signal as AbortSignal
  unmount()
  expect(signal.aborted).toBe(true)
})
