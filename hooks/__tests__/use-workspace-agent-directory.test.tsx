import { describe, expect, it, vi } from "vitest"
import { act, renderHook, waitFor } from "@testing-library/react"
import { useWorkspaceAgentDirectory } from "../use-workspace-agent-directory"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))

describe("workspace agent identities", () => {
  it("shares one request across cards and ignores responses for the previous workspace", async () => {
    let resolveOld!: (value: unknown) => void
    api.mockImplementation((url: string) => url.includes("old-workspace") ? new Promise(resolve => { resolveOld = resolve }) : Promise.resolve({ ok: true, json: async () => [{ id: "new", slug: "morgan", name: "New Morgan" }] }))
    const { result, rerender } = renderHook(({ workspace }) => [useWorkspaceAgentDirectory(workspace), useWorkspaceAgentDirectory(workspace)], { initialProps: { workspace: "old-workspace" } })
    expect(api).toHaveBeenCalledTimes(1)
    rerender({ workspace: "new-workspace" })
    await waitFor(() => expect(result.current[0].agents?.[0].name).toBe("New Morgan"))
    await act(async () => resolveOld({ ok: true, json: async () => [{ id: "old", slug: "morgan", name: "Old Morgan" }] }))
    expect(result.current[0].agents?.[0].name).toBe("New Morgan")
    expect(result.current[1].agents?.[0].name).toBe("New Morgan")
    expect(api).toHaveBeenCalledTimes(2)
  })
  it("distinguishes a failed lookup from an empty directory", async () => {
    api.mockResolvedValue({ ok: false })
    const { result } = renderHook(() => useWorkspaceAgentDirectory("unavailable-workspace"))
    await waitFor(() => expect(result.current.error).toBe(true))
    expect(result.current.agents).toBeNull()
  })
})
