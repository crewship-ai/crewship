import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, expect, it, vi } from "vitest"
import type { Pipeline } from "../use-pipelines"
import { deriveRoutinePurpose, useRoutinePurposes } from "../use-routine-purposes"

const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))
const routine = (slug: string, description = "", hash = "v1") =>
  ({ slug, description, definition_hash: hash }) as Pipeline
const response = (name: string, hash = "v1") =>
  new Response(
    JSON.stringify({
      definition_hash: hash,
      definition: {
        steps: [{ id: "technical_id", name, type: "transform" }],
      },
    }),
  )
beforeEach(() => { fetch.mockReset() })

it("loads only missing descriptions and reuses summaries across list polling", async () => {
  fetch.mockResolvedValue(response("Prepare report"))
  const rows = [routine("described", "Send a weekly report"), routine("missing")]
  const view = renderHook(({ rows }) => useRoutinePurposes("ws", rows), {
    initialProps: { rows },
  })
  await waitFor(() => expect(view.result.current[1].description).toBe("Prepare report."))
  expect(view.result.current[0].description).toBe("Send a weekly report")
  view.rerender({ rows: rows.map((row) => ({ ...row, invocation_count: 7 })) })
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/workspaces/ws/pipelines/missing")
})

it("aborts obsolete workspace reads and ignores their late results", async () => {
  let finish!: (value: Response) => void
  fetch.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve
      }),
  )
  fetch.mockResolvedValueOnce(response("Current workspace"))
  const rows = [routine("same-slug")]
  const view = renderHook(({ ws }) => useRoutinePurposes(ws, rows), {
    initialProps: { ws: "old" },
  })
  const signal = fetch.mock.calls[0][1].signal as AbortSignal
  view.rerender({ ws: "new" })
  expect(signal.aborted).toBe(true)
  await waitFor(() => expect(view.result.current[0].description).toBe("Current workspace."))
  await act(async () => finish(response("Obsolete workspace")))
  expect(view.result.current[0].description).toBe("Current workspace.")
})

it("reloads changed recipes and reports failed reads without inventing a purpose", async () => {
  fetch.mockResolvedValueOnce(response("Original purpose"))
  const view = renderHook(({ rows }) => useRoutinePurposes("ws", rows), {
    initialProps: { rows: [routine("recipe")] },
  })
  await waitFor(() => expect(view.result.current[0].description).toBe("Original purpose."))
  fetch.mockResolvedValueOnce(new Response("unavailable", { status: 503 }))
  view.rerender({ rows: [routine("recipe", "", "v2")] })
  await waitFor(() => expect(view.result.current[0].description).toBe("Purpose unavailable."))
  expect(fetch).toHaveBeenCalledTimes(2)
})

it("derives only from actual actions and distinguishes absent from empty definitions", () => {
  expect(deriveRoutinePurpose(null)).toBe("Purpose unavailable.")
  expect(deriveRoutinePurpose({ steps: [] })).toBe("No steps configured.")
  expect(
    deriveRoutinePurpose({
      steps: [
        { id: "a", type: "agent_run", agent_slug: "sam" },
        { id: "b", type: "wait", wait: { kind: "approval" } },
      ],
    }),
  ).toBe("Ask sam. Wait for approval.")
})

it("bounds concurrent reads while eventually enriching every missing purpose", async () => {
  const pending: ((r: Response) => void)[] = []
  fetch.mockImplementation(() => new Promise<Response>((resolve) => pending.push(resolve)))
  const rows = Array.from({ length: 9 }, (_, i) => routine(`missing-${i}`))
  const view = renderHook(() => useRoutinePurposes("ws", rows))
  expect(fetch).toHaveBeenCalledTimes(4)
  for (let i = 0; i < rows.length; i++) {
    await act(async () => {
      pending[i](response(`Purpose ${i}`))
    })
    expect(fetch.mock.calls.length).toBeLessThanOrEqual(Math.min(i + 5, rows.length))
  }
  await waitFor(() =>
    expect(view.result.current.map((r) => r.description)).toEqual(
      rows.map((_, i) => `Purpose ${i}.`),
    ),
  )
})
