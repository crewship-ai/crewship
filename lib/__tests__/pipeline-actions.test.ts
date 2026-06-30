import { describe, it, expect } from "vitest"
import { buildPipelineActionRequest } from "@/lib/pipeline-actions"

const ws = "ws_1"
const slug = "csv-to-json"
const routine = {
  slug,
  definition: { steps: [{ kind: "transform", id: "x" }] },
}

describe("buildPipelineActionRequest", () => {
  it("run → slug path with inputs body", () => {
    const req = buildPipelineActionRequest(ws, slug, "run", routine)
    expect(req.url).toBe(`/api/v1/workspaces/${ws}/pipelines/${slug}/run`)
    expect(req.body).toEqual({ inputs: {} })
  })

  it("dry_run → slug path with inputs body", () => {
    const req = buildPipelineActionRequest(ws, slug, "dry_run", routine)
    expect(req.url).toBe(`/api/v1/workspaces/${ws}/pipelines/${slug}/dry_run`)
    expect(req.body).toEqual({ inputs: {} })
  })
})
