import { describe, it, expect, vi } from "vitest"

import { bulkRunNow, bulkTurnOff, bulkSummary, requiredInputsWithoutDefault } from "@/lib/routine-bulk"

// Bulk actions of the Routines panel (PNL-03, routines.md R01): Turn off is
// POST …/pipelines/{slug}/disable one by one (Owner/Admin on the server);
// Run now starts only routines that need no answer from a person — a
// required input without a default is skipped and named.

const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body, text: async () => JSON.stringify(body) }) as unknown as Response

function fetcher(routes: Record<string, (init?: RequestInit) => Response>) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${url}`
    const handler = routes[key]
    if (!handler) throw new Error(`unexpected ${key}`)
    return handler(init)
  })
}

const base = "/api/v1/workspaces/ws%201/pipelines"

describe("requiredInputsWithoutDefault", () => {
  it.each([
    [{ inputs: [{ name: "month", required: true }] }, ["month"]],
    [{ inputs: [{ name: "month", required: true, default: "2026-10" }] }, []],
    [{ inputs: [{ name: "note" }] }, []],
    [{}, []],
    [null, []],
  ])("%j → %j", (definition, names) => expect(requiredInputsWithoutDefault(definition as Record<string, unknown> | null)).toEqual(names))
})

describe("bulkRunNow", () => {
  it("starts the routines that need no answer and skips the ones that do, by name", async () => {
    const f = fetcher({
      [`GET ${base}/weekly`]: () => json({ slug: "weekly", definition: { inputs: [] } }),
      [`GET ${base}/billing`]: () => json({ slug: "billing", definition: { inputs: [{ name: "month", required: true }] } }),
      [`POST ${base}/weekly/run`]: () => json({ run_id: "run_1" }, 202),
    })
    const result = await bulkRunNow("ws 1", ["weekly", "billing"], f)
    expect(result).toEqual({ started: ["weekly"], skipped: [{ slug: "billing", reason: "needs month" }], failed: [] })
    const run = f.mock.calls.find(([url]) => url === `${base}/weekly/run`)!
    expect(JSON.parse(String(run[1]!.body))).toEqual({ inputs: {} })
    expect((run[1]!.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/\S+/)
    expect(f.mock.calls.some(([url]) => url === `${base}/billing/run`)).toBe(false)
  })

  it("reports a refused start without stopping the rest", async () => {
    const f = fetcher({
      [`GET ${base}/a`]: () => json({ definition: {} }),
      [`GET ${base}/b`]: () => json({ definition: {} }),
      [`POST ${base}/a/run`]: () => json({ error: "concurrency limit reached" }, 429),
      [`POST ${base}/b/run`]: () => json({ run_id: "r" }, 202),
    })
    const result = await bulkRunNow("ws 1", ["a", "b"], f)
    expect(result.started).toEqual(["b"])
    expect(result.failed).toEqual([{ slug: "a", reason: "concurrency limit reached" }])
  })
})

describe("bulkTurnOff", () => {
  it("posts disable for each routine and reports the refused ones", async () => {
    const f = fetcher({
      [`POST ${base}/a/disable`]: () => json({}, 200),
      [`POST ${base}/b/disable`]: () => json({ error: "Forbidden" }, 403),
    })
    const result = await bulkTurnOff("ws 1", ["a", "b"], f)
    expect(result).toEqual({ done: ["a"], failed: [{ slug: "b", reason: "Forbidden" }] })
  })
})

describe("bulkSummary", () => {
  it.each([
    [{ started: ["a", "b"], skipped: [], failed: [] }, "Started 2 runs"],
    [{ started: ["a"], skipped: [{ slug: "x", reason: "needs month" }], failed: [] }, "Started 1 run · skipped 1 that needs an answer"],
    [{ started: [], skipped: [], failed: [{ slug: "x", reason: "429" }] }, "Started 0 runs · 1 refused"],
  ])("%j → %s", (r, text) => expect(bulkSummary(r)).toBe(text))
})
