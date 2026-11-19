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
  it("unlink uses the shared confirmation, never window.confirm", async () => {
    serve([conn("c-eng", "c-ops", "bidirectional")])
    const confirm = vi.fn(() => true)
    vi.stubGlobal("confirm", confirm)
    const { result } = await mount()
    await act(() => result.current.setPair(ENG, OPS, "none"))
    expect(confirm).not.toHaveBeenCalled()
    expect(writes()).toHaveLength(0)
    expect(result.current.pendingUnlink).toEqual({ self: ENG, other: OPS })
  })

  it("unlinks by deleting the stored row with its version, once confirmed", async () => {
    serve([conn("c-eng", "c-ops", "bidirectional")])
    const { result } = await mount()
    await act(() => result.current.setPair(ENG, OPS, "none"))
    await act(() => result.current.confirmUnlink())
    expect(writes()).toHaveLength(1)
    expect(String(writes()[0][0])).toContain("/crew-connections/cc-c-eng-c-ops?workspace_id=ws1&expected_version=1")
    expect(result.current.pendingUnlink).toBeNull()
  })

  it("does not unlink when the confirmation is cancelled", async () => {
    serve([conn("c-eng", "c-ops", "bidirectional")])
    const { result } = await mount()
    await act(() => result.current.setPair(ENG, OPS, "none"))
    act(() => result.current.cancelUnlink())
    expect(writes()).toHaveLength(0)
    expect(result.current.pendingUnlink).toBeNull()
  })

  it("keeps the confirmation when the server refuses the unlink", async () => {
    serve([conn("c-eng", "c-ops", "bidirectional")], (_u, init) => (init?.method === "DELETE" ? json({ detail: "Connection changed" }, 409) : undefined))
    const { result } = await mount()
    await act(() => result.current.setPair(ENG, OPS, "none"))
    await act(async () => { await expect(result.current.confirmUnlink()).rejects.toThrow() })
    expect(toast.error).toHaveBeenCalled()
    expect(result.current.pendingUnlink).toEqual({ self: ENG, other: OPS })
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
    expect(toast.error).toHaveBeenCalledWith("target crew is archived", { duration: Infinity })
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

// ── applyLevels: the ladder's one write path ────────────────────────────────

type Row = { id: string; from_crew_id: string; to_crew_id: string; direction: string; status: string; forward_file_access: string; reverse_file_access: string; access_version: number }

/** A tiny stateful server: POST widens or creates, PUT sets a level, DELETE removes. */
function stateful(initial: Row[], refusePut = false) {
  let rows = initial.map((r) => ({ ...r }))
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    const body = init?.body ? JSON.parse(String(init.body)) : null
    if (init?.method === "POST") {
      const hit = rows.find((r) => (r.from_crew_id === body.from_crew_id && r.to_crew_id === body.to_crew_id) || (r.from_crew_id === body.to_crew_id && r.to_crew_id === body.from_crew_id))
      if (hit) {
        const widened = hit.from_crew_id !== body.from_crew_id ? "bidirectional" : body.direction
        if (widened !== hit.direction) { hit.direction = widened; hit.access_version += 1 }
      } else rows.push({ id: `cc-${rows.length + 1}`, from_crew_id: body.from_crew_id, to_crew_id: body.to_crew_id, direction: body.direction, status: "active", forward_file_access: body.forward_file_access ?? "none", reverse_file_access: body.reverse_file_access ?? "none", access_version: 1 })
      return json({ id: "x" }, 201)
    }
    if (init?.method === "PUT") {
      if (refusePut) return json({ detail: "link changed" }, 409)
      const id = url.split("/crew-connections/")[1].split("/")[0]
      const row = rows.find((r) => r.id === id)!
      if (row.from_crew_id === body.requester_crew_id) row.forward_file_access = body.level; else row.reverse_file_access = body.level
      row.access_version += 1
      return json({})
    }
    if (init?.method === "DELETE") {
      const id = url.split("/crew-connections/")[1].split("?")[0]
      rows = rows.filter((r) => r.id !== id)
      return json(null, 204)
    }
    if (url.startsWith("/api/v1/crew-connections")) return json(rows)
    if (url.startsWith("/api/v1/crews")) return json([ENG, OPS])
    return json(null, 404)
  })
}
const row = (from: string, to: string, direction: string, f: string, r: string): Row => ({ id: "cc-1", from_crew_id: from, to_crew_id: to, direction, status: "active", forward_file_access: f, reverse_file_access: r, access_version: 1 })
const methods = () => writes().map(([, init]) => (init as RequestInit).method)

describe("useCrewLinks.applyLevels", () => {
  it("creates a link, then sets the file level with the version the server now holds", async () => {
    stateful([])
    const { result } = await mount()
    await act(async () => { expect(await result.current.applyLevels(ENG, OPS, { out: "deliver", in: "off" })).toBe("done") })
    expect(methods()).toEqual(["POST", "PUT"])
    const put = writes()[1]
    expect(JSON.parse(String((put[1] as RequestInit).body))).toEqual({ requester_crew_id: "c-eng", level: "read_write", expected_version: 1 })
    expect(toast.success).toHaveBeenCalledTimes(1)
    expect(toast.success).toHaveBeenCalledWith("Engineering → Ops: Deliver files")
  })

  it("does not write a file level a new direction already has", async () => {
    stateful([])
    const { result } = await mount()
    await act(async () => { await result.current.applyLevels(ENG, OPS, { out: "work", in: "off" }) })
    expect(methods()).toEqual(["POST"])
  })

  it("only sets the file level when the direction already exists", async () => {
    stateful([row("c-eng", "c-ops", "unidirectional", "read", "none")])
    const { result } = await mount()
    await act(async () => { await result.current.applyLevels(ENG, OPS, { out: "deliver", in: "off" }) })
    expect(methods()).toEqual(["PUT"])
  })

  it("adds the reverse direction to a one-way link as a two-way link, then its level", async () => {
    stateful([row("c-eng", "c-ops", "unidirectional", "read", "none")])
    const { result } = await mount()
    await act(async () => { await result.current.applyLevels(ENG, OPS, { out: "view", in: "view" }) })
    expect(methods()).toEqual(["POST", "PUT"])
    expect(JSON.parse(String((writes()[0][1] as RequestInit).body)).direction).toBe("bidirectional")
  })

  it("turns a one-way link around by delete and create, then sets the new direction's level", async () => {
    stateful([row("c-ops", "c-eng", "unidirectional", "read", "none")])
    const { result } = await mount()
    // Ops handed to Engineering; now Engineering hands to Ops, and Ops no longer does.
    await act(async () => { await result.current.applyLevels(ENG, OPS, { out: "view", in: "off" }) })
    expect(methods()).toEqual(["DELETE", "POST", "PUT"])
  })

  it("asks before removing the last direction and writes nothing yet", async () => {
    stateful([row("c-eng", "c-ops", "unidirectional", "read", "none")])
    const { result } = await mount()
    await act(async () => { expect(await result.current.applyLevels(ENG, OPS, { out: "off", in: "off" })).toBe("asked") })
    expect(writes()).toHaveLength(0)
    expect(result.current.pendingUnlink).toEqual({ self: ENG, other: OPS })
  })

  it("keeps the other direction when one goes Off", async () => {
    stateful([row("c-eng", "c-ops", "bidirectional", "read", "read_write")])
    const { result } = await mount()
    await act(async () => { await result.current.applyLevels(ENG, OPS, { out: "off", in: "deliver" }) })
    // Delete and re-create one-way as Ops → Engineering; Ops' level was carried over, so no PUT.
    expect(methods()).toEqual(["DELETE", "POST"])
    expect(JSON.parse(String((writes()[1][1] as RequestInit).body))).toMatchObject({ from_crew_id: "c-ops", to_crew_id: "c-eng", direction: "unidirectional", forward_file_access: "read_write" })
  })

  it("reports a refused level change and reloads, so the control returns to what the server holds", async () => {
    stateful([row("c-eng", "c-ops", "unidirectional", "read", "none")], true)
    const { result } = await mount()
    await act(async () => { expect(await result.current.applyLevels(ENG, OPS, { out: "deliver", in: "off" })).toBe("failed") })
    expect(toast.error).toHaveBeenCalledWith("link changed", { duration: Infinity })
    expect(result.current.connections[0].forward_file_access).toBe("read")
  })
})
