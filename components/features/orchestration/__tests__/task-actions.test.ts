import { describe, it, expect, vi, beforeEach } from "vitest"

import { setTaskStatus, runTaskAction, type TaskRef } from "../task-actions"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}))

const ref: TaskRef = {
  crewId: "crew-1",
  missionId: "mis-1",
  taskId: "task-1",
  workspaceId: "ws-1",
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

describe("setTaskStatus", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
  })

  it("retry PATCHes the task back to PENDING and reports that it landed", async () => {
    apiFetch.mockResolvedValue(json(200, { id: "task-1", status: "PENDING" }))

    await expect(setTaskStatus("retry", ref)).resolves.toBe(true)

    expect(apiFetch).toHaveBeenCalledWith(
      "/api/v1/crews/crew-1/missions/mis-1/tasks/task-1?workspace_id=ws-1",
      expect.objectContaining({ method: "PATCH", body: JSON.stringify({ status: "PENDING" }) }),
    )
    expect(toastSuccess).toHaveBeenCalledWith("Task queued for retry")
    expect(toastError).not.toHaveBeenCalled()
  })

  it("skip PATCHes the task to SKIPPED and reports that it landed", async () => {
    apiFetch.mockResolvedValue(json(200, { id: "task-1", status: "SKIPPED" }))

    await expect(setTaskStatus("skip", ref)).resolves.toBe(true)

    expect(apiFetch).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ body: JSON.stringify({ status: "SKIPPED" }) }),
    )
    expect(toastSuccess).toHaveBeenCalledWith("Task skipped")
  })

  // The defect this module exists to close: apiFetch RESOLVES on 5xx, so a
  // bare `await` reads as success. An operator told "Task queued for retry"
  // stops watching a task that was never requeued.
  it("does not claim the retry was queued when the server refuses it", async () => {
    apiFetch.mockResolvedValue(json(500, { error: "task is owned by a run that is still executing" }))

    await expect(setTaskStatus("retry", ref)).resolves.toBe(false)

    expect(toastSuccess).not.toHaveBeenCalled()
    // The server's own words — it is the only thing that separates "this task
    // is busy" from "you may not touch this crew".
    expect(toastError).toHaveBeenCalledWith("task is owned by a run that is still executing")
  })

  it("does not claim the task was skipped when the server refuses it", async () => {
    apiFetch.mockResolvedValue(json(403, { detail: "you do not have write access to this crew" }))

    await expect(setTaskStatus("skip", ref)).resolves.toBe(false)

    expect(toastSuccess).not.toHaveBeenCalled()
    expect(toastError).toHaveBeenCalledWith("you do not have write access to this crew")
  })

  it("names the status when the refusal carries no readable body", async () => {
    // A gateway page, an empty 502, anything that is not our JSON envelope.
    apiFetch.mockResolvedValue(new Response("<html>502 Bad Gateway</html>", { status: 502 }))

    await expect(setTaskStatus("retry", ref)).resolves.toBe(false)

    expect(toastSuccess).not.toHaveBeenCalled()
    expect(toastError).toHaveBeenCalledWith(expect.stringContaining("502"))
  })

  it("does not claim success when the request never reaches the server", async () => {
    apiFetch.mockRejectedValue(new Error("Failed to fetch"))

    await expect(setTaskStatus("skip", ref)).resolves.toBe(false)

    expect(toastSuccess).not.toHaveBeenCalled()
    expect(toastError).toHaveBeenCalledWith("Failed to fetch")
  })
})

// The wiring the orchestration panel's task buttons actually go through. It
// is tested here and not by rendering the layout, because the layout is a
// thousand lines of JSX no unit test mounts — which is exactly how a mis-wire
// ("Retry" sending SKIPPED, a refresh after a refused write) stays invisible.
describe("runTaskAction", () => {
  const scope = {
    missions: [
      { id: "mis-1", crew_id: "crew-1" },
      { id: "mis-orphan", crew_id: null },
    ],
    workspaceId: "ws-1",
  }

  beforeEach(() => {
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
  })

  it("sends retry as PENDING against the crew that owns the mission", async () => {
    apiFetch.mockResolvedValue(json(200, { id: "task-1", status: "PENDING" }))

    await expect(runTaskAction("retry", "task-1", "mis-1", scope)).resolves.toBe(true)

    expect(apiFetch).toHaveBeenCalledWith(
      "/api/v1/crews/crew-1/missions/mis-1/tasks/task-1?workspace_id=ws-1",
      expect.objectContaining({ method: "PATCH", body: JSON.stringify({ status: "PENDING" }) }),
    )
  })

  it("sends skip as SKIPPED — the two buttons are not interchangeable", async () => {
    apiFetch.mockResolvedValue(json(200, { id: "task-1", status: "SKIPPED" }))

    await expect(runTaskAction("skip", "task-1", "mis-1", scope)).resolves.toBe(true)

    expect(apiFetch).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ body: JSON.stringify({ status: "SKIPPED" }) }),
    )
  })

  it("reports no refresh is due when the server refuses the write", async () => {
    apiFetch.mockResolvedValue(json(403, { error: "you do not have write access to this crew" }))

    await expect(runTaskAction("retry", "task-1", "mis-1", scope)).resolves.toBe(false)

    expect(toastSuccess).not.toHaveBeenCalled()
  })

  it("writes nothing for edit — it only reveals a panel already on screen", async () => {
    await expect(runTaskAction("edit", "task-1", "mis-1", scope)).resolves.toBe(false)

    expect(apiFetch).not.toHaveBeenCalled()
    expect(toastSuccess).not.toHaveBeenCalled()
    expect(toastError).not.toHaveBeenCalled()
  })

  it("writes nothing when the mission is not in the list", async () => {
    await expect(runTaskAction("retry", "task-1", "mis-gone", scope)).resolves.toBe(false)

    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("writes nothing when the mission carries no crew — never guess the crew id", async () => {
    await expect(runTaskAction("skip", "task-1", "mis-orphan", scope)).resolves.toBe(false)

    expect(apiFetch).not.toHaveBeenCalled()
  })
})
