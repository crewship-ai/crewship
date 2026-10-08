// Admin › Runtime beyond the inventory: the summary line, the log level an
// operator can raise for a while, and the maintenance actions — each of which
// looks before it removes, and the destructive one asks for a typed word.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

import { toast } from "sonner"
import { RuntimeTab } from "../runtime-tab"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

const ok = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body, text: async () => "" })
let legacyPresent = false
let logPutFails = false
const LOG = { level: "info", baseline: "info" }

function routes(url: string, init?: RequestInit) {
  const u = String(url)
  if (u.startsWith("/api/v1/crewshipd")) return ok({ status: "ok", connections: 2, uptime: "1h2m3.5s" })
  if (u.startsWith("/api/v1/agents/crews-status")) return ok({ total: 17, running: 3, idle: 13, queued: 1, error: 0 })
  if (u.startsWith("/api/v1/admin/log-level")) {
    if (init?.method === "PUT") {
      if (logPutFails) return { ok: false, status: 500, json: async () => ({ error: "log level store is down" }), text: async () => JSON.stringify({ error: "log level store is down" }) }
      const b = JSON.parse(String(init.body))
      return ok({ level: b.level, baseline: "info", expires_at: b.ttl_seconds ? "2026-09-29T12:15:00Z" : undefined })
    }
    return ok(LOG)
  }
  if (u.startsWith("/api/v1/admin/legacy-resources")) return ok({ present: legacyPresent })
  if (u.startsWith("/api/v1/admin/reap-orphan-containers")) {
    const apply = u.includes("apply=true")
    return ok({
      orphans: [{ crew_id: "c1", slug: "ops", container_id: "abcdef1234567890", reaped: apply }],
      count: 1, applied: apply, inspected: 4, identified: 4, detector_inert: false,
    })
  }
  if (u.startsWith("/api/v1/admin/prune-crew-runtimes")) return ok({ removed: ["ops", "qa"], count: 2 })
  if (u.startsWith("/api/v1/admin/prune-legacy-resources")) return ok({ removed: ["x"], count: 1 })
  return ok(null, 404)
}

beforeEach(() => {
  cleanup()
  legacyPresent = false
  logPutFails = false
  vi.mocked(toast.error).mockReset()
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (u: string, init?: RequestInit) => routes(u, init))
})

function renderTab() {
  // Inside a page, as Admin renders it: the level edit joins the page's Save bar.
  return render(
    <PageSaveProvider>
      <RuntimeTab runtimeChecking={false} runtimeAvailable
        allRuntimes={[{ runtime: "docker", version: "29.3.0", socket: "/var/run/docker.sock", in_use: true }]}
        runtimeInstallLinks={{}} onCheckRuntime={vi.fn()} workspaceId="ws-1" />
      <PageSaveBar />
    </PageSaveProvider>,
  )
}
const calls = (pred: (u: string, init?: RequestInit) => boolean) =>
  h.apiFetch.mock.calls.filter(([u, init]) => pred(String(u), init as RequestInit | undefined))

describe("Runtime — summary", () => {
  it("says in one line what runs the agents, the daemon, agents and the log level", async () => {
    renderTab()
    const line = document.querySelector("[data-slot=runtime-summary]") as HTMLElement
    expect(line).toHaveTextContent("Docker 29.3.0 in use")
    await waitFor(() => expect(line).toHaveTextContent("Daemon healthy · up 1h 2m"))
    expect(line).toHaveTextContent("3 running · 13 idle · 1 queued")
    expect(line).toHaveTextContent("Log info")
    // The figures row is gone: one line, not four tiles.
    expect(document.querySelector("[data-slot=admin-kpi]")).toBeNull()
  })
})

describe("Runtime — logging", () => {
  it("raises the level for a while, then goes back to the baseline", async () => {
    renderTab()
    await waitFor(() => expect(document.querySelector("[data-slot=log-current]")).not.toBeNull())
    fireEvent.click(screen.getByRole("button", { name: "debug" }))
    fireEvent.click(screen.getByRole("button", { name: "1 hour" }))
    // No Apply in the card: the page's bar carries the edit.
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull()
    expect(screen.getByRole("region", { name: "Unsaved changes" })).toHaveTextContent("2 unsaved changes")
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(calls((u, i) => u.startsWith("/api/v1/admin/log-level") && i?.method === "PUT")).toHaveLength(1))
    const [, init] = calls((u, i) => u.startsWith("/api/v1/admin/log-level") && i?.method === "PUT")[0]
    expect(JSON.parse(String(init!.body))).toEqual({ level: "debug", ttl_seconds: 3600 })

    fireEvent.click(await screen.findByRole("button", { name: "Back to info" }))
    await waitFor(() => expect(calls((u, i) => u.startsWith("/api/v1/admin/log-level") && i?.method === "PUT")).toHaveLength(2))
    const [, back] = calls((u, i) => u.startsWith("/api/v1/admin/log-level") && i?.method === "PUT")[1]
    expect(JSON.parse(String(back!.body))).toEqual({ level: "info", ttl_seconds: 0 })
  })

  it("shows nothing to save until a level is picked, and Discard drops the pick", async () => {
    renderTab()
    await waitFor(() => expect(document.querySelector("[data-slot=log-current]")).not.toBeNull())
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "warn" }))
    expect(screen.getByRole("region", { name: "Unsaved changes" })).toHaveTextContent("1 unsaved change")
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
    expect(screen.getByRole("button", { name: "info" })).toHaveAttribute("aria-pressed", "true")
  })

  it("a refused level change is a corner toast and the pick stays", async () => {
    logPutFails = true
    renderTab()
    await waitFor(() => expect(document.querySelector("[data-slot=log-current]")).not.toBeNull())
    fireEvent.click(screen.getByRole("button", { name: "debug" }))
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(vi.mocked(toast.error).mock.calls[0][0]).toBe("Couldn’t save Logging")
    expect(screen.getByRole("region", { name: "Unsaved changes" })).toHaveTextContent("1 unsaved change")
    expect(screen.getByRole("button", { name: "debug" })).toHaveAttribute("aria-pressed", "true")
  })
})

describe("Runtime — maintenance", () => {
  it("checks for orphaned containers first and removes them only when asked", async () => {
    renderTab()
    fireEvent.click(screen.getByRole("button", { name: "Check" }))
    const remove = await screen.findByRole("button", { name: "Remove 1" })
    const first = calls((u) => u.startsWith("/api/v1/admin/reap-orphan-containers"))
    expect(first).toHaveLength(1)
    expect(first[0][0]).not.toContain("apply=true")
    expect(first[0][1]).toMatchObject({ method: "POST" })
    fireEvent.click(remove)
    await waitFor(() => expect(calls((u) => u.includes("reap-orphan-containers?apply=true"))).toHaveLength(1))
    expect(await screen.findByText(/Removed 1 of 1 orphaned container/)).toBeInTheDocument()
  })

  it("hides the legacy row when there is nothing legacy, and offers it when there is", async () => {
    renderTab()
    await waitFor(() => expect(calls((u) => u.startsWith("/api/v1/admin/legacy-resources"))).toHaveLength(1))
    expect(screen.queryByTestId("legacy-remove")).toBeNull()
    cleanup()
    legacyPresent = true
    renderTab()
    expect(await screen.findByTestId("legacy-remove")).toBeInTheDocument()
  })

  it("removes every crew runtime from the Danger zone, only after the word is typed", async () => {
    renderTab()
    const zone = screen.getByRole("region", { name: "Danger zone" })
    fireEvent.click(within(zone).getByRole("button", { name: /remove runtimes/i }))
    const go = within(zone).getByRole("button", { name: "Remove every runtime" })
    expect(go).toBeDisabled()
    fireEvent.change(within(zone).getByLabelText(/type/i), { target: { value: "remov" } })
    expect(go).toBeDisabled()
    fireEvent.change(within(zone).getByLabelText(/type/i), { target: { value: "remove" } })
    expect(go).toBeEnabled()
    fireEvent.click(go)
    await waitFor(() => expect(calls((u, i) => u.startsWith("/api/v1/admin/prune-crew-runtimes") && i?.method === "POST")).toHaveLength(1))
    expect(await screen.findByText(/Removed the runtime of 2 crews/)).toBeInTheDocument()
  })

  it("says the action needs the Docker provider when the server answers 503", async () => {
    h.apiFetch.mockImplementation(async (u: string, init?: RequestInit) =>
      String(u).startsWith("/api/v1/admin/reap-orphan-containers") ? ok({ error: "no provider" }, 503) : routes(u, init))
    renderTab()
    fireEvent.click(screen.getByRole("button", { name: "Check" }))
    expect(await screen.findByText("Only available with the Docker provider.")).toBeInTheDocument()
  })
})
