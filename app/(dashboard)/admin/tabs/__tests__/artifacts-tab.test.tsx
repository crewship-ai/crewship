import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
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

function view(client = new QueryClient({ defaultOptions: { queries: { gcTime: 0, retry: false } } })) {
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

  it("explains why a build without a trusted package cannot turn Word on", async () => {
    mocks.apiFetch.mockResolvedValue(response(settings({
      ...word, desired: "disabled", state: "unavailable", reason: "component_missing",
      component: { available: false, reason: "component_missing" },
    })))
    view()
    const toggle = await screen.findByRole("button", { name: "Turn on Word" })
    expect(toggle).toBeDisabled()
    expect(screen.getByText(/This Crewship build has no trusted Word package/)).toBeVisible()
    expect(screen.queryByRole("button", { name: "Install Word" })).not.toBeInTheDocument()
    expect(mocks.apiFetch).toHaveBeenCalledTimes(1)
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

  it("shows upload progress and reads the server status before the file response", async () => {
    const missing = { ...word, state: "unavailable", reason: "component_not_installed", component: { available: false } }
    const installing = { ...missing, reason: "component_installing", message: "Verifying the uploaded package." }
    let finishUpload: (value: Response) => void = () => {}
    const upload = new Promise<Response>((resolve) => { finishUpload = resolve })
    mocks.apiFetch
      .mockResolvedValueOnce(response(settings(missing)))
      .mockReturnValueOnce(upload)
      .mockResolvedValue(response(settings(installing)))
    const { container } = view()
    await screen.findByRole("button", { name: "Install Word from file" })
    const input = container.querySelector("input[type=file]")!
    const file = new File(["fixture"], "word.craftpkg")
    fireEvent.change(input, { target: { files: [file] } })
    expect(await screen.findByText("Uploading and verifying Word…")).toBeVisible()
    expect(mocks.apiFetch).toHaveBeenCalledWith(`${PATH}/wordcraft/install`, {
      method: "POST", headers: { "Content-Type": "application/octet-stream" }, body: file,
    })
    await waitFor(() => expect(screen.getByText("Verifying the uploaded package.")).toBeVisible(), { timeout: 5000 })
    await act(async () => { finishUpload(response(settings())) })
    await waitFor(() => expect(screen.queryByText("Uploading and verifying Word…")).not.toBeInTheDocument())
  })

  it("shows progress when enabling an application starts a download", async () => {
    const missing = { ...word, desired: "disabled", state: "unavailable", reason: "component_not_installed", component: { available: false } }
    const installing = { ...missing, desired: "enabled", reason: "component_installing" }
    let finishPatch: (value: Response) => void = () => {}
    const patch = new Promise<Response>((resolve) => { finishPatch = resolve })
    mocks.apiFetch
      .mockResolvedValueOnce(response(settings(missing)))
      .mockReturnValueOnce(patch)
      .mockResolvedValue(response(settings(installing)))
    view()
    fireEvent.click(await screen.findByRole("button", { name: "Turn on Word" }))
    expect(await screen.findByText("Turning on Word…")).toBeVisible()
    expect(mocks.apiFetch).toHaveBeenCalledWith(`${PATH}/wordcraft`, expect.objectContaining({
      method: "PATCH", body: JSON.stringify({ desired: "enabled" }),
    }))
    await act(async () => { finishPatch(response(settings(installing))) })
    expect(await screen.findByText("Installing Word…")).toBeVisible()
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

  it("removes cached controls if the server revokes access", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    mocks.apiFetch
      .mockResolvedValueOnce(response(settings()))
      .mockResolvedValue(response({ error: "Instance administrator required" }, 403))
    view(client)
    expect(await screen.findByRole("button", { name: "Turn off Word" })).toBeInTheDocument()
    void client.invalidateQueries({ queryKey: ["instance-admin", "artifact-applications"] })
    expect(await screen.findByText("Instance administrator required")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Turn off Word" })).not.toBeInTheDocument()
  })
})
