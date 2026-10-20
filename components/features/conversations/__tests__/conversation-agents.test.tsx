import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"

import { answerConfirm, mountConfirmHost } from "@/components/ui/__tests__/confirm-test-utils"
import { ConversationAgents } from "../conversation-agents"

const fetcher = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", async (original) => ({ ...(await original<typeof import("@/lib/api-fetch")>()), apiFetch: fetcher }))

afterEach(() => { cleanup(); fetcher.mockReset() })

const agents = [{ agent_id: "a1", name: "Scout", slug: "scout", joined_at: "2026-10-01T10:00:00Z" }]

function setup() {
  const refresh = vi.fn()
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<QueryClientProvider client={client}><ConversationAgents workspaceId="ws" conversationId="c1" agents={agents} canManage refresh={refresh} /></QueryClientProvider>)
  return refresh
}

it("asks in the page before removing an agent; cancel removes nothing", async () => {
  mountConfirmHost()
  fetcher.mockImplementation(async (_url: string, options?: RequestInit) => options?.method === "DELETE" ? new Response(null, { status: 204 }) : Response.json([]))
  const refresh = setup()
  fireEvent.click(screen.getByRole("button", { name: "Remove" }))
  expect(await screen.findByRole("alertdialog", { name: "Remove Scout from this channel?" })).toBeVisible()
  await answerConfirm("Cancel")
  expect(fetcher.mock.calls.some(([, o]) => o?.method === "DELETE")).toBe(false)
  fireEvent.click(screen.getByRole("button", { name: "Remove" }))
  await answerConfirm("Remove")
  await waitFor(() => expect(refresh).toHaveBeenCalled())
  expect(fetcher.mock.calls.some(([u, o]) => o?.method === "DELETE" && u.includes("/agents/a1"))).toBe(true)
})
