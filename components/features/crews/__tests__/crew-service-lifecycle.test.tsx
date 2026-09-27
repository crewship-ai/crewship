import { beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { CrewServiceLifecycle } from "../crew-service-lifecycle"
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
const snapshot = { supported: true, services: [{ name: "redis", desired_state: "on_demand", observed_state: "unmanaged", version: 0 }] }
const response = (value: unknown, status = 200) => ({ ok: status < 400, status, json: async () => value })
describe("crew service lifecycle", () => {
 beforeEach(() => { cleanup(); apiFetch.mockReset(); apiFetch.mockResolvedValue(response(snapshot)) })
 it("writes the observed version and keeps requested state separate from observed state", async () => {
  apiFetch.mockImplementation(async (_url: string, init?: RequestInit) => init?.method === "PUT" ? response({}, 202) : response(snapshot))
  render(<CrewServiceLifecycle crewId="crew" workspaceId="ws" canManage />)
  fireEvent.click(await screen.findByRole("button", { name: "redis: Keep running" }))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith("/api/v1/crews/crew/services/redis/state?workspace_id=ws", expect.objectContaining({ method: "PUT", body: JSON.stringify({ desired_state: "running", expected_version: 0 }) })))
  expect(screen.getByText(/Last reconciliation: unmanaged/)).toBeInTheDocument()
 })
 it("offers no mutation below manager", async () => {
  render(<CrewServiceLifecycle crewId="crew" workspaceId="ws" canManage={false} />)
  await screen.findByText("redis")
  expect(screen.queryByRole("button", { name: "redis: Stop" })).not.toBeInTheDocument()
 })
 it("does not claim an empty crew when state could not be read", async () => {
  apiFetch.mockResolvedValue(response({}, 503))
  render(<CrewServiceLifecycle crewId="crew" workspaceId="ws" canManage />)
  expect(await screen.findByRole("alert")).toHaveTextContent("could not be verified")
  expect(screen.queryByText(/No services are declared/)).not.toBeInTheDocument()
 })
 it("reloads a conflict instead of assuming the requested stop succeeded", async () => {
  apiFetch.mockImplementation(async (_url: string, init?: RequestInit) => init?.method === "PUT" ? response({}, 409) : response(snapshot))
  render(<CrewServiceLifecycle crewId="crew" workspaceId="ws" canManage />)
  fireEvent.click(await screen.findByRole("button", { name: "redis: Stop" }))
  await waitFor(() => expect(apiFetch.mock.calls.filter(([, init]) => !init?.method)).toHaveLength(2))
  expect(screen.getByText(/Requested: On demand/)).toBeInTheDocument()
 })
})
