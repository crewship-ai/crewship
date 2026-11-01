import type { ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch }))

import { useArtifactProjectAvailability } from "../use-artifact-project-availability"

const reply = (body: unknown) => apiFetch.mockResolvedValue(new Response(JSON.stringify(body), { status: 200 }))
const wrapper = ({ children }: { children: ReactNode }) => (
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>
)
beforeEach(() => apiFetch.mockReset())
afterEach(() => vi.clearAllMocks())

it("scopes the question to the agent", async () => {
  reply({ enabled: true, can_create: true })
  const { result } = renderHook(() => useArtifactProjectAvailability("ws-1", "agent 1"), { wrapper })
  await waitFor(() => expect(result.current).toBe(true))
  expect(String(apiFetch.mock.calls[0][0])).toBe("/api/v1/pages/artifacts/availability?workspace_id=ws-1&agent_id=agent%201")
})

it("is false when the agent's crew is refused", async () => {
  reply({ enabled: true, can_create: false, code: "artifact_crew_not_allowed" })
  const { result } = renderHook(() => useArtifactProjectAvailability("ws-1", "agent-1"), { wrapper })
  await waitFor(() => expect(apiFetch).toHaveBeenCalled())
  await new Promise(resolve => setTimeout(resolve, 20))
  expect(result.current).toBe(false)
})

it("is false when the feature is disabled and stays workspace-wide without an agent", async () => {
  reply({ enabled: false })
  const off = renderHook(() => useArtifactProjectAvailability("ws-1", "agent-1"), { wrapper })
  await waitFor(() => expect(apiFetch).toHaveBeenCalled())
  expect(off.result.current).toBe(false)
  reply({ enabled: true })
  const wide = renderHook(() => useArtifactProjectAvailability("ws-2"), { wrapper })
  await waitFor(() => expect(wide.result.current).toBe(true))
  expect(String(apiFetch.mock.calls.at(-1)?.[0])).not.toContain("agent_id")
})
