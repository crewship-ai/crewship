import { afterEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { SharedRoutineReview } from "../shared-routine-review"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
const snapshot = { draft_id: "d1", revision: 3, base_revision: 2 }
const receipt = { ...snapshot, id: "review1", routine_id: "r1", state: "pending", published_version: 0 }
const draftKey = ["shared-routine-draft", "guest", "/api/draft"]
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function show(dirty = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  client.setQueryData(draftKey, { ...snapshot, name: "Local content" })
  render(<QueryClientProvider client={client}><SharedRoutineReview url="/api/draft" draftKey={draftKey} snapshot={snapshot} dirty={dirty} saving={false} /></QueryClientProvider>)
  return client
}
afterEach(() => { cleanup(); api.mockReset() })

it("submits only the exact saved snapshot and displays a pending receipt without a fake version", async () => {
  api.mockImplementation(async (_url: string, init?: RequestInit) => response(init?.method === "POST" ? receipt : null, init?.method === "POST" ? 202 : 200))
  show()
  const button = screen.getByRole("button", { name: "Request publication review" })
  await waitFor(() => expect(button).toBeEnabled())
  fireEvent.click(button)
  await screen.findByText("Awaiting publication review · Draft revision 3")
  const posts = api.mock.calls.filter(([, init]) => init?.method === "POST")
  expect(posts).toHaveLength(1)
  expect(JSON.parse(posts[0][1].body)).toEqual(snapshot)
  expect(screen.queryByText(/Published version/)).toBeNull()
  expect(button).toBeDisabled()
})

it("requires saving local changes before a request", async () => {
  api.mockImplementation(async () => response(null))
  show(true)
  await screen.findByText("Save your changes before requesting publication review.")
  expect(screen.getByRole("button")).toBeDisabled()
  expect(api.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
})

it("retains the last good receipt and draft on a transient refresh error", async () => {
  let status = 200
  api.mockImplementation(async () => response(receipt, status))
  const client = show()
  await screen.findByText("Awaiting publication review · Draft revision 3")
  status = 500
  await act(async () => { await client.invalidateQueries({ queryKey: ["shared-routine-review"] }) })
  expect(screen.getByText("Awaiting publication review · Draft revision 3")).toBeInTheDocument()
  expect(client.getQueryData(draftKey)).toMatchObject(snapshot)
})

it.each([401, 403, 404])("clears draft content and review receipts after hard access loss %s", async status => {
  api.mockImplementation(async () => response({}, status))
  const client = show()
  await waitFor(() => expect(client.getQueryData(draftKey)).toBeNull())
  expect(client.getQueryData(["shared-routine-review", ...draftKey])).toBeUndefined()
})

it("keeps edits after a 409 and never silently retries a request", async () => {
  api.mockImplementation(async (_url: string, init?: RequestInit) => response(init?.method === "POST" ? {} : null, init?.method === "POST" ? 409 : 200))
  const client = show()
  const button = screen.getByRole("button")
  await waitFor(() => expect(button).toBeEnabled())
  fireEvent.click(button)
  await screen.findByText(/This saved revision can no longer be submitted/)
  expect(client.getQueryData(draftKey)).toMatchObject(snapshot)
  expect(api.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1)
})

it("shows an actual published version and requires a new revision after a terminal decision", async () => {
  api.mockImplementation(async () => response({ ...receipt, state: "published", published_version: 4 }))
  show()
  await screen.findByText("Published · Draft revision 3 · Published version 4")
  expect(screen.getByRole("button")).toBeDisabled()
  expect(screen.getByText("Save a new draft revision before requesting another review.")).toBeInTheDocument()
})

it("rejects a receipt that falsely claims publication without a version", async () => {
  api.mockImplementation(async () => response({ ...receipt, state: "published" }))
  show()
  await screen.findByRole("alert")
  expect(screen.queryByText(/Published ·/)).toBeNull()
})

it("refuses a successful response for a different saved snapshot", async () => {
  api.mockImplementation(async (_url: string, init?: RequestInit) => response(init?.method === "POST" ? { ...receipt, revision: 4 } : null, init?.method === "POST" ? 202 : 200))
  const client = show()
  const button = screen.getByRole("button")
  await waitFor(() => expect(button).toBeEnabled())
  fireEvent.click(button)
  await screen.findByText("The publication review did not match this saved revision.")
  expect(client.getQueryData(["shared-routine-review", ...draftKey])).toBeNull()
  expect(screen.queryByText(/Awaiting publication review/)).toBeNull()
  expect(client.getQueryData(draftKey)).toMatchObject(snapshot)
})
