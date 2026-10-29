import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { RunInDocument } from "../run-in-document"

const identity = vi.hoisted(() => ({ id: "original-human" }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: identity.id } } }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
const props = { workspaceId: "workspace", chatId: "chat", messageId: "stored-original-message", authorId: "original-human" }
const row = { page_id: "doc", title: "Private document title", application_id: "wordcraft", document_grant: "write", policy_allows: true }
const response = (rows = [row]) => new Response(JSON.stringify({ documents: rows }), { status: 200 })
async function settle() { await act(async () => { await vi.advanceTimersByTimeAsync(0) }) }
beforeEach(() => { identity.id = "original-human"; apiFetch.mockReset(); vi.useFakeTimers(); Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true }) })
afterEach(() => { cleanup(); vi.useRealTimers() })

it("has no action or metadata reads for a different author", () => {
  render(<RunInDocument {...props} authorId="another-human" />)
  expect(screen.queryByRole("button", { name: "Run in this Doc" })).toBeNull()
  expect(apiFetch).not.toHaveBeenCalled()
})

it("selects only existing WordCraft write delegation and submits the original stored message", async () => {
  apiFetch.mockResolvedValueOnce(response([row, { ...row, page_id: "read", title: "Read only", document_grant: "read" }, { ...row, page_id: "other-app", title: "Other application", application_id: "gridcraft" }])).mockResolvedValue(new Response("{}", { status: 202 }))
  render(<RunInDocument {...props} />)
  expect(apiFetch).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "Run in this Doc" })); await settle()
  expect(screen.queryByText("Read only")).toBeNull(); expect(screen.queryByText("Other application")).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: row.title })); await settle()
  const [url, options] = apiFetch.mock.calls[1]
  expect(url).toBe("/api/v1/pages/%40doc/mooring/tasks?workspace_id=workspace")
  expect(JSON.parse(options.body)).toEqual({ chat_id: "chat", message_id: "stored-original-message" })
  expect(screen.getByRole("status")).toHaveTextContent("Task requested")
  expect(screen.queryByText(/^Saved$/)).toBeNull()
})

it("removes titles on a denied refresh and rejects a delayed response after account switch", async () => {
  let complete!: (value: Response) => void
  apiFetch.mockResolvedValueOnce(response()).mockImplementationOnce(() => new Promise(resolve => { complete = resolve }))
  const view = render(<RunInDocument {...props} />)
  fireEvent.click(screen.getByRole("button", { name: "Run in this Doc" })); await settle()
  expect(screen.getByText(row.title)).toBeInTheDocument()
  await act(async () => { await vi.advanceTimersByTimeAsync(1500) })
  identity.id = "different-human"; view.rerender(<RunInDocument {...props} />)
  expect(screen.queryByText(row.title)).toBeNull()
  await act(async () => { complete(response()); await Promise.resolve() })
  expect(screen.queryByText(row.title)).toBeNull(); expect(screen.queryByRole("dialog")).toBeNull()
})

it("hides protected names immediately after a refused current read", async () => {
  apiFetch.mockResolvedValueOnce(response()).mockResolvedValue(new Response("{}", { status: 403 }))
  render(<RunInDocument {...props} />)
  fireEvent.click(screen.getByRole("button", { name: "Run in this Doc" })); await settle()
  expect(screen.getByText(row.title)).toBeInTheDocument()
  await act(async () => { await vi.advanceTimersByTimeAsync(1500) })
  expect(apiFetch).toHaveBeenCalledTimes(2)
  // Flush React Query's batched observer notification before the 3 s expiry.
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  expect(screen.queryByText(row.title)).toBeNull()
})

it("expires metadata when the current read hangs without a successful response", async () => {
  apiFetch.mockResolvedValueOnce(response()).mockImplementation(() => new Promise(() => {}))
  render(<RunInDocument {...props} />)
  fireEvent.click(screen.getByRole("button", { name: "Run in this Doc" })); await settle()
  await act(async () => { await vi.advanceTimersByTimeAsync(3001) })
  expect(screen.queryByText(row.title)).toBeNull()
})


it("clears protected picker metadata while hidden and rechecks before showing it again", async () => {
  apiFetch.mockResolvedValueOnce(response()).mockImplementation(() => new Promise(() => {}))
  render(<RunInDocument {...props} />)
  fireEvent.click(screen.getByRole("button", { name: "Run in this Doc" })); await settle()
  expect(screen.getByText(row.title)).toBeInTheDocument()
  Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true })
  fireEvent(document, new Event("visibilitychange")); await settle()
  expect(screen.queryByText(row.title)).toBeNull()
  const calls = apiFetch.mock.calls.length
  await act(async () => { await vi.advanceTimersByTimeAsync(3001) })
  expect(apiFetch).toHaveBeenCalledTimes(calls)
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true })
  fireEvent(document, new Event("visibilitychange")); await settle()
  expect(screen.queryByText(row.title)).toBeNull()
})
