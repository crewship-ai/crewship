import { describe, it, expect } from "vitest"
import { buildPipelineActionRequest, canTestRun } from "@/lib/pipeline-actions"

const ws = "ws_1"
const slug = "csv-to-json"
const routine = {
  slug,
  definition: { steps: [{ kind: "transform", id: "x" }] },
  author_crew_id: "crew_9",
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

  it("test_run → SLUGLESS path (regression: 404 bug) with definition + author_crew_id", () => {
    const req = buildPipelineActionRequest(ws, slug, "test_run", routine)
    // The backend registers POST /pipelines/test_run WITHOUT the {slug}.
    expect(req.url).toBe(`/api/v1/workspaces/${ws}/pipelines/test_run`)
    expect(req.url).not.toContain(slug)
    expect(req.body).toEqual({
      definition: routine.definition,
      author_crew_id: "crew_9",
      sample_inputs: {},
    })
  })
})

describe("canTestRun", () => {
  it("requires an author crew (backend rejects empty author_crew_id)", () => {
    expect(canTestRun(routine)).toBe(true)
    expect(canTestRun({ ...routine, author_crew_id: undefined })).toBe(false)
    expect(canTestRun({ ...routine, author_crew_id: "" })).toBe(false)
    expect(canTestRun(null)).toBe(false)
  })
})
