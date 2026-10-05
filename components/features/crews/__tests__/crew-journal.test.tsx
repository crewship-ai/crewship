import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { CrewJournal } from "../crew-journal"
import { apiFetch } from "@/lib/api-fetch"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { toast } from "sonner"
import type { JournalEntry } from "@/lib/types/journal"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-journal-stream", () => ({ useJournalStream: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() } }))
const fetchMock = vi.mocked(apiFetch)
const entry: JournalEntry = { id: "event", workspace_id: "w", ts: "2026-10-03T00:00:00Z", entry_type: "run.started", severity: "info", actor_type: "system", summary: "Agent began working" }
let entries: JournalEntry[]
let summarize: () => Promise<Response>
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { resolve, promise } }
beforeEach(() => {
  entries = []
  summarize = () => Promise.resolve(new Response(null, { status: 202 }))
  fetchMock.mockReset().mockImplementation((url) => String(url).includes("/summarize") ? summarize() : Promise.resolve(new Response(JSON.stringify({ entries, next_cursor: null }))))
  vi.mocked(useJournalStream).mockClear()
})
afterEach(cleanup)
it("loads a scoped 24-hour window, shares stable stream filters and renders live entries", async () => {
  entries = [entry]
  const { rerender } = render(<CrewJournal crewId="crew one" workspaceId="w&one" />)
  expect(await screen.findByText(entry.summary)).toBeInTheDocument()
  const url = new URL(String(fetchMock.mock.calls[0][0]), "http://test")
  expect(url.searchParams.get("workspace_id")).toBe("w&one")
  expect(url.searchParams.get("crew_id")).toBe("crew one")
  expect(url.searchParams.get("limit")).toBe("30")
  expect(Math.abs(Date.now() - Date.parse(url.searchParams.get("since")!) - 86400000)).toBeLessThan(5000)
  const stream = vi.mocked(useJournalStream).mock.lastCall![0]
  rerender(<CrewJournal crewId="crew one" workspaceId="w&one" />)
  expect(vi.mocked(useJournalStream).mock.lastCall![0].params).toBe(stream.params)
  act(() => stream.onEntry({ ...entry, id: "new", summary: "Live update" }))
  expect(screen.getByText("Live update")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "View full journal" })).toHaveAttribute("href", "/journal?crew_id=crew%20one")
})
it("distinguishes initial loading from an empty window", async () => {
  const list = deferred<Response>()
  fetchMock.mockReturnValueOnce(list.promise)
  render(<CrewJournal crewId="c" workspaceId="w" />)
  expect(screen.getByText("Loading journal…")).toBeInTheDocument()
  await act(async () => { list.resolve(new Response('{"entries":[]}')) })
  expect(screen.getByText("No events in the last 24 hours.")).toBeInTheDocument()
})
it("caps visible history and keeps unknown severity readable", async () => {
  entries = Array.from({ length: 31 }, (_, i) => ({ ...entry, id: String(i), summary: `Event ${i}`, severity: ["info", "notice", "warn", "error", "future"][i % 5] }))
  render(<CrewJournal crewId="c" workspaceId="w" />)
  expect(await screen.findByText("Event 29")).toBeInTheDocument()
  expect(screen.queryByText("Event 30")).not.toBeInTheDocument()
  expect(screen.getAllByText("future")[0]).toHaveClass("text-info")
})
it("starts a summary once, disables duplicate clicks, and refreshes the journal after acceptance", async () => {
  const pending = deferred<Response>()
  summarize = () => pending.promise
  render(<CrewJournal crewId="c/a" workspaceId="w&" />)
  await screen.findByText("No events in the last 24 hours.")
  const button = screen.getByRole("button", { name: "Generate Summary" })
  fireEvent.click(button); fireEvent.click(button)
  expect(button).toBeDisabled()
  expect(fetchMock.mock.calls.filter(([url]) => String(url).includes("/summarize"))).toHaveLength(1)
  expect(fetchMock).toHaveBeenCalledWith("/api/v1/crews/c%2Fa/journal/summarize?workspace_id=w%26", expect.objectContaining({ method: "POST" }))
  entries = [{ ...entry, summary: "Generated summary" }]
  await act(async () => { pending.resolve(new Response(null, { status: 202 })) })
  expect(await screen.findByText("Generated summary")).toBeInTheDocument()
  expect(toast.success).toHaveBeenCalledWith("Summary generation started")
  expect(button).toBeEnabled()
})
it.each([404, 403, 500])("reports summary refusal %s and allows retry without reloading the journal", async status => {
  summarize = () => Promise.resolve(new Response(null, { status }))
  render(<CrewJournal crewId="c" workspaceId="w" />)
  await screen.findByText("No events in the last 24 hours.")
  fireEvent.click(screen.getByRole("button", { name: "Generate Summary" }))
  await waitFor(() => expect(screen.getByRole("button", { name: "Generate Summary" })).toBeEnabled())
  if (status === 404) expect(toast.info).toHaveBeenCalledWith("Summary generation not yet available")
  else expect(toast.error).toHaveBeenCalledWith(`Summary failed (${status})`)
  expect(fetchMock.mock.calls.filter(([url]) => String(url).startsWith("/api/v1/journal?"))).toHaveLength(1)
})
it.each([new Error("offline"), "unknown"])("reports a transport refusal without claiming summary success", async error => {
  summarize = () => Promise.reject(error)
  render(<CrewJournal crewId="c" workspaceId="w" />)
  await screen.findByText("No events in the last 24 hours.")
  fireEvent.click(screen.getByRole("button", { name: "Generate Summary" }))
  await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Summary failed", { description: error instanceof Error ? "offline" : undefined }))
  expect(toast.success).not.toHaveBeenCalled()
})
it("lets a new crew start a summary while an abandoned crew request is pending", async () => {
  const pending = deferred<Response>()
  summarize = () => pending.promise
  const { rerender } = render(<CrewJournal crewId="old" workspaceId="w" />)
  await screen.findByText("No events in the last 24 hours.")
  fireEvent.click(screen.getByRole("button", { name: "Generate Summary" }))
  rerender(<CrewJournal crewId="new" workspaceId="w" />)
  await screen.findByText("No events in the last 24 hours.")
  expect(screen.getByRole("button", { name: "Generate Summary" })).toBeEnabled()
})
it.each([202, 500])("ignores abandoned summary response %s without reloading the old crew", async status => {
  const pending = deferred<Response>()
  summarize = () => pending.promise
  const { rerender } = render(<CrewJournal crewId="old" workspaceId="w" />)
  await screen.findByText("No events in the last 24 hours.")
  fireEvent.click(screen.getByRole("button", { name: "Generate Summary" }))
  rerender(<CrewJournal crewId="new" workspaceId="new-workspace" />)
  await screen.findByText("No events in the last 24 hours.")
  const calls = fetchMock.mock.calls.length
  await act(async () => { pending.resolve(new Response(null, { status })) })
  expect(toast.success).not.toHaveBeenCalled()
  expect(toast.error).not.toHaveBeenCalled()
  expect(fetchMock).toHaveBeenCalledTimes(calls)
})
it("aborts an abandoned summary and suppresses its transport rejection", async () => {
  let reject!: (error: Error) => void
  summarize = () => new Promise<Response>((_, rejectPromise) => { reject = rejectPromise })
  const { unmount } = render(<CrewJournal crewId="c" workspaceId="w" />)
  await screen.findByText("No events in the last 24 hours.")
  fireEvent.click(screen.getByRole("button", { name: "Generate Summary" }))
  const request = fetchMock.mock.calls.find(([url]) => String(url).includes("/summarize"))!
  const signal = request[1]?.signal
  expect(signal?.aborted).toBe(false)
  unmount()
  expect(signal?.aborted).toBe(true)
  await act(async () => { reject(new Error("request aborted")) })
  expect(toast.error).not.toHaveBeenCalled()
  expect(toast.success).not.toHaveBeenCalled()
})
