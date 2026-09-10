import { describe, expect, it, vi } from "vitest"
import { loadRoutineDraft, saveRoutineDraft } from "../routine-drafts"
import { apiFetch } from "../api-fetch"
vi.mock("../api-fetch", () => ({ apiFetch: vi.fn() }))
describe("draft HTTP errors", () => {
  it.each(["load", "save"])("%s explains a non-JSON proxy failure", async (operation) => {
    vi.mocked(apiFetch).mockResolvedValue(new Response("<html>Bad gateway</html>", { status: 502 }))
    const result =
      operation === "load"
        ? loadRoutineDraft("ws", "recipe")
        : saveRoutineDraft(
            "ws",
            {
              id: "d",
              slug: "recipe",
              revision: 1,
              base_pipeline_id: "",
              base_revision: 0,
              document: {},
            },
            {},
          )
    await expect(result).rejects.toThrow("Could not save the routine draft")
  })
})
