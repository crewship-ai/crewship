import { afterEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { SharedRoutineDraft } from "../shared-routine-draft"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
const draft = { draft_id: "d1", revision: 3, base_revision: 2, name: "Client draft", description: "Shared notes", definition: { steps: [], name: "Candidate" }, can_edit: true }
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<QueryClientProvider client={client}><SharedRoutineDraft url="/api/v1/workspaces/w/restricted-routine-library/r/draft" userId="guest" /></QueryClientProvider>)
  fireEvent.click(screen.getByRole("button", { name: "Edit shared draft" }))
  return client
}
afterEach(() => { cleanup(); api.mockReset() })

it("discards local changes after an explicit reload with the same server revision", async () => {
  api.mockImplementation(async () => response(draft))
  show()
  fireEvent.change(await screen.findByRole("textbox", { name: "Draft name" }), { target: { value: "Unsaved name" } })
  fireEvent.change(screen.getByRole("textbox", { name: "Draft description" }), { target: { value: "Unsaved description" } })
  fireEvent.change(screen.getByRole("textbox", { name: "Draft definition JSON" }), { target: { value: "{}" } })
  fireEvent.click(screen.getByRole("button", { name: "Reload draft and discard local changes" }))
  await waitFor(() => expect(screen.getByRole("textbox", { name: "Draft name" })).toHaveValue(draft.name))
  expect(screen.getByRole("textbox", { name: "Draft description" })).toHaveValue(draft.description)
  expect(screen.getByRole("textbox", { name: "Draft definition JSON" })).toHaveValue(JSON.stringify(draft.definition, null, 2))
})

it("retains local changes when an explicit reload fails", async () => {
  let status = 200
  api.mockImplementation(async () => response(draft, status))
  show()
  fireEvent.change(await screen.findByRole("textbox", { name: "Draft name" }), { target: { value: "Unsaved name" } })
  status = 500
  fireEvent.click(screen.getByRole("button", { name: "Reload draft and discard local changes" }))
  await screen.findByText("Could not refresh the shared draft. Your local changes are retained.")
  expect(screen.getByRole("textbox", { name: "Draft name" })).toHaveValue("Unsaved name")
})

it("retains local changes on a background refresh with the same server revision", async () => {
  api.mockImplementation(async () => response(draft))
  const client = show()
  fireEvent.change(await screen.findByRole("textbox", { name: "Draft name" }), { target: { value: "Unsaved name" } })
  await act(async () => { await client.invalidateQueries({ queryKey: ["shared-routine-draft"] }) })
  expect(screen.getByRole("textbox", { name: "Draft name" })).toHaveValue("Unsaved name")
})

it("saves only the reviewed draft snapshot and content without publishing", async () => {
  api.mockImplementation(async (_url: string, init?: RequestInit) => response(init?.method === "PUT" ? { ...draft, name: "New name", revision: 4 } : draft))
  show()
  fireEvent.change(await screen.findByRole("textbox", { name: "Draft name" }), { target: { value: "New name" } })
  fireEvent.click(screen.getByRole("button", { name: "Save shared draft" }))
  await screen.findByText("Draft revision 4 · Based on publication 2")
  const writes = api.mock.calls.filter(([, init]) => init?.method === "PUT")
  expect(writes).toHaveLength(1)
  expect(JSON.parse(String(writes[0][1].body))).toEqual({ draft_id: "d1", revision: 3, base_revision: 2, name: "New name", description: "Shared notes", definition: draft.definition })
  expect(api.mock.calls.every(([url]) => url.endsWith("/draft"))).toBe(true)
  expect(screen.queryByRole("button", { name: /publish|run/i })).toBeNull()
})

it("retains local work on conflict without retrying or fetching a new snapshot", async () => {
  api.mockImplementation(async (_url: string, init?: RequestInit) => init?.method === "PUT" ? response({}, 409) : response(draft))
  show()
  fireEvent.change(await screen.findByRole("textbox", { name: "Draft name" }), { target: { value: "Local unsaved work" } })
  fireEvent.click(screen.getByRole("button", { name: "Save shared draft" }))
  await screen.findByText(/Another editor or publication changed/)
  expect(screen.getByRole("textbox", { name: "Draft name" })).toHaveValue("Local unsaved work")
  expect(api).toHaveBeenCalledTimes(2)
})

it("keeps a local edit on transient refresh failure and clears it on hard access loss", async () => {
  let status = 200
  api.mockImplementation(async () => response(draft, status))
  const client = show()
  fireEvent.change(await screen.findByRole("textbox", { name: "Draft name" }), { target: { value: "Local unsaved work" } })
  status = 500
  await act(async () => { await client.invalidateQueries({ queryKey: ["shared-routine-draft"] }) })
  expect(screen.getByRole("textbox", { name: "Draft name" })).toHaveValue("Local unsaved work")
  status = 404
  await act(async () => { await client.invalidateQueries({ queryKey: ["shared-routine-draft"] }) })
  await waitFor(() => expect(screen.queryByRole("textbox", { name: "Draft name" })).toBeNull())
  expect(screen.getByText("Draft access is no longer available.")).toBeInTheDocument()
})

it("rejects malformed definitions before sending a mutation", async () => {
  api.mockResolvedValue(response(draft))
  show()
  fireEvent.change(await screen.findByRole("textbox", { name: "Draft definition JSON" }), { target: { value: "[]" } })
  fireEvent.click(screen.getByRole("button", { name: "Save shared draft" }))
  await screen.findByText("Definition must be a JSON object.")
  expect(api).toHaveBeenCalledTimes(1)
})

it("offers no form for a credential without mutation scope", async () => {
  api.mockResolvedValue(response({ ...draft, can_edit: false }))
  show()
  await screen.findByText("This credential cannot edit the draft.")
  expect(screen.queryByRole("button", { name: "Save shared draft" })).toBeNull()
})
