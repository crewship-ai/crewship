import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"

const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetchMock(...args),
}))

import { useStepIO } from "@/hooks/use-step-io"

function okJson(body: unknown): Response {
  return { ok: true, json: async () => body } as unknown as Response
}

describe("useStepIO (#863)", () => {
  beforeEach(() => apiFetchMock.mockReset())

  it("fetches GetRun scoped to the opened step and returns its spans", async () => {
    apiFetchMock.mockResolvedValue(
      okJson({ sub_spans: { "step-1": [{ kind: "bash", output: "hi" }] } }),
    )
    const { result } = renderHook(() => useStepIO("ws-1", "run-1", "step-1"))

    await waitFor(() => expect(result.current.spans).toBeDefined())
    expect(result.current.spans).toEqual([{ kind: "bash", output: "hi" }])

    const url = apiFetchMock.mock.calls[0][0] as string
    expect(url).toContain("/pipeline-runs/run-1")
    expect(url).toContain("io_step=step-1")
  })

  it("does not fetch when no step is selected", () => {
    renderHook(() => useStepIO("ws-1", "run-1", null))
    expect(apiFetchMock).not.toHaveBeenCalled()
  })

  it("yields undefined (fallback) when the step has no spans in the response", async () => {
    apiFetchMock.mockResolvedValue(okJson({ sub_spans: {} }))
    const { result } = renderHook(() => useStepIO("ws-1", "run-1", "ghost"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.spans).toBeUndefined()
  })
})
