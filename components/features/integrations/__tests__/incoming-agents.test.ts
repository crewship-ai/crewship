import { describe, it, expect, vi, beforeEach } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { incomingAgents } from "../use-incoming-endpoints"
vi.mock("@/lib/api-fetch", async original => ({...await original<typeof import("@/lib/api-fetch")>(), apiFetch: vi.fn()}))
beforeEach(() => vi.clearAllMocks())
describe("incoming agent catalog", () => {
  it("follows catalog pagination so configured agents after the first page are counted", async () => {
    vi.mocked(apiFetch)
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify([{ id: "first", webhook_secret_set: false }]),
          { headers: { "X-Total-Count": "2" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify([{ id: "configured", webhook_secret_set: true }]),
          { headers: { "X-Total-Count": "2" } },
        ),
      )
    expect((await incomingAgents("ws")).map((a) => a.id)).toEqual([
      "first",
      "configured",
    ])
    expect(vi.mocked(apiFetch).mock.calls[1][0]).toContain("offset=1")
  })
  it("does not interpret an omitted configuration flag as an unconfigured endpoint", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(
      new Response(JSON.stringify([{ id: "unknown" }])),
    )
    await expect(incomingAgents("ws")).rejects.toThrow(
      "configuration is unavailable",
    )
  })
})
