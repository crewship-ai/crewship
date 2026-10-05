import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { MissionControlBar } from "../mission-control-bar"
import { now, ago, task, mission } from "./mission-fixtures"
import type { MissionStatus } from "@/lib/types/mission"

const state = vi.hoisted(() => ({ api: vi.fn(), success: vi.fn(), error: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: state.api }))
vi.mock("sonner", () => ({ toast: { success: state.success, error: state.error } }))
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
beforeEach(() => { state.api.mockReset(); state.success.mockReset(); state.error.mockReset(); vi.useFakeTimers({ toFake: ["Date"] }); vi.setSystemTime(now) })
afterEach(() => { cleanup(); vi.useRealTimers() })
const actions = [
  { label: "Start Mission", status: "PLANNING", suffix: "/start", method: "POST", body: undefined, toast: "Mission started" },
  { label: "Cancel", status: "IN_PROGRESS", suffix: "", method: "PATCH", body: "CANCELLED", toast: "Mission cancelled" },
  { label: "Complete", status: "REVIEW", suffix: "", method: "PATCH", body: "DONE", toast: "Mission completed" },
  { label: "Restart", status: "DONE", suffix: "/restart", method: "POST", body: undefined, toast: "Mission reset to Planning — incomplete tasks requeued" },
  { label: "Clone", status: "DONE", suffix: "/clone", method: "POST", body: undefined, toast: "Mission cloned — select it from the dropdown" },
] as const

it.each(actions)("$label sends the requested mutation and refreshes only after success", async (action) => {
  const pending = deferred<Response>()
  state.api.mockReturnValue(pending.promise)
  const changed = vi.fn()
  render(<MissionControlBar mission={mission("m", { status: action.status, tasks: [task("t")] })} workspaceId="workspace & one" onMissionChanged={changed} />)
  fireEvent.click(screen.getByRole("button", { name: action.label }))
  expect(changed).not.toHaveBeenCalled()
  for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled()
  expect(state.api).toHaveBeenCalledTimes(1)
  const [url, init] = state.api.mock.calls[0]
  expect(url).toBe(`/api/v1/crews/crew/missions/m${action.suffix}?workspace_id=workspace%20%26%20one`)
  expect(init.method).toBe(action.method)
  if (action.body) expect(JSON.parse(init.body)).toEqual({ status: action.body })
  else expect(init.body).toBeUndefined()
  await act(async () => pending.resolve(json({})))
  expect(changed).toHaveBeenCalledTimes(1)
  expect(state.success).toHaveBeenCalledWith(action.toast)
  expect(state.error).not.toHaveBeenCalled()
  expect(screen.getByRole("button", { name: action.label })).toBeEnabled()
})

it.each(actions)("$label handles refusal, malformed JSON and network errors and permits retry", async (action) => {
  const changed = vi.fn()
  render(<MissionControlBar mission={mission("m", { status: action.status, tasks: [task("t")] })} workspaceId="workspace" onMissionChanged={changed} />)
  for (const failure of ["refusal", "invalid", "network"] as const) {
    state.api.mockImplementation(() => failure === "network" ? Promise.reject(new Error("offline")) : Promise.resolve(failure === "refusal" ? json({ detail: "Action refused by server" }, 403) : new Response("not JSON", { status: 503 })))
    fireEvent.click(screen.getByRole("button", { name: action.label }))
    await waitFor(() => expect(screen.getByRole("button", { name: action.label })).toBeEnabled())
    if (failure === "refusal") expect(state.error).toHaveBeenLastCalledWith("Action refused by server")
    else expect(state.error).toHaveBeenLastCalledWith(expect.stringMatching(/^Failed to .* mission$/))
    expect(changed).not.toHaveBeenCalled()
    expect(state.success).not.toHaveBeenCalled()
  }
  state.api.mockResolvedValue(json({}))
  fireEvent.click(screen.getByRole("button", { name: action.label }))
  await waitFor(() => expect(changed).toHaveBeenCalledTimes(1))
})

it.each(actions)("$label ignores completion after selection changes", async (action) => {
  const pending = deferred<Response>()
  state.api.mockReturnValue(pending.promise)
  const oldChanged = vi.fn()
  const freshChanged = vi.fn()
  const view = render(<MissionControlBar mission={mission("old", { status: action.status, tasks: [task("t")] })} workspaceId="old" onMissionChanged={oldChanged} />)
  fireEvent.click(screen.getByRole("button", { name: action.label }))
  view.rerender(<MissionControlBar mission={mission("new", { status: action.status, tasks: [task("new")] })} workspaceId="new" onMissionChanged={freshChanged} />)
  expect(screen.getByRole("button", { name: action.label })).toBeEnabled()
  await act(async () => pending.resolve(json({})))
  expect(oldChanged).not.toHaveBeenCalled()
  expect(freshChanged).not.toHaveBeenCalled()
  expect(state.success).not.toHaveBeenCalled()
})

it("disables mutation controls when workspace identity is missing", () => {
  render(<MissionControlBar mission={mission("m", { status: "PLANNING", tasks: [task("t")] })} workspaceId="" onMissionChanged={vi.fn()} />)
  for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled()
  expect(state.api).not.toHaveBeenCalled()
})

it("disables start without tasks and reflects lifecycle-specific actions", () => {
  const view = render(<MissionControlBar mission={mission("m", { status: "PLANNING" })} workspaceId="workspace" onMissionChanged={vi.fn()} />)
  expect(screen.getByRole("button", { name: "Start Mission" })).toBeDisabled()
  for (const status of ["DONE", "COMPLETED", "FAILED", "CANCELLED", "REVIEW", "BACKLOG"] satisfies MissionStatus[]) {
    view.rerender(<MissionControlBar mission={mission("m", { status })} workspaceId="workspace" onMissionChanged={vi.fn()} />)
    expect(screen.queryByRole("button", { name: "Start Mission" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument()
    if (status === "BACKLOG") expect(screen.queryByRole("button", { name: "Restart" })).not.toBeInTheDocument()
    else expect(screen.getByRole("button", { name: "Restart" })).toBeEnabled()
    expect(screen.getByRole("button", { name: "Clone" })).toBeEnabled()
  }
})

it("aggregates task progress, failures and metered usage", () => {
  render(<MissionControlBar mission={mission("m", { tasks: [
    task("done", { status: "COMPLETED", token_count: 1000, estimated_cost: 0.25 }),
    task("failed", { status: "FAILED", token_count: 500, estimated_cost: 0.125 }),
    task("running"), task("waiting", { status: "PENDING" }),
  ] })} workspaceId="workspace" onMissionChanged={vi.fn()} />)
  expect(screen.getByText("1/4")).toBeVisible()
  expect(screen.getByText("(1 failed)")).toBeVisible()
  expect(screen.getByText("(1 running)")).toBeVisible()
  expect(screen.getByText("1.5k tok · $0.3750")).toBeVisible()
  expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "25")
})

it("updates live duration from the earliest task and stops its timer on unmount", () => {
  vi.useRealTimers()
  vi.useFakeTimers()
  vi.setSystemTime(now)
  const view = render(<MissionControlBar mission={mission("m", { tasks: [task("later", { started_at: ago(5) }), task("first", { started_at: ago(59), token_count: 42 }), task("last", { started_at: ago(1) })] })} workspaceId="workspace" onMissionChanged={vi.fn()} />)
  expect(screen.getByText("59s")).toBeVisible()
  expect(screen.getByText("42 tok")).toBeVisible()
  act(() => vi.advanceTimersByTime(2000))
  expect(screen.getByText("1m 1s")).toBeVisible()
  act(() => vi.advanceTimersByTime(3600000))
  expect(screen.getByText("1h 1m")).toBeVisible()
  view.unmount()
  expect(vi.getTimerCount()).toBe(0)
})

it.each(["response body", "transport"])("ignores an abandoned %s failure while the new mission is pending", async (failure) => {
  const oldBody = deferred<unknown>()
  const oldRequest = deferred<Response>()
  const fresh = deferred<Response>()
  let rejectOld!: (error: Error) => void
  const oldTransport = new Promise<Response>((_, reject) => { rejectOld = reject })
  state.api.mockReturnValueOnce(failure === "response body" ? oldRequest.promise : oldTransport).mockReturnValueOnce(fresh.promise)
  const changed = vi.fn()
  const view = render(<MissionControlBar mission={mission("old", { status: "DONE" })} workspaceId="ws" onMissionChanged={changed} />)
  fireEvent.click(screen.getByRole("button", { name: "Clone" }))
  const signal = state.api.mock.calls[0][1].signal as AbortSignal
  if (failure === "response body") {
    const response = json({}, 403)
    vi.spyOn(response, "json").mockReturnValue(oldBody.promise)
    await act(async () => oldRequest.resolve(response))
  }
  view.rerender(<MissionControlBar mission={mission("new", { status: "DONE" })} workspaceId="ws" onMissionChanged={changed} />)
  expect(signal.aborted).toBe(true)
  fireEvent.click(screen.getByRole("button", { name: "Clone" }))
  await act(async () => {
    if (failure === "response body") oldBody.resolve({ detail: "stale refusal" })
    else rejectOld(new Error("stale transport"))
  })
  expect(screen.getByRole("button", { name: "Clone" })).toBeDisabled()
  expect(state.error).not.toHaveBeenCalled()
  expect(changed).not.toHaveBeenCalled()
  await act(async () => fresh.resolve(json({})))
  expect(changed).toHaveBeenCalledTimes(1)
})

it("encodes resource identities and cancels a pending mutation on unmount", async () => {
  const pending = deferred<Response>()
  state.api.mockReturnValue(pending.promise)
  const changed = vi.fn()
  const view = render(<MissionControlBar mission={mission("mission/one", { crew_id: "crew & one", status: "DONE" })} workspaceId="ws" onMissionChanged={changed} />)
  fireEvent.click(screen.getByRole("button", { name: "Clone" }))
  expect(state.api.mock.calls[0][0]).toBe("/api/v1/crews/crew%20%26%20one/missions/mission%2Fone/clone?workspace_id=ws")
  const signal = state.api.mock.calls[0][1].signal as AbortSignal
  view.unmount()
  expect(signal.aborted).toBe(true)
  await act(async () => pending.resolve(json({})))
  expect(state.success).not.toHaveBeenCalled()
  expect(changed).not.toHaveBeenCalled()
})
