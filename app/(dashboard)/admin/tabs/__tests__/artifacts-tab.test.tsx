import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { ArtifactsTab } from "../artifacts-tab"

const mocks = vi.hoisted(() => ({ apiFetch: vi.fn(), error: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => mocks.apiFetch(...args) }))
vi.mock("@/lib/toast", () => ({ toast: { error: mocks.error } }))

const PATH = "/api/v1/admin/instance/artifacts/applications"
const word = {
  id: "wordcraft", label: "Word", desired: "enabled", state: "ready", source: "admin", locked: false,
  documents: 2, component: { available: true, version: "1.2.3", runtime: "wasm" },
}
const sheet = {
  id: "gridcraft", label: "Sheet", desired: "disabled", state: "disabled", source: "server_config", locked: true,
  message: "Turned off. Existing documents stay readable.",
}
const agents = {
  id: "agents", label: "Agents in Word and Sheet", desired: "enabled", state: "ready", source: "admin", locked: false,
}

function settings(changedWord: object = word) {
  return { applications: [changedWord, sheet], agents, editor: "ok" }
}

const response = (body: unknown, status = 200) => ({
  ok: status >= 200 && status < 300, status, json: async () => body,
}) as Response

function view() {
  const client = new QueryClient({ defaultOptions: { queries: { gcTime: 0, retry: false } } })
  return render(<QueryClientProvider client={client}><ArtifactsTab /></QueryClientProvider>)
}

beforeEach(() => {
  mocks.apiFetch.mockReset()
  mocks.error.mockReset()
  mocks.apiFetch.mockResolvedValue(response(settings()))
})
afterEach(cleanup)

describe("Admin artifact applications", () => {
  it("shows actual state, engine information, and the reason a setting is locked", async () => {
    view()
    const wordCard = await screen.findByRole("region", { name: "Word" })
    expect(within(wordCard).getByText("Ready")).toBeInTheDocument()
    expect(within(wordCard).getByText("2 documents")).toBeInTheDocument()
    expect(within(wordCard).getByText("Installed version 1.2.3 · wasm")).toBeInTheDocument()
    const sheetCard = screen.getByRole("region", { name: "Sheet" })
    expect(within(sheetCard).getByText(/Managed by server configuration/)).toBeInTheDocument()
    expect(within(sheetCard).getByRole("button", { name: "Turn on Sheet" })).toBeDisabled()
    expect(screen.getByRole("region", { name: "Agents in Word and Sheet" })).toBeInTheDocument()
    expect(mocks.apiFetch).toHaveBeenCalledWith(PATH, expect.objectContaining({ signal: expect.any(AbortSignal) }))
  })

  it("only disables Word after confirmation and sends the selected drain", async () => {
    const drained = { ...word, desired: "disabled", state: "draining", drain_until: "2026-11-25T08:15:00Z" }
    mocks.apiFetch.mockResolvedValueOnce(response(settings())).mockResolvedValueOnce(response(settings(drained)))
    view()
    fireEvent.click(await screen.findByRole("button", { name: "Turn off Word" }))
    const dialog = await screen.findByRole("alertdialog", { name: "Turn off Word?" })
    expect(dialog).toHaveTextContent("Existing documents will remain readable.")
    expect(mocks.apiFetch).toHaveBeenCalledTimes(1)
    fireEvent.change(within(dialog).getByLabelText("Allow open editors to finish saving for"), { target: { value: "300" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Turn off Word" }))
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledWith(`${PATH}/wordcraft`, expect.objectContaining({
      method: "PATCH", body: JSON.stringify({ desired: "disabled", drain_seconds: 300 }),
    })))
    expect(await screen.findByText("Turning off")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Turn on Word" })).toBeInTheDocument()
  })

  it("polls installation until the server reports the application ready", async () => {
    const missing = {
      ...word, state: "unavailable", reason: "component_not_installed", component: { available: false },
    }
    const installing = {
      ...missing, reason: "component_installing", message: "Downloading and verifying the application.",
    }
    mocks.apiFetch
      .mockResolvedValueOnce(response(settings(missing)))
      .mockResolvedValueOnce(response(settings(installing), 202))
      .mockResolvedValue(response(settings()))

    view()
    fireEvent.click(await screen.findByRole("button", { name: "Install Word" }))
    expect(await screen.findByText("Downloading and verifying the application.")).toBeInTheDocument()
    expect(mocks.apiFetch).toHaveBeenCalledWith(`${PATH}/wordcraft/install`, { method: "POST" })
    await waitFor(() => expect(within(screen.getByRole("region", { name: "Word" })).getByText("Ready")).toBeInTheDocument(), {
      timeout: 5000,
    })
  })

  it("shows a diagnostic failure without changing access", async () => {
    mocks.apiFetch
      .mockResolvedValueOnce(response(settings()))
      .mockResolvedValueOnce(response({ passed: false, message: "The document engine failed its self-test." }))
    view()
    fireEvent.click(await screen.findByRole("button", { name: "Test Word" }))
    expect(await screen.findByText("The document engine failed its self-test.")).toBeInTheDocument()
    expect(mocks.apiFetch).toHaveBeenCalledWith(`${PATH}/wordcraft/diagnostics`, { method: "POST" })
    expect(screen.getByRole("button", { name: "Turn off Word" })).toBeEnabled()
  })

  it("shows an authorization failure without rendering application controls", async () => {
    mocks.apiFetch.mockResolvedValue(response({ error: "Instance administrator required" }, 403))
    view()
    expect(await screen.findByText("Couldn’t load artifact applications")).toBeInTheDocument()
    expect(screen.getByText("Instance administrator required")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Turn off Word" })).not.toBeInTheDocument()
  })
})
