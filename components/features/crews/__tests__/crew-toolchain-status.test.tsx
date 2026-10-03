import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { CrewToolchainStatus } from "../crew-toolchain-status"
import { CrewRuntimeConfig } from "../crew-runtime-config"

const apiFetch = vi.hoisted(() => vi.fn())
const callbacks = vi.hoisted(() => new Map<string, (event: { payload: Record<string, unknown> }) => void>())
vi.mock("@/lib/api-fetch", () => ({ apiFetch }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (name: string, callback: (event: { payload: Record<string, unknown> }) => void) => { callbacks.set(name, callback) } }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "VIEWER" }) }))
vi.mock("../runtime-config", () => ({ RuntimeConfig: () => null }))
const image = `sha256:${"a".repeat(64)}`
function response(body: unknown) { return { ok: true, json: async () => body } }
function recorded(version = "0.160.0") {
  return { cached_image: image, toolchain: { requested: [{ binary: "codex", source: "mise", selector: "latest", exact: false }], built: { schema_version: 1, image_id: image, status: "recorded", tools: [{ binary: "codex", version, status: "observed" }], qualification: { image_id: image, status: "passed", tools: [{ binary: "codex", status: "passed" }] } } } }
}
beforeEach(() => { apiFetch.mockReset(); callbacks.clear() })
afterEach(cleanup)

describe("built AI tool evidence", () => {
  it("distinguishes desired selectors and selected-image versions with scoped reads", async () => {
    apiFetch.mockResolvedValue(response(recorded()))
    render(<CrewToolchainStatus crewId="crew/a" workspaceId="ws & b" />)
    expect(await screen.findByText("0.160.0")).toBeInTheDocument()
    expect(screen.getByText("latest")).toBeInTheDocument()
    expect(screen.getByText(/Startup checks passed/)).toHaveTextContent("Provider sign-in is tested separately")
    expect(screen.getByText(/A running environment may still use an older build/)).toBeInTheDocument()
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/crews/crew%2Fa/provision?workspace_id=ws%20%26%20b", expect.objectContaining({ signal: expect.any(AbortSignal) }))
  })
  it("does not invent evidence for old builds", async () => {
    const data = recorded()
    apiFetch.mockResolvedValue(response({ ...data, toolchain: { requested: [{ binary: "claude", source: "devcontainer_feature", exact: false }], built: null } }))
    render(<CrewToolchainStatus crewId="a" workspaceId="w" />)
    expect(await screen.findByText("Devcontainer feature")).toBeInTheDocument()
    expect(screen.getByText("Not recorded")).toBeInTheDocument()
    expect(screen.getByText(/Startup checks not available/)).toBeInTheDocument()
  })
  it("withholds a version and successful check bound to a different image", async () => {
    apiFetch.mockResolvedValue(response({ ...recorded(), cached_image: `sha256:${"b".repeat(64)}` }))
    render(<CrewToolchainStatus crewId="a" workspaceId="w" />)
    expect(await screen.findByText("Not recorded")).toBeInTheDocument()
    expect(screen.queryByText("0.160.0")).not.toBeInTheDocument()
    expect(screen.queryByText(/Startup checks passed/)).not.toBeInTheDocument()
  })
  it("reports failed checks while keeping the image observation distinct", async () => {
    const data = recorded(); data.toolchain.built.qualification.status = "failed"
    data.toolchain.requested[0] = { binary: "codex", source: "mise", selector: "0.160.0", exact: true }
    apiFetch.mockResolvedValue(response(data))
    render(<CrewToolchainStatus crewId="a" workspaceId="w" />)
    expect(await screen.findByText("0.160.0 (pinned)")).toBeInTheDocument()
    expect(screen.getByText(/Startup checks did not pass/)).toBeInTheDocument()
  })
  it("supports retry after a failed read without showing stale success", async () => {
    apiFetch.mockResolvedValueOnce({ ok: false }).mockResolvedValueOnce(response(recorded()))
    render(<CrewToolchainStatus crewId="a" workspaceId="w" />)
    expect(await screen.findByText(/Could not load tool versions/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Refresh built tool versions" }))
    expect(await screen.findByText("0.160.0")).toBeInTheDocument()
  })
  it("ignores a late response from the previous crew and workspace", async () => {
    let finish: (value: unknown) => void = () => {}
    apiFetch.mockReturnValueOnce(new Promise((resolve) => { finish = resolve })).mockResolvedValueOnce(response(recorded("0.161.0")))
    const view = render(<CrewToolchainStatus crewId="a" workspaceId="w1" />)
    view.rerender(<CrewToolchainStatus crewId="b" workspaceId="w2" />)
    expect(await screen.findByText("0.161.0")).toBeInTheDocument()
    await act(async () => { finish(response(recorded("0.159.0"))) })
    expect(screen.queryByText("0.159.0")).not.toBeInTheDocument()
    expect(screen.getByText("0.161.0")).toBeInTheDocument()
  })
  it("refreshes relevant build completion, reconnect and changed definitions", async () => {
    apiFetch.mockResolvedValue(response(recorded()))
    const view = render(<CrewToolchainStatus crewId="a" workspaceId="w" refreshKey="one" />)
    await screen.findByText("0.160.0")
    await act(async () => { callbacks.get("provision.completed")?.({ payload: { crew_id: "other" } }) })
    expect(apiFetch).toHaveBeenCalledTimes(1)
    await act(async () => { callbacks.get("provision.completed")?.({ payload: { crew_id: "a" } }) })
    expect(apiFetch).toHaveBeenCalledTimes(2)
    await act(async () => { callbacks.get("realtime.reconnected")?.({ payload: {} }) })
    expect(apiFetch).toHaveBeenCalledTimes(3)
    view.rerender(<CrewToolchainStatus crewId="a" workspaceId="w" refreshKey="two" />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(4))
  })
  it("handles malformed evidence without rendering a successful check", async () => {
    apiFetch.mockResolvedValue(response({ toolchain: { built: { tools: "invalid" } } }))
    render(<CrewToolchainStatus crewId="a" workspaceId="w" />)
    expect(await screen.findByText(/Could not load tool versions/)).toBeInTheDocument()
  })
  it("is present in read-only runtime settings", async () => {
    apiFetch.mockResolvedValue(response(recorded()))
    render(<CrewRuntimeConfig crewId="a" workspaceId="w" runtimeImage={null} devcontainerConfig={null} miseConfig={null} cachedImage={image} canEdit={false} onSave={vi.fn()} />)
    expect(await screen.findByText("0.160.0")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Rebuild" })).not.toBeInTheDocument()
  })
})
