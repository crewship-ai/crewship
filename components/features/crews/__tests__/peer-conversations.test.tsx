import { useEffect } from "react"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { CrewPeerConversations } from "../crew-peer-conversations"
import { apiFetch } from "@/lib/api-fetch"
import type { PeerConversation } from "@/lib/types/peer-conversation"

const { events } = vi.hoisted(() => ({ events: new Map<string, () => void>() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: (name: string, callback: () => void) => {
  useEffect(() => { events.set(name, callback); return () => { events.delete(name) } }, [name, callback])
} }))
const fetchMock = vi.mocked(apiFetch)
const conversation: PeerConversation = { id: "first", from_name: "Writer", from_slug: "writer", to_name: "Reviewer", to_slug: "reviewer", question: "Is the result valid?", response: "The result is valid.", status: "COMPLETED", duration_ms: 1250, escalated: false, created_at: "2026-10-03T00:00:00Z", finished_at: "2026-10-03T00:00:01Z" }
beforeEach(() => { fetchMock.mockReset(); events.clear() })
afterEach(cleanup)

it("loads scoped conversations and explains an empty result", async () => {
  fetchMock.mockResolvedValue(new Response("[]"))
  render(<CrewPeerConversations crewId="crew" workspaceId="workspace" />)
  expect(screen.getByText("Loading peer conversations...")).toBeInTheDocument()
  expect(await screen.findByText("No peer conversations yet.")).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledWith("/api/v1/crews/crew/peer-conversations?workspace_id=workspace&limit=50")
})

it("expands responses with pointer and keyboard while leaving unanswered rows inert", async () => {
  fetchMock.mockResolvedValue(new Response(JSON.stringify([
    conversation,
    { ...conversation, id: "running", question: "Still working?", status: "RUNNING", response: null, duration_ms: null, finished_at: null },
    { ...conversation, id: "failed", question: "Need help?", status: "FAILED", response: "Escalated response", escalated: true },
  ])))
  const { container } = render(<CrewPeerConversations crewId="crew" workspaceId="workspace" />)
  const row = await screen.findByRole("button", { name: /Is the result valid/ })
  expect(row).toHaveAttribute("aria-expanded", "false")
  fireEvent.click(row)
  expect(row).toHaveAttribute("aria-expanded", "true")
  expect(document.getElementById(row.getAttribute("aria-controls")!)).toHaveTextContent("The result is valid.")
  fireEvent.keyDown(row, { key: "Escape" })
  expect(row).toHaveAttribute("aria-expanded", "true")
  fireEvent.keyDown(row, { key: "Enter" })
  expect(screen.queryByText("The result is valid.")).not.toBeInTheDocument()
  fireEvent.keyDown(row, { key: " " })
  expect(row).toHaveAttribute("aria-expanded", "true")
  const unanswered = screen.getByText("Still working?").closest("tr")!
  expect(unanswered).not.toHaveAttribute("role", "button")
  expect(unanswered).toHaveAttribute("tabindex", "-1")
  expect(within(unanswered).getByText("—")).toBeInTheDocument()
  fireEvent.click(unanswered)
  fireEvent.keyDown(unanswered, { key: "Enter" })
  expect(row).toHaveAttribute("aria-expanded", "true")
  expect(container.querySelector(".animate-ping")).not.toBeNull()
  fireEvent.click(screen.getByRole("button", { name: /Need help/ }))
  expect(screen.getByText("Escalated response")).toBeInTheDocument()
  expect(screen.getByText("Escalated")).toBeInTheDocument()
  expect(row).toHaveAttribute("aria-expanded", "false")
  expect(within(row).getByText("@writer")).toBeInTheDocument()
  expect(within(row).getByText("@reviewer")).toBeInTheDocument()
})

it("keeps the last valid response on transient or malformed refresh and accepts the next valid event", async () => {
  fetchMock.mockResolvedValueOnce(new Response(JSON.stringify([conversation])))
  const { unmount } = render(<CrewPeerConversations crewId="crew" workspaceId="workspace" />)
  await screen.findByText(conversation.question)
  for (const response of [new Response("offline", { status: 503 }), new Response(JSON.stringify([{ ...conversation, status: "unknown" }]))]) {
    fetchMock.mockResolvedValueOnce(response)
    await act(async () => { events.get("peer_conversation.updated")!() })
    expect(screen.getByText(conversation.question)).toBeInTheDocument()
    expect(screen.queryByText("Loading peer conversations...")).not.toBeInTheDocument()
  }
  fetchMock.mockResolvedValueOnce(new Response(JSON.stringify([{ ...conversation, question: "Updated question" }])))
  await act(async () => { events.get("peer_conversation.updated")!() })
  expect(screen.getByText("Updated question")).toBeInTheDocument()
  unmount()
  expect(events.size).toBe(0)
})
