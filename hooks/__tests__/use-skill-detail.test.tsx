import { describe, expect, it, vi } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"

const h = vi.hoisted(() => ({ status: 404, calls: 0 }))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async () => {
    h.calls++
    return { ok: h.status < 400, status: h.status, json: async () => ({}) }
  }),
}))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => undefined }))

import { useSkillDetail } from "@/hooks/use-skills"

// The app's QueryClient keeps react-query's default of three retries with
// back-off (~7 s). A skill that does not exist must say so at once rather
// than show a skeleton for those seconds (e2e L05 on dev1).
function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient()
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

describe("useSkillDetail", () => {
  it("does not retry a 404: the skill is gone, not slow", async () => {
    h.status = 404
    h.calls = 0
    const { result } = renderHook(() => useSkillDetail("ws", "does-not-exist"), { wrapper })
    await waitFor(() => expect(result.current.isError).toBe(true), { timeout: 800 })
    expect(h.calls).toBe(1)
  })

  it("still retries a server error", async () => {
    h.status = 503
    h.calls = 0
    const { result } = renderHook(() => useSkillDetail("ws", "sk"), { wrapper })
    await waitFor(() => expect(h.calls).toBeGreaterThan(1), { timeout: 2500 })
    expect(result.current.isError).toBe(false)
  })
})
