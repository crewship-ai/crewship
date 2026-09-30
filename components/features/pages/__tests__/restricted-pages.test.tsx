import React from "react"
import { afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { RestrictedPages } from "../restricted-pages"
import PagesPage from "@/app/(dashboard)/pages/page"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
const workspace = vi.hoisted(() => ({ mode: "restricted" }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "workspace", workspace: { currentUserAccessMode: workspace.mode }, loading: false }) }))
vi.mock("../pages-layout", () => ({ PagesLayout: () => <div>Trusted Page editor</div> }))
afterEach(() => { cleanup(); api.mockReset(); vi.restoreAllMocks(); Reflect.deleteProperty(window, "confirm"); workspace.mode = "restricted" })
const catalog = [{ slug: "allowed", name: "Allowed Page", publication: 7, actions: [{ panel_id: "panel", id: "work", label: "Allowed action", inputs: [{ name: "task", type: "text", required: true }], confirm: { title: "Confirm work", body: "Run this declared action?" } }] }]
const key = JSON.stringify(["allowed", "panel", "work"])
function response(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }) }

it("submits a publication-bound declared action after host confirmation and polls only its private result", async () => {
  const confirm = vi.fn(() => true)
  Object.defineProperty(window, "confirm", { value: confirm, configurable: true })
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/restricted-pages")) return response(catalog)
    if (init?.method === "POST") return response({ run_id: "private-run", status: "pending" }, 202)
    if (url.endsWith("/restricted-routine-runs/private-run")) return response({ run_id: "private-run", status: "completed", step_outputs: { answer: "OWN_PAGE_RESULT" } })
    throw new Error("Unexpected shared request")
  })
  render(<RestrictedPages workspaceId="workspace" />)
  await screen.findByRole("option", { name: "Allowed Page — Allowed action" })
  fireEvent.change(screen.getByRole("combobox", { name: "Action" }), { target: { value: key } })
  fireEvent.change(screen.getByRole("textbox", { name: "task" }), { target: { value: "private input" } })
  fireEvent.click(screen.getByRole("button", { name: "Run action" }))
  await screen.findByText("OWN_PAGE_RESULT")
  expect(confirm).toHaveBeenCalledWith("Confirm work\n\nRun this declared action?")
  const submit = api.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(submit[0]).toBe("/api/v1/pages/allowed/application/actions/panel/work")
  expect(JSON.parse(submit[1].body)).toEqual({ inputs: { task: "private input" }, publication: 7 })
  expect(submit[1].headers["Idempotency-Key"]).toBeTruthy()
  expect(api.mock.calls.some(([url]) => url.includes("/journal") || url.includes("/assets") || url.endsWith("/pages") || url.includes("/panels/panel/data"))).toBe(false)
})

it("retries the same frozen Page admission and clears a revoked private result", async () => {
  Object.defineProperty(window, "confirm", { value: vi.fn(() => true), configurable: true })
  let submitted = 0
  let polls = 0
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/restricted-pages")) return response(catalog)
    if (init?.method === "POST") { if (++submitted === 1) throw new TypeError("network disconnected"); return response({ run_id: "private-run", status: "pending" }, 202) }
    if (url.endsWith("/restricted-routine-runs/private-run")) { if (++polls === 1) return response({ run_id: "private-run", status: "running", step_outputs: { answer: "REVOKED_PAGE_RESULT" } }); return response({ error: "unavailable" }, 404) }
    throw new Error("Unexpected shared request")
  })
  render(<RestrictedPages workspaceId="workspace" />)
  await screen.findByRole("option", { name: "Allowed Page — Allowed action" })
  fireEvent.change(screen.getByRole("combobox", { name: "Action" }), { target: { value: key } })
  fireEvent.change(screen.getByRole("textbox", { name: "task" }), { target: { value: "private input" } })
  fireEvent.click(screen.getByRole("button", { name: "Run action" }))
  await screen.findByText(/Could not confirm the outcome/)
  fireEvent.click(screen.getByRole("button", { name: "Retry request" }))
  await screen.findByText("REVOKED_PAGE_RESULT")
  await screen.findByText("This run is no longer available.", {}, { timeout: 3000 })
  await waitFor(() => expect(screen.queryByText("REVOKED_PAGE_RESULT")).toBeNull())
  const submits = api.mock.calls.filter(([, init]) => init?.method === "POST")
  expect(submits[0][1].body).toBe(submits[1][1].body)
  expect(submits[0][1].headers["Idempotency-Key"]).toBe(submits[1][1].headers["Idempotency-Key"])
})

it("ordinary Pages automatically selects the server membership mode", async () => {
  api.mockResolvedValue(response([]))
  render(<PagesPage />)
  await screen.findByText("No Page actions are available with your current access.")
  expect(screen.queryByText("Trusted Page editor")).toBeNull()
  cleanup()
  workspace.mode = "trusted"
  render(<PagesPage />)
  expect(screen.getByText("Trusted Page editor")).toBeTruthy()
})
