import { afterEach, expect, it, vi } from "vitest"
import { waitpointDecide } from "../api/waitpoints"

afterEach(() => vi.unstubAllGlobals())

it("T2 sends the flat decision accepted by the Go handler", async () => {
  const fetch = vi.fn().mockResolvedValue({ ok: true })
  vi.stubGlobal("fetch", fetch)
  expect(
    await waitpointDecide("ws", "tok", true, {
      action_id: "go",
      data: { count: 0, enabled: false },
    }),
  ).toEqual({ ok: true })
  expect(fetch).toHaveBeenCalledWith(
    "/api/v1/workspaces/ws/pipelines/waitpoints/tok/approve",
    expect.objectContaining({
      method: "POST",
      body: '{"approved":true,"action_id":"go","data":{"count":0,"enabled":false}}',
    }),
  )
})
