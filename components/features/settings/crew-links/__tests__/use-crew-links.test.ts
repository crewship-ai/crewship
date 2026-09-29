import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, waitFor, act } from "@testing-library/react"
import { toast } from "sonner"

import { useCrewLinks } from "../use-crew-links"

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "MANAGER" }) }))

const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })
const ENG = { id: "c-eng", name: "Engineering", slug: "engineering" }
const OPS = { id: "c-ops", name: "Ops", slug: "ops" }
const conn = (from: string, to: string, direction: string) => ({ id: `cc-${from}-${to}`, from_crew_id: from, to_crew_id: to, direction, status: "active", forward_file_access: "read_write", reverse_file_access: "read", access_version: 1 })
const writes = () => api.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method)

function serve(connections: unknown[], overrides: (url: string, init?: RequestInit) => unknown = () => undefined) {
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    const o = overrides(url, init)
    if (o) return o
    if (init?.method === "POST") return json({ id: "cc-new" }, 201)
    if (init?.method === "DELETE") return json(null, 204)
    if (url.startsWith("/api/v1/crew-connections")) return json(connections)
    if (url.startsWith("/api/v1/crews")) return json([ENG, OPS])
    return json(null, 404)
  })
}

async function mount() {
  const hook = renderHook(() => useCrewLinks("ws1"))
  await waitFor(() => expect(hook.result.current.loading).toBe(false))
  return hook
}

beforeEach(() => {
  api.mockReset()
  vi.mocked(toast.error).mockReset()
  vi.mocked(toast.success).mockReset()
})

// The write path the Crew links page shares across its three views — moved
// from the old settings card with its guarantees intact.
describe("useCrewLinks writes", () => {
  it("unlinks by deleting the stored row with its version, once confirmed", async () => {
    serve([conn("c-eng", "c-ops", "bidirectional")])
    vi.stubGlobal("confirm", vi.fn(() => true))
    const { result } = await mount()
    await act(() => result.current.setPair(ENG, OPS, "none"))
    expect(writes()).toHaveLength(1)
    expect(String(writes()[0][0])).toContain("/crew-connections/cc-c-eng-c-ops?workspace_id=ws1&expected_version=1")
  })

  it("does not unlink when the confirmation is declined", async () => {
    serve([conn("c-eng", "c-ops", "bidirectional")])
    vi.stubGlobal("confirm", vi.fn(() => false))
    const { result } = await mount()
    await act(() => result.current.setPair(ENG, OPS, "none"))
    expect(writes()).toHaveLength(0)
  })

  it("re-points a link stored the other way round, carrying each side's file level", async () => {
    serve([conn("c-ops", "c-eng", "unidirectional")])
    const { result } = await mount()
    await act(() => result.current.setPair(ENG, OPS, "out"))
    expect((writes()[0][1] as RequestInit).method).toBe("DELETE")
    expect(JSON.parse(String((writes()[1][1] as RequestInit).body))).toEqual({
      from_crew_id: "c-eng", to_crew_id: "c-ops", direction: "unidirectional",
      forward_file_access: "read", reverse_file_access: "read_write",
    })
  })

  it("stops after a refused delete instead of recreating from stale state", async () => {
    serve([conn("c-ops", "c-eng", "unidirectional")], (_u, init) => (init?.method === "DELETE" ? json({ detail: "Connection changed" }, 409) : undefined))
    const { result } = await mount()
    await act(() => result.current.setPair(ENG, OPS, "out"))
    expect(writes()).toHaveLength(1)
    expect(toast.error).toHaveBeenCalled()
  })

  it("puts the old link back and says why when the re-point is refused", async () => {
    let posts = 0
    serve([conn("c-ops", "c-eng", "unidirectional")], (_u, init) => {
      if (init?.method !== "POST") return undefined
      posts += 1
      return posts === 1 ? json({ detail: "target crew is archived" }, 409) : json({ id: "cc-restored" }, 201)
    })
    const { result } = await mount()
    await act(() => result.current.setPair(ENG, OPS, "out"))
    expect(toast.error).toHaveBeenCalledWith("target crew is archived")
  })

  it("warns the link is gone when even the restore fails", async () => {
    let posts = 0
    serve([conn("c-ops", "c-eng", "unidirectional")], (_u, init) => {
      if (init?.method !== "POST") return undefined
      posts += 1
      return posts === 1 ? json({ detail: "target crew is archived" }, 409) : json(null, 500)
    })
    const { result } = await mount()
    await act(() => result.current.setPair(ENG, OPS, "out"))
    const msg = String(vi.mocked(toast.error).mock.calls[0][0])
    expect(msg).toMatch(/could not be restored/i)
    expect(msg).toMatch(/re-create the link manually/i)
  })

  it("reports a failed load instead of an empty workspace", async () => {
    api.mockResolvedValue(json({}, 503))
    const { result } = await mount()
    expect(result.current.loadError).toBe(true)
  })
})
