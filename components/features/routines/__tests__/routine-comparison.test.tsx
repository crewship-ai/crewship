import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { RoutineComparison } from "../routine-comparison"
const h = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetch }))
const response = (raw: unknown) => ({ ok: true, json: async () => raw })
const version = { version: 3, is_head: true, definition_hash: "frozen" }
const record = (id: string, status = "completed") => ({
  id,
  workspace_id: "ws",
  pipeline_version: 3,
  definition_hash: "frozen",
  status,
  output: "ok",
  duration_ms: 100,
  cost_usd: 0.01,
})
async function prepare() {
  render(<RoutineComparison workspaceId="ws" slug="recipe" />)
  fireEvent.click(screen.getByRole("button", { name: "Load published versions" }))
  await waitFor(() => expect(screen.getByLabelText("Side A recipe version")).toHaveValue("3"))
  fireEvent.change(screen.getByLabelText("Comparison dataset"), {
    target: {
      value: '[{"id":"sample","inputs":{"count":0,"enabled":false},"expected_output":"ok"}]',
    },
  })
}
describe("live comparison", () => {
  it("pins both sides and preserves the same key after an uncertain start", async () => {
    const posts: { headers: Record<string, string>; body: string }[] = []
    h.fetch
      .mockReset()
      .mockImplementation(
        async (url: string, options?: { headers: Record<string, string>; body: string }) => {
          if (url.endsWith("versions")) return response([version])
          if (options) {
            posts.push(options)
            if (posts.length === 1) throw new Error("Connection lost")
            return response({ run_id: posts.length === 2 ? "a" : "b" })
          }
          return response(record(url.endsWith("/a") ? "a" : "b"))
        },
      )
    await prepare()
    fireEvent.click(screen.getByRole("button", { name: "Start live comparison" }))
    await screen.findByText("Connection lost")
    fireEvent.click(screen.getByRole("button", { name: "Continue / refresh accepted run" }))
    await waitFor(() => expect(screen.getAllByText("Exact match")).toHaveLength(2))
    const spec = JSON.parse(
      readFileSync(resolve(process.cwd(), "internal/api/openapi.gen.json"), "utf8"),
    )
    const reads = h.fetch.mock.calls.filter(
      ([url, options]) => !options && !url.endsWith("versions"),
    )
    expect(reads.length).toBeGreaterThan(0)
    for (const [url] of reads) {
      const route = url
        .replace("/workspaces/ws/", "/workspaces/{workspaceId}/")
        .replace(/\/[ab]$/, "/{runId}")
      expect(spec.paths[route]?.get, `GET ${url} must exist in the server contract`).toBeDefined()
    }
    expect(posts).toHaveLength(3)
    for (const post of posts) expect(post.headers.Prefer).toBe("respond-async")
    expect(posts[0].headers["Idempotency-Key"]).toBe(posts[1].headers["Idempotency-Key"])
    expect(posts[2].headers["Idempotency-Key"]).not.toBe(posts[1].headers["Idempotency-Key"])
    for (const post of posts)
      expect(JSON.parse(post.body)).toMatchObject({
        pinned_version: 3,
        inputs: { count: 0, enabled: false },
      })
  })
  it("refreshes a waiting accepted run without repeating its start", async () => {
    let postCount = 0
    let reads = 0
    h.fetch.mockReset().mockImplementation(async (url: string, options?: unknown) => {
      if (url.endsWith("versions")) return response([version])
      if (options) return response({ run_id: ++postCount === 1 ? "a" : "b" })
      reads++
      return response(record(url.endsWith("/a") ? "a" : "b", reads === 1 ? "waiting" : "completed"))
    })
    await prepare()
    fireEvent.click(screen.getByRole("button", { name: "Start live comparison" }))
    await screen.findByRole("button", { name: "Continue / refresh accepted run" })
    expect(postCount).toBe(1)
    expect(screen.getByText("Pending")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Continue / refresh accepted run" }))
    await waitFor(() => expect(screen.getAllByText("Exact match")).toHaveLength(2))
    expect(postCount).toBe(2)
  })
  it("stops at archive mismatch and leaves an accepted run accessible", async () => {
    h.fetch
      .mockReset()
      .mockResolvedValueOnce(response([version]))
      .mockResolvedValueOnce(response({ run_id: "a" }))
      .mockResolvedValueOnce(response({ ...record("a"), definition_hash: "changed" }))
    await prepare()
    fireEvent.click(screen.getByRole("button", { name: "Start live comparison" }))
    await screen.findByText(/recorded recipe does not match/)
    expect(h.fetch).toHaveBeenCalledTimes(3)
    expect(screen.getByRole("link", { name: "Open run" })).toHaveAttribute(
      "href",
      "/routines?run=a",
    )
    fireEvent.click(screen.getByRole("button", { name: "Stop remaining comparison" }))
    expect(screen.getByLabelText("Comparison dataset")).not.toBeDisabled()
  })
})
