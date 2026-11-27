import { webcrypto } from "node:crypto"
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { InlineRunImages, loadRunImages, runImageFiles } from "../inline-run-images"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { readArtifactSnapshot, readArtifactVersion } from "../artifact-file-io"

const sessionState = vi.hoisted(() => ({ id: "human" as string | undefined }))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: sessionState.id } } }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("../../chat-agent-context", () => ({ useChatAgent: () => ({ crewId: "crew", slug: "agent" }) }))
vi.mock("../artifact-file-io", async original => ({ ...await original<typeof import("../artifact-file-io")>(), readArtifactSnapshot: vi.fn(), readArtifactVersion: vi.fn() }))
const file = { path: "crew/agent/runs/run-1/chart.png", name: "chart.png", size: 68, mod_time: "2026-10-16", is_dir: false }
const png = new Uint8Array(Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aE/0AAAAASUVORK5CYII=", "base64"))
const opts = { agentId: "a", workspaceId: "w", chatId: "chat", runId: "run-1", crewId: "crew", slug: "agent", signal: new AbortController().signal }
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const view = render(<QueryClientProvider client={client}><InlineRunImages agentId="a" workspaceId="w" chatId="chat" runId="run-1" /></QueryClientProvider>)
  return { ...view, client }
}
beforeEach(() => {
  vi.resetAllMocks()
  sessionState.id = "human"
  vi.stubGlobal("IntersectionObserver", class {
    constructor(private callback: IntersectionObserverCallback) {}
    observe(target: Element) { this.callback([{ target, isIntersecting: true } as IntersectionObserverEntry], this as unknown as IntersectionObserver) }
    disconnect() {}
  })
  vi.stubGlobal("crypto", webcrypto)
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:authorized-image"), revokeObjectURL: vi.fn() }))
  vi.mocked(apiFetch).mockImplementation(async (url) => new Response(JSON.stringify(String(url).includes("execution-profile") ? { mode: "trusted" } : [file])))
  vi.mocked(readArtifactSnapshot).mockResolvedValue({ path: file.path, bytes: png })
  vi.mocked(readArtifactVersion).mockResolvedValue(JSON.stringify([file.mod_time, file.size]))
})
describe("run images", () => {
  it("discovers only this run's raster deliverables from authorized paths", () => {
    expect(runImageFiles([file, { ...file, path: "crew/agent/runs/other/chart.png" }, { ...file, path: "crew/agent/runs/run-1/.codex/chart.png" }, { ...file, path: "crew/agent/runs/run-1/../chart.png" }, { ...file, path: "crew/agent/runs/run-1/chart.svg" }], "run-1", "crew", "agent")).toEqual([file])
    expect(runImageFiles([file], "../run-1", "crew", "agent")).toEqual([])
  })
  it("mounts a real PNG in the existing safe viewer with alt, caption and byte version", async () => {
    mount()
    expect(screen.getByRole("status")).toHaveTextContent("Checking run images")
    expect(await screen.findByRole("img", { name: "chart.png" })).toHaveAttribute("src", "blob:authorized-image")
    expect(screen.getByText(/Current run output · version [a-f0-9]{12}/)).toBeInTheDocument()
  })
  it("never renders script-bearing SVG disguised as PNG", async () => {
    vi.mocked(readArtifactSnapshot).mockResolvedValue({ path: file.path, bytes: new TextEncoder().encode('<svg onload="alert(1)"><script>alert(1)</script></svg>') })
    const { container } = mount()
    await waitFor(() => expect(readArtifactVersion).toHaveBeenCalled())
    expect(await loadRunImages(opts)).toEqual([])
    expect(container.querySelector("svg,script,iframe,img")).toBeNull()
  })
  it("denied listing never requests bytes or exposes filenames", async () => {
    vi.mocked(apiFetch).mockResolvedValue(new Response("", { status: 403 }))
    mount()
    expect(await screen.findByRole("alert")).toHaveTextContent("Images unavailable")
    expect(readArtifactSnapshot).not.toHaveBeenCalled()
    expect(screen.queryByText(/chart.png/)).toBeNull()
  })
  it("a run whose files cannot be reached right now, and that never showed an image, shows nothing", async () => {
    vi.mocked(apiFetch).mockImplementation(async (url) => String(url).includes("execution-profile") ? new Response(JSON.stringify({ mode: "trusted" })) : new Response("", { status: 502 }))
    const { container } = mount()
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByRole("status")).toBeNull())
    expect(screen.queryByRole("alert")).toBeNull()
    expect(container.textContent).not.toContain("Images unavailable")
    expect(readArtifactSnapshot).not.toHaveBeenCalled()
  })
  it("a run with no files (the agent listing answers null) shows nothing under the answer", async () => {
    vi.mocked(apiFetch).mockImplementation(async (url) => new Response(JSON.stringify(String(url).includes("execution-profile") ? { mode: "trusted" } : null)))
    const { container } = mount()
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByRole("status")).toBeNull())
    expect(screen.queryByRole("alert")).toBeNull()
    expect(container.textContent).toBe("")
    expect(await loadRunImages(opts)).toEqual([])
  })
  it("a listing that is neither a list nor empty is still an error", async () => {
    vi.mocked(apiFetch).mockImplementation(async (url) => new Response(JSON.stringify(String(url).includes("execution-profile") ? { mode: "trusted" } : { files: "x" })))
    mount()
    expect(await screen.findByRole("alert")).toHaveTextContent("Images unavailable")
  })
  it("a run that showed images still says they are unavailable when its files cannot be reached", async () => {
    const { client } = mount()
    await screen.findByRole("img")
    vi.mocked(apiFetch).mockResolvedValue(new Response("", { status: 502 }))
    await client.refetchQueries({ queryKey: ["chat-run-images"] })
    expect(await screen.findByRole("alert")).toHaveTextContent("Images unavailable")
    expect(screen.getByRole("button", { name: "Refresh images" })).toBeTruthy()
    expect(screen.queryByRole("img")).toBeNull()
  })
  it("rejects overwritten files instead of claiming an older revision", async () => {
    vi.mocked(readArtifactVersion).mockResolvedValue('["changed",68]')
    await expect(loadRunImages(opts)).rejects.toThrow("Image changed")
  })
  it("removes already displayed bytes and caption when access is revoked", async () => {
    const { client } = mount()
    await screen.findByRole("img")
    vi.mocked(apiFetch).mockResolvedValue(new Response("", { status: 404 }))
    await client.refetchQueries({ queryKey: ["chat-run-images"] })
    await screen.findByRole("alert")
    expect(screen.queryByRole("img")).toBeNull()
    expect(screen.queryByText(/chart.png/)).toBeNull()
  })
})

for (const mode of ["restricted", "pending"]) {
  it(`${mode} chat never requests generic file listings or bytes`, async () => {
    vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify({ mode })))
    mount()
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    expect(await loadRunImages({ ...opts })).toEqual([])
    expect(readArtifactSnapshot).not.toHaveBeenCalled()
    expect(vi.mocked(apiFetch).mock.calls.every(([url]) => String(url).includes("execution-profile"))).toBe(true)
  })
}


it("does no offscreen HTTP, loads on entry and drops cached bytes/blobs on leaving while preserving height", async () => {
  let notify: IntersectionObserverCallback | undefined
  vi.stubGlobal("IntersectionObserver", class {
    constructor(callback: IntersectionObserverCallback) { notify = callback }
    observe() {}
    disconnect() {}
  })
  const { container, client } = mount()
  expect(apiFetch).not.toHaveBeenCalled()
  const boundary = container.firstElementChild as HTMLElement
  act(() => notify?.([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver))
  await screen.findByRole("img")
  vi.spyOn(boundary, "getBoundingClientRect").mockReturnValue({ height: 320 } as DOMRect)
  act(() => notify?.([{ isIntersecting: false } as IntersectionObserverEntry], {} as IntersectionObserver))
  expect(screen.queryByRole("img")).toBeNull()
  expect(boundary).toHaveStyle({ minHeight: "320px" })
  await waitFor(() => expect(client.getQueryCache().getAll()).toHaveLength(0))
  expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:authorized-image")
})

it("requires an explicit load without IntersectionObserver and releases bytes on Hide", async () => {
  vi.stubGlobal("IntersectionObserver", undefined)
  const { client } = mount()
  expect(apiFetch).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "Load run images" }))
  await screen.findByRole("img")
  fireEvent.click(screen.getByRole("button", { name: "Hide run images" }))
  expect(screen.queryByRole("img")).toBeNull()
  await waitFor(() => expect(client.getQueryCache().getAll()).toHaveLength(0))
})


it("does not carry a previous viewer's measured output space into another viewer", async () => {
  let notify: IntersectionObserverCallback | undefined
  vi.stubGlobal("IntersectionObserver", class {
    constructor(callback: IntersectionObserverCallback) { notify = callback }
    observe() {}
    disconnect() {}
  })
  const { container, client, rerender } = mount()
  act(() => notify?.([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver))
  await screen.findByRole("img")
  vi.spyOn(container.firstElementChild as HTMLElement, "getBoundingClientRect").mockReturnValue({ height: 320 } as DOMRect)
  act(() => notify?.([{ isIntersecting: false } as IntersectionObserverEntry], {} as IntersectionObserver))
  expect(container.firstElementChild).toHaveStyle({ minHeight: "320px" })
  const requests = vi.mocked(apiFetch).mock.calls.length
  sessionState.id = "another-viewer"
  rerender(<QueryClientProvider client={client}><InlineRunImages agentId="a" workspaceId="w" chatId="chat" runId="run-1" /></QueryClientProvider>)
  expect(container.firstElementChild).toHaveStyle({ minHeight: "1px" })
  expect(apiFetch).toHaveBeenCalledTimes(requests)
})

it("ignores a burst of other agents' and other runs' file events, and coalesces its own into one recheck", async () => {
  const handler = (type: string) => {
    const calls = vi.mocked(useRealtimeEventSafe).mock.calls.filter(([t]) => t === type)
    return calls[calls.length - 1][1] as (event: { type: string; payload: Record<string, unknown>; timestamp: Date }) => void
  }
  const fire = (type: string, payload: Record<string, unknown>) => handler(type)({ type, payload, timestamp: new Date() })
  mount()
  await screen.findByRole("img", { name: "chart.png" })
  const before = vi.mocked(apiFetch).mock.calls.length
  act(() => {
    for (let i = 0; i < 200; i++) fire("file.event", { event: "write", agent: `other-${i % 5}`, path: `other-${i % 5}/runs/run-1/f${i}.png` })
    for (let i = 0; i < 20; i++) fire("file.event", { event: "write", agent: "agent", path: `agent/runs/run-2/f${i}.png` })
    fire("run.completed", { run_id: "run-9", agent_id: "someone-else" })
  })
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 700)) })
  expect(vi.mocked(apiFetch).mock.calls.length).toBe(before)
  act(() => {
    for (let i = 0; i < 30; i++) fire("file.event", { event: "write", agent: "agent", path: `agent/runs/run-1/f${i}.png` })
  })
  await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.length).toBeGreaterThan(before))
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 700)) })
  // One recheck: the execution-profile read plus one run listing.
  expect(vi.mocked(apiFetch).mock.calls.length - before).toBe(2)
})
