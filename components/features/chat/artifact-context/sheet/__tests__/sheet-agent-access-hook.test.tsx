import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", async () => ({ ...await vi.importActual("@/lib/api-fetch"), apiFetch }))

import { useSheetAgentAccess } from "../use-sheet-agent-access"

const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>
const advice = () => renderHook(() => useSheetAgentAccess("ws1", "pg_1", "chat-1", false), { wrapper })

describe("the server's advice never tells a reader whether someone else's Sheet exists", () => {
  beforeEach(() => apiFetch.mockReset())
  it("says the same for a Sheet the reader may not read as for one that does not exist", async () => {
    apiFetch.mockResolvedValue(new Response(null, { status: 403 }))
    const forbidden = advice()
    await waitFor(() => expect(forbidden.result.current.checking).toBe(false))
    apiFetch.mockResolvedValue(new Response(null, { status: 404 }))
    const missing = advice()
    await waitFor(() => expect(missing.result.current.checking).toBe(false))
    expect(forbidden.result.current.refusal).toBe(missing.result.current.refusal)
    expect(forbidden.result.current.refusal).not.toMatch(/permission|owner|exists|private/i)
  })
  it("asks without storing, for this Sheet in this chat only", async () => {
    apiFetch.mockResolvedValue(new Response(JSON.stringify({ allowed: true, max_sheets: 1 }), { status: 200 }))
    const { result } = advice()
    await waitFor(() => expect(result.current.checking).toBe(false))
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/pages/%40pg_1/craft/agent-access?workspace_id=ws1&chat_id=chat-1", expect.objectContaining({ cache: "no-store" }))
  })
})
