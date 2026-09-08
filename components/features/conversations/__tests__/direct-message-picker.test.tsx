import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { DirectMessagePicker } from "../direct-message-picker"

const fetcher = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
const people = [
  { user: { id: "alice", full_name: "Alice", email: "alice@example.test" } },
  { user: { id: "bob", full_name: "Bob", email: "bob@example.test" } },
]
const direct = { id: "existing-dm", title: "Alice · Bob", kind: "group", is_direct: true, access_scope: "participants" }
function setup(onOpened = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return { ...render(<QueryClientProvider client={client}><DirectMessagePicker workspaceId="ws" userId="alice" onOpened={onOpened} /></QueryClientProvider>), onOpened }
}
beforeEach(() => {
  fetcher.mockReset()
  fetcher.mockImplementation(async (url: string) => Response.json(url.includes("/members") ? people : direct))
})
describe("direct message discovery", () => {
  it("excludes self, filters colleagues and opens the canonical existing conversation without membership mutations", async () => {
    const { onOpened } = setup()
    await screen.findByRole("button", { name: /Bob/ })
    expect(screen.queryByRole("button", { name: /Alice/ })).not.toBeInTheDocument()
    fireEvent.change(screen.getByRole("textbox", { name: "Find a colleague" }), { target: { value: "missing" } })
    expect(screen.getByText("No people match your search.")).toBeInTheDocument()
    fireEvent.change(screen.getByRole("textbox", { name: "Find a colleague" }), { target: { value: "bob@" } })
    fireEvent.click(screen.getByRole("button", { name: /Bob/ }))
    await waitFor(() => expect(onOpened).toHaveBeenCalledWith(direct))
    const writes = fetcher.mock.calls.filter(([, options]) => options?.method === "POST")
    expect(writes).toHaveLength(1)
    expect(writes[0][0]).toBe("/api/v1/conversations/direct?workspace_id=ws")
    expect(JSON.parse(writes[0][1].body)).toEqual({ user_id: "bob" })
  })
  it("retries roster errors without issuing a create request", async () => {
    fetcher.mockResolvedValueOnce(new Response(null, { status: 503 }))
    setup()
    fireEvent.click(await screen.findByRole("button", { name: "Retry loading people" }))
    await screen.findByRole("button", { name: /Bob/ })
    expect(fetcher.mock.calls.every(([, options]) => !options?.method)).toBe(true)
  })
  it("retries an ambiguous open failure with the same pair and uses the returned existing ID", async () => {
    let attempts = 0
    fetcher.mockImplementation(async (url: string) => {
      if (url.includes("/members")) return Response.json(people)
      attempts++
      return attempts === 1 ? new Response(null, { status: 503 }) : Response.json(direct)
    })
    const { onOpened } = setup()
    fireEvent.click(await screen.findByRole("button", { name: /Bob/ }))
    await screen.findByRole("alert")
    expect(onOpened).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: /Bob/ }))
    await waitFor(() => expect(onOpened).toHaveBeenCalledWith(direct))
    const requests = fetcher.mock.calls.filter(([url]) => url.includes("/direct"))
    expect(requests).toHaveLength(2)
    expect(requests.map(([, options]) => JSON.parse(options.body))).toEqual([{ user_id: "bob" }, { user_id: "bob" }])
  })
  it("does not navigate when an in-flight picker is dismissed", async () => {
    let complete!: (response: Response) => void
    fetcher.mockImplementation(async (url: string) => url.includes("/members") ? Response.json(people) : new Promise<Response>((resolve) => { complete = resolve }))
    const { onOpened, unmount } = setup()
    fireEvent.click(await screen.findByRole("button", { name: /Bob/ }))
    unmount()
    complete(Response.json(direct))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(onOpened).not.toHaveBeenCalled()
  })
})
