import { afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { SharedRoutineResults } from "../shared-routine-results"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
afterEach(() => { cleanup(); api.mockReset() })
const run = { run_id: "run-1", routine_id: "routine-1", status: "completed", mode: "run", started_at: "2026-10-15T00:00:00Z", ended_at: null }
const result = { ...run, output: "FINAL_RESULT_CANARY", step_outputs: { answer: "STEP_RESULT_CANARY" } }
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function show() { const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); return render(<QueryClientProvider client={client}><SharedRoutineResults workspaceId="w" userId="u" routineId="routine-1" /></QueryClientProvider>) }
async function select() { fireEvent.click(await screen.findByRole("button", { name: /Completed.*· run-1$/ })); await screen.findByText("FINAL_RESULT_CANARY") }

it("requests outputs only after choosing an explicitly visible run", async () => {
  api.mockImplementation(async (url: string) => response(url.endsWith("/run-1") ? result : [run]))
  show()
  await screen.findByRole("button", { name: /Completed.*· run-1$/ })
  expect(api.mock.calls).toHaveLength(1)
  expect(screen.queryByText("FINAL_RESULT_CANARY")).toBeNull()
  await select()
  expect(screen.getByText("STEP_RESULT_CANARY")).toBeInTheDocument()
  expect(api.mock.calls.every(([, options]) => !options?.method)).toBe(true)
})
it.each([401, 403, 404])("removes outputs after hard access loss (%d)", async status => {
  let revoked = false
  api.mockImplementation(async (url: string) => revoked ? response({}, status) : response(url.endsWith("/run-1") ? result : [run]))
  show(); await select(); revoked = true
  fireEvent.click(screen.getByRole("button", { name: "Refresh runs" }))
  await waitFor(() => expect(screen.queryByText("FINAL_RESULT_CANARY")).toBeNull())
  expect(screen.queryByText("STEP_RESULT_CANARY")).toBeNull()
  expect(screen.getByRole("alert")).toBeInTheDocument()
})
it("retains the last successful result on a transient refresh error", async () => {
  let unavailable = false
  api.mockImplementation(async (url: string) => unavailable ? response({}, 503) : response(url.endsWith("/run-1") ? result : [run]))
  show(); await select(); unavailable = true
  fireEvent.click(screen.getByRole("button", { name: "Refresh runs" }))
  await screen.findAllByText(/Could not refresh/)
  expect(screen.getByText("FINAL_RESULT_CANARY")).toBeInTheDocument()
})
it("rejects a result for another run or routine", async () => {
  api.mockImplementation(async (url: string) => response(url.endsWith("/run-1") ? { ...result, routine_id: "another", output: "FOREIGN_RESULT_CANARY" } : [run]))
  show(); fireEvent.click(await screen.findByRole("button", { name: /Completed.*· run-1$/ }))
  await screen.findByText("The selected result could not be confirmed.")
  expect(screen.queryByText("FOREIGN_RESULT_CANARY")).toBeNull()
})
it("rejects foreign routine rows before offering selection", async () => {
  api.mockResolvedValue(response([{ ...run, routine_id: "other", run_id: "FOREIGN_RUN_CANARY" }]))
  show(); await screen.findByText("Run history could not be confirmed.")
  expect(screen.queryByText(/FOREIGN_RUN_CANARY/)).toBeNull()
})
it("clears the selected output when moving to another page", async () => {
  api.mockImplementation(async (url: string) => response(url.endsWith("/run-1") ? result : url.includes("offset=25") ? [] : Array.from({length:25},(_,i)=>({...run,run_id:i===0?"run-1":`run-${i+1}`}))))
  show(); await select()
  fireEvent.click(screen.getByRole("button", { name: "Next runs" }))
  await screen.findByText("No visible runs on this page.")
  expect(screen.queryByText("FINAL_RESULT_CANARY")).toBeNull()
  expect(api.mock.calls.some(([url]) => url.endsWith("?limit=25&offset=25"))).toBe(true)
})

it("explains an oversized selected result", async () => {
  api.mockImplementation(async (url: string) => response(url.endsWith("/run-1") ? {} : [run], url.endsWith("/run-1") ? 413 : 200))
  show(); fireEvent.click(await screen.findByRole("button", { name: /Completed.*· run-1$/ }))
  await screen.findByText("This result exceeds the shared result size limit.")
  expect(screen.queryByText("FINAL_RESULT_CANARY")).toBeNull()
  expect(screen.getByText(/Inputs are not shown directly; outputs may contain them/)).toBeInTheDocument()
})
