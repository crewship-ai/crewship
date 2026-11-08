import React from "react"
import { afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { RestrictedArtifacts } from "../restricted-artifacts"
import PagesPage from "@/app/(dashboard)/pages/page"
import { answerConfirm, mountConfirmHost } from "@/components/ui/__tests__/confirm-test-utils"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api, broadcastSessionExpired: vi.fn() }))
const workspace = vi.hoisted(() => ({ mode: "restricted" }))
vi.mock("@/hooks/use-workspace", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/hooks/use-workspace")>(),
  useWorkspace: () => ({ workspaceId: "workspace", workspace: { currentUserAccessMode: workspace.mode }, workspaces: [{ id: "workspace", currentUserAccessMode: workspace.mode }], loading: false }),
}))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: () => <div>Trusted Page editor</div> }))
const nav = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ replace: nav.replace, push: vi.fn() }), useSearchParams: () => new URLSearchParams(), usePathname: () => window.location.pathname }))
function Allowed() { return <RestrictedArtifacts workspaceId="workspace" focus="allowed" hrefFor={slug => `/artifacts?item=${slug}`} homeHref="/artifacts" /> }
afterEach(() => { cleanup(); api.mockReset(); nav.replace.mockReset(); vi.restoreAllMocks();  workspace.mode = "restricted" })
const catalog = [{ slug: "allowed", name: "Allowed Page", publication: 7, actions: [{ intent_hash: "frozen-intent", panel_id: "panel", id: "work", label: "Allowed action", inputs: [{ name: "task", type: "text", required: true }], confirm: { title: "Confirm work", body: "Run this declared action?" } }] }]
const key = JSON.stringify(["allowed", "panel", "work"])
function response(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }) }

it("submits a publication-bound declared action after host confirmation and polls only its private result", async () => {
  mountConfirmHost()
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/restricted-pages")) return response(catalog)
    if (init?.method === "POST") return response({ run_id: "private-run", status: "SCHEDULED" }, 202)
    if (url.endsWith("/restricted-routine-runs/private-run")) return response({ run_id: "private-run", status: "completed", step_outputs: { answer: "OWN_PAGE_RESULT" } })
    throw new Error("Unexpected shared request")
  })
  render(<Allowed />)
  await screen.findByRole("option", { name: "Allowed action" })
  fireEvent.change(screen.getByRole("combobox", { name: "Action" }), { target: { value: key } })
  fireEvent.change(screen.getByRole("textbox", { name: "task" }), { target: { value: "private input" } })
  fireEvent.click(screen.getByRole("button", { name: "Run action" }))
  expect(await screen.findByRole("alertdialog", { name: "Confirm work" })).toHaveTextContent("Run this declared action?")
  await answerConfirm("Run action")
  await screen.findByText("OWN_PAGE_RESULT")
  const submit = api.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(submit[0]).toBe("/api/v1/pages/allowed/application/actions/panel/work?workspace_id=workspace")
  expect(JSON.parse(submit[1].body)).toEqual({ inputs: { task: "private input" }, publication: 7, expected_intent_hash:"frozen-intent" })
  expect(submit[1].headers["Idempotency-Key"]).toBeTruthy()
  expect(api.mock.calls.some(([url]) => url.includes("/journal") || url.includes("/assets") || url.endsWith("/pages") || url.includes("/panels/panel/data"))).toBe(false)
})

it("sends nothing when the declared action is not confirmed", async () => {
  mountConfirmHost()
  api.mockImplementation(async (url: string) => {
    if (url.endsWith("/restricted-pages")) return response(catalog)
    throw new Error("Unexpected request")
  })
  render(<Allowed />)
  await screen.findByRole("option", { name: "Allowed action" })
  fireEvent.change(screen.getByRole("combobox", { name: "Action" }), { target: { value: key } })
  fireEvent.change(screen.getByRole("textbox", { name: "task" }), { target: { value: "private input" } })
  fireEvent.click(screen.getByRole("button", { name: "Run action" }))
  await answerConfirm("Cancel")
  expect(api.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
  expect(screen.getByRole("button", { name: "Run action" })).toBeEnabled()
})

it("retries the same frozen Page admission and clears a revoked private result", async () => {
  mountConfirmHost()
  let submitted = 0
  let polls = 0
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/restricted-pages")) return response(catalog)
    if (init?.method === "POST") { if (++submitted === 1) throw new TypeError("network disconnected"); return response({ run_id: "private-run", status: "DEDUPED" }, 202) }
    if (url.endsWith("/restricted-routine-runs/private-run")) { if (++polls === 1) return response({ run_id: "private-run", status: "running", step_outputs: { answer: "REVOKED_PAGE_RESULT" } }); return response({ error: "unavailable" }, 404) }
    throw new Error("Unexpected shared request")
  })
  render(<Allowed />)
  await screen.findByRole("option", { name: "Allowed action" })
  fireEvent.change(screen.getByRole("combobox", { name: "Action" }), { target: { value: key } })
  fireEvent.change(screen.getByRole("textbox", { name: "task" }), { target: { value: "private input" } })
  fireEvent.click(screen.getByRole("button", { name: "Run action" }))
  await answerConfirm("Run action")
  await screen.findByText(/Could not confirm the outcome/)
  fireEvent.click(screen.getByRole("button", { name: "Retry request" }))
  await screen.findByText("REVOKED_PAGE_RESULT")
  await screen.findByText("This run is no longer available.", {}, { timeout: 3000 })
  await waitFor(() => expect(screen.queryByText("REVOKED_PAGE_RESULT")).toBeNull())
  const submits = api.mock.calls.filter(([, init]) => init?.method === "POST")
  expect(submits[0][1].body).toBe(submits[1][1].body)
  expect(submits[0][1].headers["Idempotency-Key"]).toBe(submits[1][1].headers["Idempotency-Key"])
})

it("ordinary Pages moves a restricted member to Artifacts and keeps the trusted editor", async () => {
  window.history.replaceState(null, "", "/pages")
  api.mockResolvedValue(response([]))
  render(<PagesPage />)
  expect(nav.replace).toHaveBeenCalledWith("/artifacts")
  expect(api).not.toHaveBeenCalled()
  expect(screen.queryByText("Trusted Page editor")).toBeNull()
  cleanup()
  workspace.mode = "trusted"
  render(<PagesPage />)
  expect(screen.getByText("Trusted Page editor")).toBeTruthy()
})

it("names neither a missing artifact nor the catalog behind the gate", async () => {
  api.mockImplementation(async (url: string) => {
    if (url.endsWith("/restricted-pages")) return response(catalog)
    throw new Error("Unexpected request")
  })
  render(<RestrictedArtifacts workspaceId="workspace" focus="secret-brief" hrefFor={slug => `/artifacts?item=${slug}`} homeHref="/artifacts?workspace_id=workspace" />)
  expect(await screen.findByText("This artifact isn’t part of your access")).toBeTruthy()
  expect(document.body.textContent).not.toMatch(/secret-brief|Allowed Page|Allowed action/)
  expect(screen.getByRole("link", { name: "Go to Artifacts" }).getAttribute("href")).toBe("/artifacts?workspace_id=workspace")
})
