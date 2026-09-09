import React from "react"
import { afterEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { useApplicationActions } from "../use-application-actions"
const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
afterEach(() => { cleanup(); api.mockReset() })
let actions: ReturnType<typeof useApplicationActions>
function Harness() { actions = useApplicationActions("ws", "health", 3); return actions.confirmation }
const request = { id: 1, method: "runAction" as const, params: { panelId: "mysql", actionId: "check", inputs: { reason: "inspect" }, idempotencyKey: "retry-one" } }
it("never submits until trusted confirmation and preserves publication and key", async () => {
  api.mockResolvedValueOnce(new Response(JSON.stringify({ actions: [{ id: "check", kind: "call", label: "Check MySQL", routine: "check-mysql" }] })))
  api.mockResolvedValueOnce(new Response(JSON.stringify({ pending_id: "queued" }), { status: 202 }))
  render(<Harness />)
  let result!: Promise<unknown>
  act(() => { result = actions.handleRequest(request, new AbortController().signal) })
  await screen.findByText("Check MySQL")
  expect(api).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole("button", { name: "Run routine" }))
  await expect(result).resolves.toEqual({ pending_id: "queued" })
  expect(api.mock.calls[1][0]).toMatch(/application\/actions\/mysql\/check/)
  const init = api.mock.calls[1][1]
  expect(JSON.parse(init.body)).toEqual({ publication: 3, inputs: { reason: "inspect" } })
  expect(init.headers["Idempotency-Key"]).toBe("retry-one")
})
it("cancels the prompt when the application disconnects without queueing", async () => {
  api.mockResolvedValueOnce(new Response(JSON.stringify({ actions: [{ id: "check", kind: "call", label: "Check MySQL" }] })))
  render(<Harness />)
  const abort = new AbortController()
  let result!: Promise<unknown>
  act(() => { result = actions.handleRequest(request, abort.signal) })
  const outcome = expect(result).rejects.toThrow(/cancelled/)
  await screen.findByText("Check MySQL")
  act(() => abort.abort())
  await outcome
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(api).toHaveBeenCalledTimes(1)
})
it("bounds history reads and binds them to the mounted publication", async () => {
  api.mockResolvedValueOnce(new Response(JSON.stringify({ items: [], next_before: 0, publication: 3 })))
  render(<Harness />)
  const signal = new AbortController().signal
  await expect(actions.handleRequest({ id: 2, method: "getPanelHistory", params: { panelId: "mysql", limit: 5, before: 12 } }, signal)).resolves.toMatchObject({ publication: 3 })
  const url = new URL(api.mock.calls[0][0], "https://studio.example.com")
  expect(url.pathname).toBe("/api/v1/pages/health/application/panels/mysql/history")
  expect(Object.fromEntries(url.searchParams)).toEqual({ workspace_id: "ws", publication: "3", limit: "5", before: "12" })
  expect(screen.queryByRole("dialog")).toBeNull()
  await expect(actions.handleRequest({ id: 3, method: "getPanelHistory", params: { panelId: "mysql", limit: 21 } }, signal)).rejects.toThrow(/limit/)
  expect(api).toHaveBeenCalledTimes(1)
})
