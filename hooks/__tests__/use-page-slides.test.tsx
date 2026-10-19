import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import fixture from "@/testdata/slides-schema-v1.json"
import { usePageSlides, type PageSlidesInput } from "../use-page-slides"

const events = vi.hoisted(() => new Map<string, () => void>())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (event: string, callback: () => void) => events.set(event, callback) }))
const input = (): PageSlidesInput => ({ workspaceId: "ws /&", pageId: "fixture_page", currentUserId: "human", activationKey: "auth-open-1", enabled: true })
const saved = () => ({ page_id: "fixture_page", schema_version: 1, revision: 7, order_version: 3, content_hash: fixture.hash, deck: structuredClone(fixture.deck) })
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
function deferred() {
  let resolve!: (value: Response) => void
  const promise = new Promise<Response>(done => { resolve = done })
  return { promise, resolve }
}
beforeEach(() => { events.clear(); vi.mocked(apiFetch).mockReset(); vi.mocked(apiFetch).mockResolvedValue(response(saved())) })
afterEach(cleanup)

it("reads only the immutable Page endpoint with no-store and publishes the exact confirmed state", async () => {
  const { result } = renderHook(() => usePageSlides(input()))
  await waitFor(() => expect(result.current.read.status).toBe("ready"))
  expect(apiFetch).toHaveBeenCalledTimes(1)
  const [url, options] = vi.mocked(apiFetch).mock.calls[0]
  expect(url).toBe("/api/v1/pages/%40fixture_page/slides?workspace_id=ws+%2F%26")
  expect(options?.cache).toBe("no-store")
  expect(options?.signal).toBeInstanceOf(AbortSignal)
  expect(options?.method).toBeUndefined()
  expect(result.current.read).toMatchObject({ status: "ready", state: saved() })
})

it.each(["off", "truthy flag", "no workspace", "no Page", "invalid Page", "no human", "no epoch"])("makes no HTTP request when activation is invalid: %s", kind => {
  const props = input()
  if (kind === "off") props.enabled = false
  if (kind === "truthy flag") props.enabled = 1 as unknown as boolean
  if (kind === "no workspace") props.workspaceId = " "
  if (kind === "no Page") props.pageId = ""
  if (kind === "invalid Page") props.pageId = "../replacement"
  if (kind === "no human") props.currentUserId = null
  if (kind === "no epoch") props.activationKey = " "
  const { result } = renderHook(() => usePageSlides(props))
  expect(result.current.read).toEqual({ status: "unavailable" })
  expect(apiFetch).not.toHaveBeenCalled()
})

it.each([
  { page_id: "other" }, { schema_version: 2 }, { schema_version: 99 },
  { revision: 0 }, { revision: Number.MAX_SAFE_INTEGER + 1 }, { order_version: 0 },
  { content_hash: "not-a-hash" }, { content_hash: "a".repeat(64) }, { extra: "private marker" },
])("rejects the whole invalid envelope without exposing metadata: %j", change => {
  vi.mocked(apiFetch).mockResolvedValue(response({ ...saved(), ...change }))
  return (async () => {
    const { result } = renderHook(() => usePageSlides(input()))
    await waitFor(() => expect(result.current.read).toEqual({ status: "unavailable" }))
    expect(result.current.read).not.toHaveProperty("state")
  })()
})

it.each(["wrong deck ID", "schema2", "future", "unknown field", "invalid geometry", "image"])("rejects an entire unsupported deck: %s", kind => {
  const value = saved()
  if (kind === "wrong deck ID") value.deck.deck_id = "other"
  if (kind === "schema2") value.deck.schema_version = 2
  if (kind === "future") value.deck.schema_version = 99
  if (kind === "unknown field") Object.assign(value.deck, { html: "private marker" })
  if (kind === "invalid geometry") value.deck.slides[0].elements[0].width = 99999
  if (kind === "image") Object.assign(value.deck.slides[0].elements[0], { type: "image", image: { alt: "private marker" } })
  vi.mocked(apiFetch).mockResolvedValue(response(value))
  return (async () => {
    const { result } = renderHook(() => usePageSlides(input()))
    await waitFor(() => expect(result.current.read).toEqual({ status: "unavailable" }))
  })()
})

it.each([401, 403, 404, 409, 503])("drops content on HTTP failure %s", async status => {
  vi.mocked(apiFetch).mockResolvedValue(response({ error: "Protected server marker", ...saved() }, status))
  const { result } = renderHook(() => usePageSlides(input()))
  await waitFor(() => expect(result.current.read.status).toBe([401, 403, 404].includes(status) ? "denied" : "unavailable"))
  expect(result.current.read).not.toHaveProperty("state")
  expect(JSON.stringify(result.current.read)).not.toContain("Protected server marker")
})

it.each(["human", "workspace", "Page", "epoch", "disable"])("masks a ready deck on the first render after %s changes", async kind => {
  const seen: string[] = []
  const { result, rerender } = renderHook(props => { const read = usePageSlides(props); seen.push(read.read.status); return read }, { initialProps: input() })
  await waitFor(() => expect(result.current.read.status).toBe("ready"))
  const pending = deferred()
  vi.mocked(apiFetch).mockReturnValue(pending.promise)
  const next = input()
  if (kind === "human") next.currentUserId = "other-human"
  if (kind === "workspace") next.workspaceId = "other-workspace"
  if (kind === "Page") next.pageId = "other-page"
  if (kind === "epoch") next.activationKey = "auth-open-2"
  if (kind === "disable") next.enabled = false
  const offset = seen.length
  rerender(next)
  expect(seen[offset]).toBe(kind === "disable" ? "unavailable" : "loading")
  expect(result.current.read).not.toHaveProperty("state")
  expect((vi.mocked(apiFetch).mock.calls[0][1]?.signal as AbortSignal).aborted).toBe(true)
})

it.each(["page.updated", "page.deleted", "realtime.reconnected"])("masks settled state and ignores old pending reads after %s", async event => {
  const { result } = renderHook(() => usePageSlides(input()))
  await waitFor(() => expect(result.current.read.status).toBe("ready"))
  const old = deferred()
  vi.mocked(apiFetch).mockReturnValueOnce(old.promise)
  act(() => events.get("page.updated")?.())
  expect(result.current.read).toEqual({ status: "loading" })
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
  vi.mocked(apiFetch).mockResolvedValue(response({ error: "denied" }, 404))
  act(() => events.get(event)?.())
  expect(result.current.read).toEqual({ status: "loading" })
  await waitFor(() => expect(result.current.read).toEqual({ status: "denied" }))
  await act(async () => old.resolve(response(saved())))
  expect(result.current.read).toEqual({ status: "denied" })
})

it("aborts on unmount and treats malformed JSON or a failed transport as content-free", async () => {
  const pending = deferred()
  vi.mocked(apiFetch).mockReturnValue(pending.promise)
  const view = renderHook(() => usePageSlides(input()))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
  view.unmount()
  expect((vi.mocked(apiFetch).mock.calls[0][1]?.signal as AbortSignal).aborted).toBe(true)
  await act(async () => pending.resolve(response(saved())))
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response("invalid JSON")).mockRejectedValueOnce(new Error("network marker"))
  const invalid = renderHook(() => usePageSlides(input()))
  await waitFor(() => expect(invalid.result.current.read).toEqual({ status: "unavailable" }))
  invalid.unmount()
  const failed = renderHook(() => usePageSlides(input()))
  await waitFor(() => expect(failed.result.current.read.status).not.toBe("loading"))
  expect(failed.result.current.read).not.toHaveProperty("state")
  expect(JSON.stringify(failed.result.current.read)).not.toContain("network marker")
})
