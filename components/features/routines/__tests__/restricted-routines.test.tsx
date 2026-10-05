import React from "react"
import { afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { RestrictedRoutines } from "../restricted-routines"
import RoutinesPage from "@/app/(dashboard)/routines/page"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
const workspace = vi.hoisted(() => ({ mode: "restricted" }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "workspace", workspace: { currentUserAccessMode: workspace.mode }, workspaces: [{ id: "workspace", currentUserAccessMode: workspace.mode }], loading: false }) }))
vi.mock("../routines-layout", () => ({ RoutinesLayout: () => <div>Trusted routine editor</div> }))
afterEach(() => { cleanup(); api.mockReset(); vi.restoreAllMocks(); workspace.mode = "restricted" })
const catalog = [{ slug: "allowed", name: "Allowed routine", definition_hash: "frozen-hash", execution_hash: "compiled-graph-hash", inputs: [{ name: "task", type: "string", required: true }] }]
function response(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status }) }

it("invokes only the admitted routine and polls its private receipt", async () => {
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/restricted-routines")) return response(catalog)
    if (init?.method === "POST") return response({ run_id: "private-run", status: "SCHEDULED" }, 202)
    if (url.endsWith("/restricted-routine-runs/private-run")) return response({ run_id: "private-run", status: "completed", step_outputs: { answer: "OWN_RESULT_CANARY" } })
    throw new Error(`Unexpected shared request ${url}`)
  })
  render(<RestrictedRoutines workspaceId="workspace" />)
  await screen.findByRole("option", { name: "Allowed routine" })
  fireEvent.change(screen.getByRole("combobox"), { target: { value: "allowed" } })
  fireEvent.change(screen.getByRole("textbox", { name: "task" }), { target: { value: "private input" } })
  fireEvent.click(screen.getByRole("button", { name: "Run routine" }))
  await screen.findByText("OWN_RESULT_CANARY")
  const submit = api.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(submit[0]).toBe("/api/v1/workspaces/workspace/pipelines/allowed/run")
  expect(JSON.parse(submit[1].body)).toEqual({ inputs: { task: "private input" }, expected_definition_hash: "frozen-hash", expected_execution_hash: "compiled-graph-hash" })
  expect(submit[1].headers["Idempotency-Key"]).toBeTruthy()
  expect(api.mock.calls.some(([url]) => url.includes("/journal") || url.endsWith("/pipelines") || url.includes("/agents"))).toBe(false)
})

it("retries an unknown submission with the same frozen request and hides revoked output", async () => {
  let submitted = 0
  let polls = 0
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/restricted-routines")) return response(catalog)
    if (init?.method === "POST") { if (++submitted === 1) throw new TypeError("network disconnected"); return response({ run_id: "private-run", status: "DEDUPED" }, 202) }
    if (url.endsWith("/restricted-routine-runs/private-run")) { if (++polls === 1) return response({ run_id: "private-run", status: "running", step_outputs: { answer: "REVOKED_RESULT_CANARY" } }); return response({ error: "unavailable" }, 404) }
    throw new Error("Unexpected shared request")
  })
  render(<RestrictedRoutines workspaceId="workspace" />)
  await screen.findByRole("option", { name: "Allowed routine" })
  fireEvent.change(screen.getByRole("combobox"), { target: { value: "allowed" } })
  fireEvent.change(screen.getByRole("textbox", { name: "task" }), { target: { value: "private input" } })
  fireEvent.click(screen.getByRole("button", { name: "Run routine" }))
  await screen.findByText(/Could not confirm the outcome/)
  fireEvent.click(screen.getByRole("button", { name: "Retry request" }))
  await screen.findByText("REVOKED_RESULT_CANARY")
  await screen.findByText("This run is no longer available.", {}, { timeout: 3000 })
  await waitFor(() => expect(screen.queryByText("REVOKED_RESULT_CANARY")).toBeNull())
  const submits = api.mock.calls.filter(([, init]) => init?.method === "POST")
  expect(submits[0][1].body).toBe(submits[1][1].body)
  expect(submits[0][1].headers["Idempotency-Key"]).toBe(submits[1][1].headers["Idempotency-Key"])
})

it("ordinary routines page automatically selects the server membership profile", async () => {
  api.mockResolvedValue(response(catalog))
  const mounted = render(<RoutinesPage />)
  await screen.findByRole("option", { name: "Allowed routine" })
  expect(screen.queryByText("Trusted routine editor")).toBeNull()
  expect(api).toHaveBeenCalledWith("/api/v1/workspaces/workspace/restricted-routines", expect.anything())
  mounted.unmount()
  workspace.mode = "trusted"
  render(<RoutinesPage />)
  expect(screen.getByText("Trusted routine editor")).toBeTruthy()
})

it("shows interrupted work as needing review without another model wake", async () => {
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/restricted-routines")) return response(catalog)
    if (init?.method === "POST") return response({ run_id: "private-run", status: "SCHEDULED" }, 202)
    if (url.endsWith("/restricted-routine-runs/private-run")) return response({ run_id: "private-run", status: "needs_reconciliation", step_outputs: {} })
    throw new Error(`Unexpected shared request ${url}`)
  })
  render(<RestrictedRoutines workspaceId="workspace" />)
  await screen.findByRole("option", { name: "Allowed routine" })
  fireEvent.change(screen.getByRole("combobox"), { target: { value: "allowed" } })
  fireEvent.change(screen.getByRole("textbox", { name: "task" }), { target: { value: "private input" } })
  fireEvent.click(screen.getByRole("button", { name: "Run routine" }))
  await screen.findByText("Status: Needs review")
  expect(screen.getByText(/It will not run again automatically/)).toBeInTheDocument()
  expect(api.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1)
})
