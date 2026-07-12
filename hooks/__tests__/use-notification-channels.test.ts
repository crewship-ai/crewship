import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor, act } from "@testing-library/react"

import { useNotificationChannels } from "@/hooks/use-notification-channels"

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    json: async () => body,
  } as unknown as Response
}

function errJSON(status: number, error: string): Response {
  return {
    ok: false,
    status,
    json: async () => ({ error }),
  } as unknown as Response
}

const CH = {
  id: "nc1",
  workspace_id: "ws1",
  type: "webhook",
  url: "https://example.com/hook",
  events: ["failed"],
  enabled: true,
}

beforeEach(() => {
  global.fetch = vi.fn()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe("useNotificationChannels", () => {
  it("lists channels on mount, scoped to the workspace", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      okJSON({ channels: [CH] }),
    )
    const { result } = renderHook(() => useNotificationChannels("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBeNull()
    expect(result.current.channels).toHaveLength(1)
    expect(result.current.channels[0].id).toBe("nc1")
    const url = (global.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toContain("/api/v1/notification-channels")
    expect(url).toContain("workspace_id=ws1")
  })

  it("does not fetch without a workspace id", async () => {
    const { result } = renderHook(() => useNotificationChannels(null))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(global.fetch).not.toHaveBeenCalled()
    expect(result.current.channels).toEqual([])
  })

  it("surfaces a non-2xx list as error", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      errJSON(500, "boom"),
    )
    const { result } = renderHook(() => useNotificationChannels("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toContain("500")
  })

  it("create POSTs the body, returns the one-time secret, and refreshes", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock
      .mockResolvedValueOnce(okJSON({ channels: [] })) // mount list
      .mockResolvedValueOnce(okJSON({ ...CH, secret: "s3cr3t" })) // create
      .mockResolvedValueOnce(okJSON({ channels: [CH] })) // refresh

    const { result } = renderHook(() => useNotificationChannels("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    let created: Awaited<ReturnType<typeof result.current.create>> = null
    await act(async () => {
      created = await result.current.create({
        type: "webhook",
        url: "https://example.com/hook",
        events: ["failed"],
      })
    })
    expect(created?.secret).toBe("s3cr3t")
    const [createUrl, createInit] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(createUrl).toContain("/api/v1/notification-channels")
    expect(createInit.method).toBe("POST")
    expect(JSON.parse(createInit.body as string).type).toBe("webhook")
    await waitFor(() => expect(result.current.channels).toHaveLength(1))
  })

  it("create surfaces the server's error message", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock
      .mockResolvedValueOnce(okJSON({ channels: [] }))
      .mockResolvedValueOnce(errJSON(400, "email delivery is not configured"))

    const { result } = renderHook(() => useNotificationChannels("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    await expect(
      act(async () => {
        await result.current.create({ type: "email", to: "x@y.z" })
      }),
    ).rejects.toThrow(/email delivery is not configured/)
  })

  it("remove DELETEs by id and tolerates 404", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock
      .mockResolvedValueOnce(okJSON({ channels: [CH] })) // mount
      .mockResolvedValueOnce(errJSON(404, "channel not found")) // delete
      .mockResolvedValueOnce(okJSON({ channels: [] })) // refresh

    const { result } = renderHook(() => useNotificationChannels("ws1"))
    await waitFor(() => expect(result.current.channels).toHaveLength(1))

    await act(async () => {
      await result.current.remove("nc1")
    })
    const [delUrl, delInit] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(delUrl).toContain("/api/v1/notification-channels/nc1")
    expect(delInit.method).toBe("DELETE")
    await waitFor(() => expect(result.current.channels).toHaveLength(0))
  })

  it("sendTest POSTs to the test route and throws on failure", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock
      .mockResolvedValueOnce(okJSON({ channels: [CH] })) // mount
      .mockResolvedValueOnce(errJSON(502, "test send failed: connection refused"))

    const { result } = renderHook(() => useNotificationChannels("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    await expect(
      act(async () => {
        await result.current.sendTest("nc1")
      }),
    ).rejects.toThrow(/test send failed/)
    const [testUrl, testInit] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(testUrl).toContain("/api/v1/notification-channels/nc1/test")
    expect(testInit.method).toBe("POST")
  })
})
