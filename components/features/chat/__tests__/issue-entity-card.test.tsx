import React from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import fixture from "@/internal/api/testdata/chat-entity-refs/page.json"
import deleted from "@/internal/api/testdata/chat-entity-refs/deleted.json"
import type { EntityRef } from "@/lib/chat-entity-cards"
import { IssueEntityCard } from "../issue-entity-card"
const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch, broadcastSessionExpired: vi.fn() }))
vi.mock("../chat-agent-context", () => ({ useChatAgent: () => ({ id: "entity-agent", name: "Jamie" }) }))
vi.mock("../issue-pull-requests", () => ({ ChatIssuePullRequests: ({ crewId, identifier }: { crewId: string; identifier: string }) => <div data-testid="chat-pr-card">{crewId}:{identifier}</div> }))
const base = fixture.refs[0] as EntityRef
const props = { reference: base, chatId: "chat", userId: "viewer", rechecking: false, live: true, checkedAt: 1234, onOpen: vi.fn(), reconcile: vi.fn() }
function setup(reference = base) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  const view = render(<IssueEntityCard {...props} reference={reference} />, { wrapper })
  return { ...view, rerenderCard: (next: Partial<typeof props>) => view.rerender(<IssueEntityCard {...props} reference={reference} {...next} />) }
}
afterEach(() => { cleanup(); fetch.mockReset(); props.onOpen.mockClear(); props.reconcile.mockClear(); sessionStorage.clear() })
it("renders the real projection and operation provenance without fetching per card", () => {
  setup()
  expect(screen.getByRole("heading", { name: "Synthetic title" })).toBeVisible()
  expect(screen.getByText(/Created by Jamie/)).toBeVisible()
  expect(screen.getByText("Live")).toBeVisible()
  expect(fetch).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "Open on right" }))
  expect(props.onOpen).toHaveBeenCalledWith(base)
})
it("reveals only viewer actions and keeps the verified projection during recheck", () => {
  const ref = { ...base, current: { ...base.current!, allowed_actions: ["view"] as const } } as EntityRef
  const { rerenderCard } = setup(ref)
  expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Comment" })).toBeNull()
  rerenderCard({ rechecking: true })
  expect(screen.getByText("ENG-1")).toBeVisible()
  expect(screen.getByText("Synthetic title")).toBeVisible()
})
it("uses a tombstone without a stale identifier or title", () => {
  setup(deleted.refs[0] as EntityRef)
  expect(screen.getByText("Issue deleted")).toBeVisible()
  expect(screen.queryByRole("button")).toBeNull()
  expect(screen.queryByText("ENG-1")).toBeNull()
})
it("sends inline edits with their captured revision and a stable key", async () => {
  fetch.mockImplementation(async (path: string) => new Response(JSON.stringify(path.includes("/agents") ? [] : { crew_id: "crew", revision: 1, allowed_actions: base.current!.allowed_actions })))
  setup()
  fireEvent.click(screen.getByRole("button", { name: "Edit" }))
  await screen.findByRole("textbox", { name: "Title" })
  fireEvent.change(screen.getByRole("textbox", { name: "Title" }), { target: { value: "Kept draft" } })
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(props.reconcile).toHaveBeenCalled())
  const write = fetch.mock.calls.find(([, init]) => init?.method === "PATCH")!
  expect(JSON.parse(write[1].body)).toMatchObject({ expected_revision: 1, title: "Kept draft" })
  expect(write[1].headers["Idempotency-Key"]).toBeTruthy()
})
it("retains draft on validation and retries an unknown comment with the same request", async () => {
  fetch.mockResolvedValueOnce(new Response(JSON.stringify({ crew_id: "crew", allowed_actions: ["view", "comment"] })))
  setup()
  fireEvent.click(screen.getByRole("button", { name: "Comment" }))
  const input = await screen.findByRole("textbox", { name: "Comment" })
  fireEvent.change(input, { target: { value: "One comment" } })
  fetch.mockResolvedValueOnce(new Response('{"error":"body_invalid"}', { status: 400 }))
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await screen.findByText("body_invalid")
  expect(input).toHaveValue("One comment")
  fetch.mockRejectedValueOnce(new Error("lost acknowledgement"))
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await screen.findByText("The outcome is unknown. Your draft is kept.")
  fetch.mockResolvedValueOnce(new Response("{}"))
  fireEvent.click(screen.getByRole("button", { name: "Check and retry the same operation" }))
  await waitFor(() => expect(props.reconcile).toHaveBeenCalled())
  const writes = fetch.mock.calls.filter(([, init]) => init?.method === "POST")
  expect(writes).toHaveLength(3)
  expect(writes[2]).toEqual(writes[1])
})
it("keeps an editor draft through authorization reconciliation", async () => {
  fetch.mockImplementation(async () => new Response(JSON.stringify({ crew_id: "crew", allowed_actions: base.current!.allowed_actions })))
  const { rerenderCard } = setup()
  fireEvent.click(screen.getByRole("button", { name: "Edit" }))
  const input = await screen.findByRole("textbox", { name: "Title" })
  fireEvent.change(input, { target: { value: "Unsaved" } })
  act(() => rerenderCard({ rechecking: true }))
  input.focus()
  expect(screen.getByDisplayValue("Unsaved")).toBe(input)
  expect(input).toHaveFocus()
  act(() => rerenderCard({ rechecking: false }))
  await waitFor(() => expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Unsaved"))
})

it("opens linked requests only on demand using the verified issue crew", async () => {
  fetch.mockResolvedValue(new Response(JSON.stringify({ crew_id: "verified-crew", allowed_actions: ["view"] })))
  setup()
  expect(fetch).not.toHaveBeenCalled()
  expect(screen.queryByTestId("chat-pr-card")).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Pull requests" }))
  expect(await screen.findByTestId("chat-pr-card")).toHaveTextContent("verified-crew:ENG-1")
  expect(fetch).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole("button", { name: "Pull requests" }))
  expect(screen.queryByTestId("chat-pr-card")).toBeNull()
})
it("reloads current conflict values alongside the retained draft before a fresh save", async () => {
  let revision = 1
  fetch.mockImplementation(async (path: string, init?: { method?: string }) => {
    if (init?.method === "PATCH") { revision = 2; return new Response("{}", { status: 409 }) }
    return new Response(JSON.stringify(path.includes("/agents") ? [] : { title: revision === 1 ? "Synthetic title" : "Other person's title", status: base.current!.status, priority: revision === 1 ? "none" : "high", crew_id: "crew", revision, allowed_actions: base.current!.allowed_actions }))
  })
  setup()
  fireEvent.click(screen.getByRole("button", { name: "Edit" }))
  const title = await screen.findByRole("textbox", { name: "Title" })
  fireEvent.change(title, { target: { value: "My draft" } })
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await screen.findByText("Title: Other person's title")
  expect(title).toHaveValue("My draft")
  fireEvent.click(screen.getByRole("button", { name: "Reload current values; keep my draft" }))
  await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled())
  fireEvent.click(screen.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init?.method === "PATCH")).toHaveLength(2))
  const second = fetch.mock.calls.filter(([, init]) => init?.method === "PATCH")[1]
  expect(JSON.parse(second[1].body)).toEqual({ title: "My draft", expected_revision: 2 })
})
