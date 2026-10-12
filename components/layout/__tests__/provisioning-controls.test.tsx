import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ProvisioningCrewState, ProvisioningStatus } from "@/hooks/use-provisioning-status"
const notices = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock("sonner", () => ({ toast: notices }))
vi.mock("@/lib/api-fetch", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/api-fetch")>(), apiFetch: vi.fn() }))
import { apiFetch } from "@/lib/api-fetch"
import { ProvisioningBadge, ProvisioningRow, ProvisioningChecklist, ProvisioningBuildLog, RecentBuildSummary } from "../app-toolbar-provisioning"

const ok = (body: unknown = {}) => new Response(JSON.stringify(body))
function crew(overrides: Partial<ProvisioningCrewState> = {}): ProvisioningCrewState {
  return { id: "crew-one", slug: "crew-one", name: "Quality", status: "needs_provision", featureIds: [], ...overrides }
}
function summary(overrides: Partial<ProvisioningStatus> = {}): ProvisioningStatus {
  return { total: 1, building: 0, failed: 0, needsProvision: 0, pendingRestart: 0, recentlyCompleted: 0, detail: [], acknowledge: vi.fn(), ...overrides }
}
const navigate = vi.fn()
const rowProps = { workspaceId: "workspace & one", onNavigate: navigate }
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (value: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
beforeEach(() => { vi.mocked(apiFetch).mockReset().mockResolvedValue(ok()); notices.success.mockReset(); notices.error.mockReset(); navigate.mockReset() })
afterEach(() => { cleanup(); vi.useRealTimers() })

describe("Provisioning toolbar controls", () => {
  it("hides an empty badge", () => {
    const view = render(<ProvisioningBadge workspaceId="ws" provisioning={summary({ total: 0 })} />)
    expect(view.container).toBeEmptyDOMElement()
  })
  it.each([
    [{ failed: 1 }, "1 build failed"], [{ failed: 2, building: 1 }, "2 builds failed"],
    [{ building: 1 }, "Building 1…"], [{ needsProvision: 1 }, "1 needs rebuild"], [{ needsProvision: 2 }, "2 need rebuild"],
    [{ pendingRestart: 1 }, "1 needs restart"], [{ pendingRestart: 2 }, "2 need restart"], [{ recentlyCompleted: 1 }, "Built 1"],
  ] satisfies [Partial<ProvisioningStatus>, string][])("labels attention state %j", (state, label) => {
    render(<ProvisioningBadge workspaceId="ws" provisioning={summary(state)} />)
    expect(screen.getByRole("button", { name: `Crew images: ${label}` })).toBeInTheDocument()
  })
  it("shows only actionable or recent crews and wires dismiss and navigation", async () => {
    const state = summary({ failed: 1, detail: [
      crew({ name: "Failed crew", status: "failed" }),
      crew({ id: "running", name: "Running crew", status: "running" }),
      crew({ id: "pending", name: "Pending crew" }),
      crew({ id: "restart", name: "Restart crew", status: "completed", agentsPendingRestart: 1 }),
      crew({ id: "recent", name: "Recent crew", status: "idle", recent: { outcome: "completed", at: Date.now(), stepCount: 0, features: [] } }),
      crew({ id: "ack", name: "Acknowledged", status: "failed", acknowledged: true }),
      crew({ id: "idle", name: "Idle crew", status: "idle" }),
      crew({ id: "complete", name: "Clean crew", status: "completed" }),
    ] })
    render(<ProvisioningBadge workspaceId="ws" provisioning={state} />)
    fireEvent.click(screen.getByRole("button", { name: /Crew images/ }))
    expect(await screen.findByText("Container builds")).toBeInTheDocument()
    for (const name of ["Failed crew", "Running crew", "Pending crew", "Restart crew", "Recent crew"]) expect(screen.getByRole("link", { name })).toBeInTheDocument()
    for (const name of ["Acknowledged", "Idle crew", "Clean crew"]) expect(screen.queryByText(name)).not.toBeInTheDocument()
    fireEvent.click(screen.getAllByRole("button", { name: "Dismiss" })[0]); expect(state.acknowledge).toHaveBeenCalledWith("crew-one")
    fireEvent.click(screen.getByRole("link", { name: "Pending crew" }))
    await waitFor(() => expect(screen.queryByText("Container builds")).not.toBeInTheDocument())
  })

  it("presents live feature events ahead of the coarse plan and keeps logs expandable", () => {
    const c = crew({ status: "running", activeFeature: "python", steps: ["Hidden plan"], eventSteps: [
      { key: "done", label: "Fetched", status: "completed", durationMs: 300 },
      { key: "active", label: "Installing", status: "started", reason: "concurrency" },
      { key: "failed", label: "Failed install", status: "failed", durationMs: 2000 },
    ], logTail: ["line one", "line two"] })
    render(<ProvisioningRow {...rowProps} crew={c} />)
    expect(screen.getByText("python")).toBeInTheDocument()
    expect(screen.queryByText("Hidden plan")).not.toBeInTheDocument()
    expect(screen.getAllByRole("listitem").slice(1).map(el => el.textContent)).toEqual([expect.stringContaining("Failed install"), expect.stringContaining("Installing"), expect.stringContaining("Fetched")])
    expect(screen.getByText(/line one/)).toHaveTextContent("line one line two")
  })
  it("orders coarse progress using the reported message and falls back to the numeric index", () => {
    const view = render(<ProvisioningChecklist steps={["first", "second", "third", "fourth"]} active={1} message="third" />)
    expect(screen.getAllByRole("listitem").map(el => el.textContent)).toEqual(["third", "second", "first", "fourth"])
    view.rerender(<ProvisioningChecklist steps={["first", "second"]} active={2} message="unknown" />)
    expect(screen.getAllByRole("listitem").map(el => el.textContent)).toEqual(["second", "first"])
    view.rerender(<ProvisioningChecklist steps={["first", "second"]} active={0} />)
    expect(screen.getAllByRole("listitem").map(el => el.textContent)).toEqual(["first", "second"])
  })
  it.each([
    { steps: ["Resolve", "Build"], step: 1 },
    { total: 3 },
    { total: 3, step: 2, message: "Installing packages" },
  ])("presents legacy progress %j", progress => {
    render(<ProvisioningRow {...rowProps} crew={crew({ status: "running", ...progress })} />)
    if ("steps" in progress) expect(screen.getByText("Resolve")).toBeInTheDocument()
    else expect(screen.getByText(progress.message ?? "Building image…")).toBeInTheDocument()
  })
  it("shows failure context and recent fallback log tails without hiding the retry", () => {
    render(<ProvisioningRow {...rowProps} crew={crew({ status: "failed", error: "x".repeat(300), recent: { outcome: "failed", at: Date.now(), stepCount: 0, features: [], failedStep: "Install", buildLogTail: ["build error"] }, featureIds: ["ghcr.io/devcontainers/features/python:1"], resolvedFeatures: { "ghcr.io/devcontainers/features/python:1": { pinned: true, version: "1.2" } } })} />)
    expect(screen.getByText("Install")).toBeInTheDocument()
    expect(screen.getByText("x".repeat(240))).toBeInTheDocument()
    expect(screen.getByText("build error")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Retry" })).toBeEnabled()
    expect(screen.getByTestId("feature-chip")).toHaveTextContent("python")
  })
  it("keeps log expansion stable across streamed updates", () => {
    const view = render(<ProvisioningBuildLog lines={["first"]} />)
    const details = screen.getByText("build log").closest("details")!
    details.open = true; fireEvent(details, new Event("toggle", { bubbles: true }))
    view.rerender(<ProvisioningBuildLog lines={["first", "second"]} />)
    expect(details.open).toBe(true)
    expect(screen.getByText(/first/)).toHaveTextContent("first second")
    details.open = false; fireEvent(details, new Event("toggle", { bubbles: true })); expect(details.open).toBe(false)
  })
  it.each([[35_000, "35s ago", 300, "300ms"], [120_000, "2m ago", 2500, "2.5s"], [7_200_000, "2h ago", 15_000, "15s"], [0, "0s ago", 120_000, "2m"]])("renders bounded recent build details for %d ms", (age, ago, durationMs, duration) => {
    render(<RecentBuildSummary recent={{ outcome: "completed", at: Date.now() - age, stepCount: age === 0 ? 1 : 5, features: ["a", "b", "c", "d", "e"], durationMs }} />)
    expect(screen.getByText(`built ${ago}`)).toBeInTheDocument()
    expect(screen.getByText(duration)).toBeInTheDocument()
    expect(screen.getByText("+1")).toBeInTheDocument()
    expect(screen.queryByText("e", { exact: true })).not.toBeInTheDocument()
  })

  it.each(["build", "restart"])("runs %s only with workspace context and prevents duplicate clicks", async action => {
    const c = crew(action === "restart" ? { status: "completed", agentsPendingRestart: 1 } : {})
    const pending = deferred<Response>(); vi.mocked(apiFetch).mockReturnValue(pending.promise)
    const view = render(<ProvisioningRow {...rowProps} workspaceId={null} crew={c} />)
    const label = action === "restart" ? "Restart agents" : "Build now"
    expect(screen.getByRole("button", { name: label })).toBeDisabled()
    view.rerender(<ProvisioningRow {...rowProps} crew={c} />)
    fireEvent.click(screen.getByRole("button", { name: label }))
    expect(screen.getByRole("button", { name: "Starting…" })).toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Starting…" })); expect(apiFetch).toHaveBeenCalledTimes(1)
    const [url, init] = vi.mocked(apiFetch).mock.calls[0]
    expect(url).toBe(`/api/v1/crews/crew-one/${action === "restart" ? "restart-agents" : "provision"}?workspace_id=workspace%20%26%20one`)
    expect(init?.method).toBe("POST")
    await act(async () => pending.resolve(ok({ restarted: 1 })))
    expect(notices.success).toHaveBeenCalledWith(action === "restart" ? "1 agent restarted in Quality" : "Building Quality…")
    expect(screen.getByRole("button", { name: label })).toBeEnabled()
  })
  it.each(["build", "restart"])("keeps %s retryable after refusals and both kinds of transport errors", async action => {
    const c = crew(action === "restart" ? { status: "completed", agentsPendingRestart: 2 } : { status: "failed" })
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response("refused", { status: 403 })).mockRejectedValueOnce(new Error("offline")).mockRejectedValueOnce("disconnected")
    render(<ProvisioningRow {...rowProps} crew={c} />)
    const label = action === "restart" ? "Restart agents" : "Retry"
    for (const text of ["refused", "offline", "disconnected"]) {
      fireEvent.click(screen.getByRole("button", { name: label }))
      await waitFor(() => expect(notices.error).toHaveBeenLastCalledWith(expect.stringContaining(text)))
      expect(screen.getByRole("button", { name: label })).toBeEnabled()
    }
  })
  it.each(["build", "restart"])("ignores abandoned %s completion after the workspace changes", async action => {
    const c = crew(action === "restart" ? { status: "completed", agentsPendingRestart: 1 } : {})
    const pending = deferred<Response>(); vi.mocked(apiFetch).mockReturnValue(pending.promise)
    const view = render(<ProvisioningRow {...rowProps} crew={c} />)
    const label = action === "restart" ? "Restart agents" : "Build now"
    fireEvent.click(screen.getByRole("button", { name: label }))
    view.rerender(<ProvisioningRow {...rowProps} workspaceId="workspace-two" crew={c} />)
    expect(screen.getByRole("button", { name: label })).toBeEnabled()
    await act(async () => pending.resolve(ok({ restarted: 1 })))
    expect(notices.success).not.toHaveBeenCalled()
    expect(notices.error).not.toHaveBeenCalled()
  })
  it("handles unreadable restart counts without leaving the action busy", async () => {
    vi.mocked(apiFetch).mockResolvedValue(new Response("{"))
    render(<ProvisioningRow {...rowProps} crew={crew({ status: "completed", agentsPendingRestart: 2 })} />)
    fireEvent.click(screen.getByRole("button", { name: "Restart agents" }))
    await waitFor(() => expect(notices.success).toHaveBeenCalledWith("0 agents restarted in Quality"))
    expect(screen.getByRole("button", { name: "Restart agents" })).toBeEnabled()
  })
  it.each(["build body", "restart body", "transport"])("late %s cannot report or unlock the current selection", async stage => {
    const pending = deferred<unknown>()
    const newer = deferred<Response>()
    const restarting = stage === "restart body"
    const oldCrew = crew(restarting ? { status: "completed", agentsPendingRestart: 1 } : {})
    if (stage === "transport") vi.mocked(apiFetch).mockImplementationOnce(() => pending.promise as Promise<Response>)
    else vi.mocked(apiFetch).mockResolvedValueOnce({ ok: restarting, text: () => pending.promise, json: () => pending.promise } as Response)
    vi.mocked(apiFetch).mockReturnValueOnce(newer.promise)
    const view = render(<ProvisioningRow {...rowProps} crew={oldCrew} />)
    const label = restarting ? "Restart agents" : "Build now"
    fireEvent.click(screen.getByRole("button", { name: label }))
    await act(async () => {})
    const oldSignal = vi.mocked(apiFetch).mock.calls[0][1]?.signal
    view.rerender(<ProvisioningRow {...rowProps} crew={{ ...oldCrew, id: "crew/two", name: "New crew" }} />)
    expect(oldSignal?.aborted).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: label }))
    expect(String(vi.mocked(apiFetch).mock.calls[1][0])).toContain("/crews/crew%2Ftwo/")
    await act(async () => { if (stage === "transport") pending.reject(new Error("late transport")); else pending.resolve(restarting ? { restarted: 9 } : "late refusal") })
    expect(notices.success).not.toHaveBeenCalled(); expect(notices.error).not.toHaveBeenCalled()
    expect(screen.getByRole("button", { name: "Starting…" })).toBeDisabled()
    await act(async () => newer.resolve(ok({ restarted: 2 })))
    expect(notices.success).toHaveBeenCalledWith(restarting ? "2 agents restarted in New crew" : "Building New crew…")
  })

})
