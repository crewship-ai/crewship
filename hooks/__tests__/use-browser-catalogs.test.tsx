import React from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { useAccent } from "@/hooks/use-accent"
import { useSlashCommands } from "@/hooks/use-slash-commands"
import { useJournalSpend } from "@/hooks/use-journal-spend"
import { ACCENT_STORAGE_KEY } from "@/lib/theme/accents"
import { EMPTY_SPEND_RESPONSE } from "@/lib/types/journal-spend"

const fetchMock = vi.fn()
let client: QueryClient
function Wrapper({ children }: { children: React.ReactNode }) { return <QueryClientProvider client={client}>{children}</QueryClientProvider> }
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal("fetch", fetchMock)
  const stored = new Map<string, string>()
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => stored.get(key) ?? null,
    setItem: (key: string, value: string) => { stored.set(key, value) },
    removeItem: (key: string) => { stored.delete(key) },
    clear: () => stored.clear(),
  })
  delete document.documentElement.dataset.accent
  client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
})
afterEach(() => { cleanup(); client.clear(); localStorage.clear(); delete document.documentElement.dataset.accent; vi.unstubAllGlobals() })
function json(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status }) }

describe("browser accent", () => {
  it("loads the saved choice and applies a user's new choice immediately", () => {
    localStorage.setItem(ACCENT_STORAGE_KEY, "violet")
    const { result } = renderHook(useAccent)
    expect(result.current[0]).toBe("violet")
    act(() => result.current[1]("teal"))
    expect(result.current[0]).toBe("teal")
    expect(document.documentElement.dataset.accent).toBe("teal")
    expect(localStorage.getItem(ACCENT_STORAGE_KEY)).toBe("teal")
  })
  it("follows valid cross-tab changes and removes its listener on unmount", () => {
    const { result, unmount } = renderHook(useAccent)
    for (const event of [new StorageEvent("storage", { key: "unrelated", newValue: "violet" }), new StorageEvent("storage", { key: ACCENT_STORAGE_KEY, newValue: "invalid" }), new StorageEvent("storage", { key: ACCENT_STORAGE_KEY, newValue: null })]) {
      act(() => window.dispatchEvent(event))
      expect(result.current[0]).toBe("blue")
    }
    act(() => window.dispatchEvent(new StorageEvent("storage", { key: ACCENT_STORAGE_KEY, newValue: "graphite" })))
    expect(result.current[0]).toBe("graphite")
    expect(document.documentElement.dataset.accent).toBe("graphite")
    unmount()
    window.dispatchEvent(new StorageEvent("storage", { key: ACCENT_STORAGE_KEY, newValue: "indigo" }))
    expect(document.documentElement.dataset.accent).toBe("graphite")
  })
})

describe("workspace slash catalog", () => {
  it("waits for scope and switches catalogs without retaining foreign commands", async () => {
    fetchMock.mockImplementation(async (url: string) => json([{ id: url.includes("second") ? "second-command" : "first-command", label: "Action", capability: "read" }]))
    const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => useSlashCommands(ws), { wrapper: Wrapper, initialProps: { ws: null } as { ws: string | null; reload?: number } })
    expect(fetchMock).not.toHaveBeenCalled()
    rerender({ ws: "first & workspace" })
    await waitFor(() => expect(result.current.data?.[0].id).toBe("first-command"))
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/slash-commands?workspace_id=first%20%26%20workspace")
    rerender({ ws: "second" })
    expect(result.current.data).toBeUndefined()
    await waitFor(() => expect(result.current.data?.[0].id).toBe("second-command"))
  })
  it("surfaces catalog refusal instead of an empty authorized catalog", async () => {
    fetchMock.mockResolvedValue(json({}, 403))
    const { result } = renderHook(() => useSlashCommands("ws"), { wrapper: Wrapper })
    await waitFor(() => expect(result.current.error?.message).toBe("slash-commands fetch failed: 403"))
    expect(result.current.data).toBeUndefined()
  })
})

describe("journal spend", () => {
  it("encodes workspace scope and defaults, reloads explicitly and clears on disable", async () => {
    const spend = { ...EMPTY_SPEND_RESPONSE, total_cost_usd: 12 }
    fetchMock.mockImplementation(async () => json(spend))
    const { result, rerender } = renderHook(({ ws, reload }: { ws: string | null; reload?: number }) => useJournalSpend(ws, "24h", undefined, reload), { initialProps: { ws: null } as { ws: string | null; reload?: number } })
    expect(fetchMock).not.toHaveBeenCalled()
    rerender({ ws: "ws & one" })
    await waitFor(() => expect(result.current.data?.total_cost_usd).toBe(12))
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/journal/spend?workspace_id=ws%20%26%20one&window=24h&top=5")
    rerender({ ws: "ws & one", reload: 1 })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    rerender({ ws: null })
    expect(result.current.data).toBeNull()
    expect(result.current.loading).toBe(false)
  })
  it("respects the requested window and row budget", async () => {
    fetchMock.mockResolvedValue(json(EMPTY_SPEND_RESPONSE))
    const { result } = renderHook(() => useJournalSpend("ws", "30d", 12, 2))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/journal/spend?workspace_id=ws&window=30d&top=12")
  })
  it("uses the empty schema fallback for malformed spend rows", async () => {
    fetchMock.mockResolvedValue(json({ total_cost_usd: "not a number" }))
    const { result } = renderHook(() => useJournalSpend("ws", "7d"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data).toEqual(EMPTY_SPEND_RESPONSE)
  })
})
