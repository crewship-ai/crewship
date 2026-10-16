import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { useResolvedPageIdentity } from "@/hooks/use-page-identity"
import { apiFetch } from "@/lib/api-fetch"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const reference = { kind: "page" as const, pageId: "page-original", workspaceId: "ws-1" }
const response = (slug: string) => new Response(JSON.stringify({ page_id: reference.pageId, slug }))

beforeEach(() => vi.mocked(apiFetch).mockReset())

describe("stable Page resolution lifetime", () => {
  it("aborts and hides immediately on revocation; late completion cannot restore content", async () => {
    let complete!: (value: Response) => void
    vi.mocked(apiFetch).mockImplementationOnce(() => new Promise(resolve => { complete = resolve }))
    const hook = renderHook(({ eligible }) => useResolvedPageIdentity("ws-1", reference, "viewer", eligible), { initialProps: { eligible: true } })
    const signal = vi.mocked(apiFetch).mock.calls[0][1]!.signal!
    hook.rerender({ eligible: false })
    expect(signal.aborted).toBe(true)
    expect(hook.result.current.resolution).toBeNull()
    await act(async () => complete(response("old")))
    expect(hook.result.current.resolution).toBeNull()
    vi.mocked(apiFetch).mockResolvedValueOnce(response("new"))
    hook.rerender({ eligible: true })
    expect(hook.result.current.resolution).toBeNull()
    await waitFor(() => expect(hook.result.current.resolution?.identity.slug).toBe("new"))
  })

  it("keeps a healthy resolution through a directory rerender, but isolates retry, viewer, and remount", async () => {
    vi.mocked(apiFetch).mockImplementation(async () => response("current"))
    const hook = renderHook(({ viewer }) => useResolvedPageIdentity("ws-1", reference, viewer, true), { initialProps: { viewer: "first" } })
    await waitFor(() => expect(hook.result.current.resolution).not.toBeNull())
    const original = hook.result.current.resolution!.binding.scope
    hook.rerender({ viewer: "first" })
    expect(hook.result.current.resolution!.binding.scope).toBe(original)
    expect(apiFetch).toHaveBeenCalledTimes(1)
    act(() => hook.result.current.reload())
    expect(hook.result.current.resolution).toBeNull()
    await waitFor(() => expect(hook.result.current.resolution).not.toBeNull())
    expect(hook.result.current.resolution!.binding.scope).not.toBe(original)
    hook.rerender({ viewer: "second" })
    expect(hook.result.current.resolution).toBeNull()
    await waitFor(() => expect(hook.result.current.resolution?.binding.viewerId).toBe("second"))
    const beforeUnmount = hook.result.current.resolution!.binding.scope
    hook.unmount()
    const remount = renderHook(() => useResolvedPageIdentity("ws-1", reference, "second", true))
    await waitFor(() => expect(remount.result.current.resolution).not.toBeNull())
    expect(remount.result.current.resolution!.binding.scope).not.toBe(beforeUnmount)
  })

  it("never resolves an explicit workspace mismatch or invalid reference", () => {
    const hook = renderHook(() => useResolvedPageIdentity("wrong-workspace", reference, "viewer", true))
    expect(hook.result.current.resolution).toBeNull()
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
