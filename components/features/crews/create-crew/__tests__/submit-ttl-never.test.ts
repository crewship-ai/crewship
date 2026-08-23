// =============================================================================
// Auto-stop "Never" — payload regression test
//
// The wizard's TTL chip row offers { label: "Never", value: null } and null is
// also INITIAL_STATE.ttlHours. The server (internal/api/crews_create.go, and
// the same rule on PATCH in crews_update.go) reads the two shapes differently:
//
//   container_ttl_hours ABSENT  -> NULL column -> "never configured"
//                                  -> resolveCrewContainerTTLHours() returns
//                                     defaultCrewContainerTTLHours (4 h)
//   container_ttl_hours = 0     -> stored 0    -> never stop (reaper skips it)
//
// So "Never" MUST travel as an explicit 0. Omitting it hands the user the very
// four-hour auto-stop the chip promised to avoid — the dashboard half of #1662,
// which was fixed on the CLI side only (cmd/crewship/cmd_crew_manage.go sends
// the value whenever --ttl was changed, so `--ttl 0` reaches the server).
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { submitCrew } from "../submit"
import { INITIAL_STATE, TTL_PRESETS, type WizardState } from "../types"

vi.mock("sonner", () => ({
  toast: { warning: vi.fn(), error: vi.fn(), success: vi.fn(), info: vi.fn() },
}))

interface MockCall {
  url: string
  method: string
  body: Record<string, unknown> | undefined
}

function setupFetchMock() {
  const calls: MockCall[] = []
  const responses: Array<{ ok: boolean; status: number; body: unknown }> = []

  const fetchMock = vi.fn(async (url: string | URL, init?: RequestInit) => {
    const u = typeof url === "string" ? url : url.toString()
    let parsedBody: Record<string, unknown> | undefined
    if (init?.body && typeof init.body === "string") {
      try { parsedBody = JSON.parse(init.body) } catch { /* leave undefined */ }
    }
    calls.push({ url: u, method: init?.method ?? "GET", body: parsedBody })
    const r = responses.shift() ?? { ok: true, status: 200, body: {} }
    return {
      ok: r.ok,
      status: r.status,
      json: async () => r.body,
      text: async () => (typeof r.body === "string" ? r.body : JSON.stringify(r.body)),
    } as Response
  })

  vi.stubGlobal("fetch", fetchMock)
  return {
    calls,
    queueResponse: (r: { ok: boolean; status?: number; body: unknown }) => {
      responses.push({ ok: r.ok, status: r.status ?? 200, body: r.body })
    },
  }
}

const WS = "ws_123"

function baseState(overrides: Partial<WizardState> = {}): WizardState {
  return {
    ...INITIAL_STATE,
    name: "Engineering",
    slug: "engineering",
    ...overrides,
  }
}

/** The value the "Never" chip actually writes into wizard state. Read from
 *  TTL_PRESETS rather than hardcoded so relabelling the chip can't make this
 *  test pass by testing a different chip. */
const NEVER_CHIP = TTL_PRESETS.find((p) => p.label === "Never")

describe("auto-stop 'Never' → container_ttl_hours: 0", () => {
  let fetcher: ReturnType<typeof setupFetchMock>

  beforeEach(() => { fetcher = setupFetchMock() })
  afterEach(() => { vi.unstubAllGlobals() })

  it("the 'Never' chip exists and is the wizard's initial TTL state", () => {
    expect(NEVER_CHIP, "TTL_PRESETS no longer has a chip labelled 'Never'").toBeDefined()
    expect(INITIAL_STATE.ttlHours).toBe(NEVER_CHIP!.value)
  })

  it("blank-crew POST sends an explicit 0, never an absent field", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, baseState({ mode: "empty", ttlHours: NEVER_CHIP!.value }))

    const body = fetcher.calls[0].body!
    expect(
      body,
      "an absent container_ttl_hours is read by crews_create.go as 'never configured' and resolves to the 4 h default — the opposite of what the chip promises",
    ).toHaveProperty("container_ttl_hours")
    expect(body.container_ttl_hours).toBe(0)
  })

  it("template-deploy PATCH sends an explicit 0 too (same server rule on update)", async () => {
    fetcher.queueResponse({ ok: true, body: { crew_id: "c1", crew_name: "Engineering", crew_slug: "engineering" } })
    fetcher.queueResponse({ ok: true, body: {} })

    await submitCrew(WS, baseState({
      mode: "browse",
      pickedTemplateSlug: "eng-squad",
      ttlHours: NEVER_CHIP!.value,
    }))

    const patch = fetcher.calls.find((c) => c.method === "PATCH")
    expect(patch, "template path never issued the override PATCH").toBeDefined()
    expect(patch!.body).toHaveProperty("container_ttl_hours")
    expect(patch!.body!.container_ttl_hours).toBe(0)
  })

  it("stays inside the server's bounds: 0 is the floor, negatives are a 400", async () => {
    // crews_create.go:297 and crews_update.go:207 both reject
    // container_ttl_hours < 0 with 400. There is no upper bound. So the
    // never-stop sentinel is exactly 0 — nothing below it is sendable.
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, baseState({ mode: "empty", ttlHours: NEVER_CHIP!.value }))

    const ttl = fetcher.calls[0].body!.container_ttl_hours as number
    expect(ttl).toBeGreaterThanOrEqual(0)
  })

  it("a real TTL is still sent verbatim (the fix must not flatten 1/4/24 h)", async () => {
    for (const preset of TTL_PRESETS.filter((p) => p.value !== null)) {
      const f = setupFetchMock()
      f.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })
      await submitCrew(WS, baseState({ mode: "empty", ttlHours: preset.value }))
      expect(f.calls[0].body!.container_ttl_hours, `chip "${preset.label}"`).toBe(preset.value)
      vi.unstubAllGlobals()
    }
  })
})
