// Admin › Backups and Data retention, drawn from the review fixtures (the
// design's own example) exactly as ?demo=1 shows them, and — without demo — a
// server that does not have the endpoints yet. What is pinned: each page
// shows what the design says it shows, the scope strip drives the page and
// the URL, dependencies are computed as the user edits, a partial test
// restore is never shown as a pass, and nothing is invented when the server
// has no answer.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-dess", loading: false }) }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("next/link", () => ({ default: ({ href, children, ...p }: { href: string; children: React.ReactNode }) => <a href={href} {...p}>{children}</a> }))

import { BackupsConsole } from "../backups-console"
import type { BackupsSection } from "@/app/(dashboard)/admin/navigation"

function show(page: BackupsSection | "retention", search = "?demo=1") {
  window.history.replaceState(null, "", `/admin${search}`)
  const onNavigate = vi.fn()
  const utils = render(<BackupsConsole page={page} onNavigate={onNavigate} />)
  return { ...utils, onNavigate }
}

beforeEach(() => {
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue(new Response("{}", { status: 404 }))
  Object.values(h.toast).forEach((f) => f.mockReset())
})
afterEach(() => cleanup())

describe("Overview", () => {
  it("answers the five questions, with a partial restore in the warn tone", async () => {
    show("overview")
    const verdict = await screen.findByText("Partial restore verified")
    expect(verdict).toHaveAttribute("data-tone", "warn")
    for (const label of ["Protects", "How often", "Where", "How long", "Really restored?"]) expect(screen.getByText(label)).toBeInTheDocument()
    expect(screen.getByText("Complete recovery")).toBeInTheDocument()
    expect(screen.getByText(/the whole Crewship: 5 workspaces/)).toBeInTheDocument()
  })

  it("warns that a local copy does not cover losing the server, and links Storage", async () => {
    const { onNavigate } = show("overview")
    const bar = await screen.findByText("Local copy only. Losing this server is not covered.")
    fireEvent.click(within(bar.closest("[data-slot=warn-bar]") as HTMLElement).getByRole("button", { name: "Storage" }))
    expect(onNavigate).toHaveBeenCalledWith("storage")
  })

  it("lists concrete problems with their actions", async () => {
    const { onNavigate } = show("overview")
    expect(await screen.findByText("The latest backup is missing 12 attachment files")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Keys" }))
    expect(onNavigate).toHaveBeenCalledWith("keys")
    fireEvent.click(screen.getByRole("button", { name: "See which" }))
    expect(onNavigate).toHaveBeenCalledWith("history")
  })

  it("draws fourteen equal nights with the legend", async () => {
    const { container } = show("overview")
    await screen.findByText("Last 14 nights")
    const strip = container.querySelector("[data-slot=night-strip]")!
    expect(strip.children).toHaveLength(14)
    expect(strip.querySelector("[data-status=failed]")).not.toBeNull()
    expect(screen.getByText("◆ test restore")).toBeInTheDocument()
  })

  it("shows the space the backups take and what a run and a restore need", async () => {
    show("overview")
    expect(await screen.findByText("26 GB")).toBeInTheDocument()
    expect(screen.getByText("212 GB free")).toBeInTheDocument()
    expect(screen.getByText("9 GB")).toBeInTheDocument()
    expect(screen.getByText("48 GB")).toBeInTheDocument()
  })

  it("in workspaces scope shows a row per ticked workspace and offers a backup where none is recent", async () => {
    show("overview", "?demo=1&scope=workspaces&ws=dess,sandbox")
    const table = (await screen.findByText("the newest backup of each selected workspace")).closest("section")!
    expect(within(table).getByText("Dess")).toBeInTheDocument()
    expect(within(table).getByText("Sandbox")).toBeInTheDocument()
    expect(within(table).queryByText("Coolify")).toBeNull()
    expect(within(table).getByText("never")).toBeInTheDocument()
    expect(screen.getByText("Sandbox: never backed up")).toBeInTheDocument()
  })
})

describe("scope strip", () => {
  it("switches to selected workspaces, toggles chips, counts N of M and writes the URL", async () => {
    show("overview")
    fireEvent.click(await screen.findByRole("button", { name: "Selected workspaces" }))
    expect(window.location.search).toContain("scope=workspaces")
    const coolify = await screen.findByRole("button", { name: "Coolify" })
    expect(coolify).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByText("5 of 5")).toBeInTheDocument()
    fireEvent.click(coolify)
    expect(screen.getByText("4 of 5")).toBeInTheDocument()
    expect(new URLSearchParams(window.location.search).get("ws")).toBe("dess,unify-lab,pages-demo,sandbox")
  })

  it("says an instance-only page applies to every plan", async () => {
    show("storage")
    expect(await screen.findByText("Instance setting · applies to every backup plan")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Whole instance" })).toBeNull()
  })
})

describe("Backup history", () => {
  it("lists the runs in scope with result and how far each is proven", async () => {
    show("history")
    expect(await screen.findByText("Started")).toBeInTheDocument()
    expect(screen.getAllByText("done · incomplete").length).toBeGreaterThan(0)
    expect(screen.getByText("interrupted · retried")).toBeInTheDocument()
    expect(screen.getByText("test restore · partial")).toBeInTheDocument()
    expect(screen.getByText("manual · before upgrade")).toBeInTheDocument()
    // Workspace runs are not the instance's.
    expect(screen.queryByText("Memory every 6 h", { selector: "td" })).toBeNull()
  })

  it("filters to pinned", async () => {
    const { container } = show("history")
    await screen.findByText("Started")
    fireEvent.click(screen.getByRole("button", { name: "Pinned" }))
    expect(container.querySelectorAll("tr[data-run]")).toHaveLength(1)
  })

  it("opens a run with its phases, encryption, the three proofs and its actions", async () => {
    const { container, onNavigate } = show("history")
    await screen.findByText("Started")
    fireEvent.click(container.querySelector("tr[data-run=run-4]")!)
    const drawer = container.querySelector("[data-slot=run-drawer]") as HTMLElement
    expect(within(drawer).getByText(/copy 6m12s/)).toBeInTheDocument()
    expect(within(drawer).getByText(/AGE · recipients “ops-2026”, “ops-backup”/)).toBeInTheDocument()
    expect(within(drawer).getByText("✓ matches")).toBeInTheDocument()
    expect(within(drawer).getByText("✓ every section read")).toBeInTheDocument()
    expect(within(drawer).getByText("partial")).toBeInTheDocument()
    for (const b of ["Pin", "Check contents…", "Download", "Restore…"]) expect(within(drawer).getByRole(b === "Download" ? "link" : "button", { name: b })).toBeInTheDocument()
    fireEvent.click(within(drawer).getByRole("button", { name: "Restore…" }))
    expect(onNavigate).toHaveBeenCalledWith("recovery")
  })

  it("demo mode never sends a pin", async () => {
    const { container } = show("history")
    await screen.findByText("Started")
    fireEvent.click(container.querySelector("tr[data-run=run-1]")!)
    fireEvent.click(screen.getByRole("button", { name: "Pin" }))
    await waitFor(() => expect(h.toast.message).toHaveBeenCalledWith("Demo data · nothing was sent"))
    expect(h.apiFetch.mock.calls.some(([u]) => String(u).includes("/bundles/pin"))).toBe(false)
  })
})

describe("Schedules", () => {
  it("lists the plans and opens the editor on the first with its presets", async () => {
    show("schedules")
    expect(await screen.findByText("Memory every 6 h")).toBeInTheDocument()
    expect(screen.getByText("Edit plan · What")).toBeInTheDocument()
    expect(screen.getByRole("radio", { name: /Complete recovery/ })).toHaveAttribute("aria-checked", "true")
    expect(screen.getByRole("radio", { name: /Custom backup/ })).toBeInTheDocument()
  })

  it("computes dependencies as a custom plan is edited", async () => {
    const { container } = show("schedules")
    fireEvent.click(await screen.findByRole("radio", { name: /Custom backup/ }))
    const table = container.querySelector("[data-slot=contents-table]") as HTMLElement
    const row = (k: string) => table.querySelector(`tr[data-cat=${k}]`) as HTMLElement
    fireEvent.click(within(row("agents")).getByRole("checkbox"))
    expect(row("agents")).toHaveAttribute("data-state", "required")
    expect(within(row("agents")).getByText(/kept because memory and its version history, routines, schedules and run history, credentials and integrations need it/)).toBeInTheDocument()
    fireEvent.click(within(row("att")).getByRole("checkbox"))
    expect(within(row("att")).getByText("chats will open with missing files")).toBeInTheDocument()
  })

  it("shows the next runs and the calendar legend", async () => {
    const { container } = show("schedules")
    await screen.findByText("Next runs")
    expect(container.querySelector("[data-slot=next-runs]")!.textContent).toMatch(/\d{2}:\d{2}/)
    for (const l of ["done", "skipped / catch-up", "failed", "planned", "environments"]) expect(screen.getAllByText(l).length).toBeGreaterThan(0)
  })

  it("Advanced shows the cron the choice stands for", async () => {
    show("schedules")
    fireEvent.click(await screen.findByRole("button", { name: /Advanced/ }))
    expect(screen.getByLabelText("Cron expression")).toHaveValue("0 3 * * *")
  })

  it("keeps copies local only until off-site exists, and always encrypts", async () => {
    show("schedules")
    expect(await screen.findByRole("button", { name: "add S3-compatible storage under Storage" })).toBeInTheDocument()
    expect(screen.getByText("Google Drive (later)")).toBeInTheDocument()
    expect(screen.getByText(/always · to “ops-2026” and “ops-backup”/)).toBeInTheDocument()
  })

  it("offers every stored off-site destination under Where", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.endsWith("/backups/plans")) return new Response(JSON.stringify({ data: [] }), { status: 200 })
      if (url.endsWith("/backups/settings")) {
        return new Response(JSON.stringify({
          limits: { concurrency: 1, cpu_cores: 2, disk_mbps: 0, upload_mbps: 0 }, heartbeat_url: null, recovery_kit_enabled: false, channels: [],
          events: { failed: true, incomplete: true, stale: true, offsite: true, drill: true }, stale_alert_hours: 36, drill_reminder: "monthly",
          instance_admins: 1, local_path: "/var/lib/crewship/backups",
          destinations: [
            { id: "local", kind: "local", label: "This server", path: "/var/lib/crewship/backups", used_bytes: 1, verified: true, available: true },
            { id: "bdst_1", kind: "s3", label: "r2-backups", path: null, used_bytes: 0, verified: false, available: true },
            { id: "drive", kind: "drive", label: "Google Drive", path: null, used_bytes: null, verified: false, available: false },
          ],
        }), { status: 200 })
      }
      return new Response("{}", { status: 404 })
    })
    show("schedules", "")
    const box = await screen.findByLabelText("r2-backups")
    expect(box).not.toBeDisabled()
    expect(screen.queryByRole("button", { name: "add S3-compatible storage under Storage" })).toBeNull()
  })
})

describe("Storage", () => {
  it("shows this server, Add S3-compatible storage, Drive as later, room and limits", async () => {
    show("storage")
    expect(await screen.findByText("This server · ~/.crewship/backups")).toBeInTheDocument()
    expect(screen.getByText("+ Add S3-compatible storage")).toBeInTheDocument()
    expect(await screen.findByRole("button", { name: "Add…" })).not.toBeDisabled()
    expect(screen.getByText("Google Drive (after S3)")).toBeInTheDocument()
    expect(await screen.findByText("212 GB of 342 GB")).toBeInTheDocument()
    expect(screen.getByLabelText("CPU cores")).toHaveValue(2)
    expect(screen.getByText(/encrypted before it touches disk/)).toBeInTheDocument()
  })

  it("adds S3-compatible storage: the server tests it, and the secret goes in once", async () => {
    const posted: unknown[] = []
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith("/backups/settings")) {
        return new Response(JSON.stringify({
          limits: { concurrency: 1, cpu_cores: 2, disk_mbps: 0, upload_mbps: 0 }, heartbeat_url: null, recovery_kit_enabled: false, channels: [],
          events: { failed: true, incomplete: true, stale: true, offsite: true, drill: true }, stale_alert_hours: 36, drill_reminder: "monthly",
          instance_admins: 1, local_path: "/var/lib/crewship/backups",
          destinations: [{ id: "local", kind: "local", label: "This server", path: "/var/lib/crewship/backups", used_bytes: 1, verified: true, available: true }],
        }), { status: 200 })
      }
      if (url.endsWith("/backups/destinations") && init?.method === "POST") {
        posted.push(JSON.parse(String(init.body)))
        return new Response(JSON.stringify({ destination: { id: "bdst_1" }, test: { ok: true, error: null, tested_at: "x" }, warning: null }), { status: 201 })
      }
      if (url.endsWith("/backups/destinations")) return new Response(JSON.stringify({ data: [] }), { status: 200 })
      return new Response("{}", { status: 404 })
    })
    show("storage", "")
    fireEvent.click(await screen.findByRole("button", { name: "Add…" }))
    const add = screen.getByRole("button", { name: "Test and add" })
    expect(add).toBeDisabled()
    fireEvent.change(screen.getByLabelText("Endpoint"), { target: { value: "https://acct.r2.cloudflarestorage.com" } })
    fireEvent.change(screen.getByLabelText("Region"), { target: { value: "auto" } })
    fireEvent.change(screen.getByLabelText("Bucket"), { target: { value: "crewship-backups" } })
    fireEvent.change(screen.getByLabelText("Access key ID"), { target: { value: "AKID" } })
    expect(add).toBeDisabled()
    fireEvent.change(screen.getByLabelText("Secret access key"), { target: { value: "s3cr3t" } })
    fireEvent.click(screen.getByLabelText(/Allow a private network address/))
    expect(screen.getByText(/loopback or LAN address/)).toBeInTheDocument()
    fireEvent.click(add)
    await waitFor(() => expect(h.toast.success).toHaveBeenCalledWith("Storage added · connection checked"))
    expect(posted[0]).toMatchObject({
      endpoint: "https://acct.r2.cloudflarestorage.com", region: "auto", bucket: "crewship-backups", access_key_id: "AKID",
      secret_access_key: "s3cr3t", path_style: false, allow_private_network: true,
    })
  })

  it("lists a destination with its checked copies, and no Local-only warning once one is verified", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.endsWith("/backups/settings")) {
        return new Response(JSON.stringify({
          limits: { concurrency: 1, cpu_cores: 2, disk_mbps: 0, upload_mbps: 0 }, heartbeat_url: null, recovery_kit_enabled: false, channels: [],
          events: { failed: true, incomplete: true, stale: true, offsite: true, drill: true }, stale_alert_hours: 36, drill_reminder: "monthly",
          instance_admins: 1, local_path: "/b",
          destinations: [
            { id: "local", kind: "local", label: "This server", path: "/b", used_bytes: 1, verified: true, available: true },
            { id: "bdst_1", kind: "s3", label: "r2", path: null, used_bytes: 10, verified: true, available: true },
          ],
        }), { status: 200 })
      }
      if (url.endsWith("/backups/destinations")) {
        return new Response(JSON.stringify({ data: [{
          id: "bdst_1", name: "r2", kind: "s3", endpoint: "https://acct.r2.cloudflarestorage.com", region: "auto", bucket: "crewship-backups",
          prefix: "prod", access_key_id: "AKID", path_style: false, allow_private_network: false, last_test_at: null, last_test_error: null,
          created_at: "2026-09-30T08:00:00Z", copies: 3, copy_bytes: 3000000000, last_verified_at: null, used_by: ["Complete recovery"],
        }] }), { status: 200 })
      }
      return new Response("{}", { status: 404 })
    })
    show("storage", "")
    expect(await screen.findByText("crewship-backups/prod")).toBeInTheDocument()
    expect(screen.getByText(/3 checked copies · 3 GB · used by Complete recovery/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled()
    expect(screen.queryByText(/Local copy only/)).toBeNull()
  })
})

describe("Recovery", () => {
  it("walks Backup → Target → Keys → Checks → Dry run → Restore → Resume", async () => {
    const { container } = show("recovery")
    expect(await screen.findByText("Pick a backup")).toBeInTheDocument()
    fireEvent.click(container.querySelector("tbody tr") as HTMLElement)
    expect(screen.getByRole("radio", { name: /Empty server/ })).toHaveTextContent("crewship recover")
    fireEvent.click(screen.getByRole("radio", { name: /Isolated instance/ }))
    fireEvent.click(screen.getByRole("button", { name: "Next" }))
    fireEvent.click(screen.getByRole("button", { name: "Run the checks" }))
    expect(await screen.findByText("Before anything changes")).toBeInTheDocument()
    expect(screen.getByText(/Docker socket mount on ops/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Run the dry run" }))
    expect(await screen.findByText("12 attachments come back without files")).toBeInTheDocument()
    // An instance target restores where the key is, from the CLI.
    expect(screen.getByText(/crewship backup drill --bundle/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "After the restore: what is held" }))
    expect(await screen.findByText("17 routines")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument()
  })

  it("a workspace restore confirms, then shows its phases", async () => {
    const { container } = show("recovery", "?demo=1&scope=workspaces")
    await screen.findByText("Pick a backup")
    fireEvent.click(container.querySelector("tbody tr") as HTMLElement)
    fireEvent.click(screen.getByRole("radio", { name: /Replace a workspace/ }))
    fireEvent.click(screen.getByRole("button", { name: "Next" }))
    fireEvent.click(screen.getByRole("button", { name: "Run the checks" }))
    fireEvent.click(await screen.findByRole("button", { name: "Run the dry run" }))
    fireEvent.click(await screen.findByRole("button", { name: "Restore…" }))
    const dialog = await screen.findByTestId("confirm-dialog")
    expect(within(dialog).getByText("Everything in the workspace now is replaced by the backup.")).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore" }))
    expect(await screen.findByText("Restoring")).toBeInTheDocument()
    expect(screen.getByText("1 · Database")).toBeInTheDocument()
  })

  it("History and Drills", async () => {
    show("recovery")
    fireEvent.click(await screen.findByRole("button", { name: "History" }))
    expect(await screen.findByText("isolated instance")).toBeInTheDocument()
    expect(screen.getByText("report kept")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Drills" }))
    expect(await screen.findByText(/no message is sent, no webhook called/)).toBeInTheDocument()
  })
})

describe("Keys & alerts", () => {
  it("lists backup keys, vault key versions and the recovery kit warning", async () => {
    show("keys")
    expect(await screen.findByText("ops-2026 · age1q7x…m3k")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Recovery sheet" })).toHaveAttribute("href", "/api/v1/admin/instance/backups/recovery-sheet")
    expect(screen.getByText("ENCRYPTION_KEY · no longer mints")).toBeInTheDocument()
    expect(screen.getByText("still needed by 9 values")).toBeInTheDocument()
    expect(screen.getByText("Whoever holds a private backup key can read every secret.")).toBeInTheDocument()
    expect(screen.getByText("a key is missing on this server")).toBeInTheDocument()
  })

  it("turns the recovery kit on through settings/recovery-kit, after the warning", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (url.endsWith("/backups/vault-keys")) {
        return new Response(JSON.stringify({
          versions: [{ version: "v1", env: "ENCRYPTION_KEY", active: true, envelopes: 3, present: true }],
          recovery_kit: { available: true, enabled: false },
        }), { status: 200 })
      }
      if (url.endsWith("/backups/settings/recovery-kit")) return new Response(JSON.stringify({ enabled: true }), { status: 200 })
      return new Response("{}", { status: 404 })
    })
    show("keys", "")
    fireEvent.click(await screen.findByRole("button", { name: "Turn on…" }))
    expect(screen.getByText("Whoever holds a private backup key can read every secret in those backups.")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Turn on" }))
    await waitFor(() => expect(h.toast.success).toHaveBeenCalledWith("Recovery kit on for new instance backups"))
    const put = h.apiFetch.mock.calls.find(([u]) => String(u).endsWith("/backups/settings/recovery-kit"))
    expect(put?.[1]).toMatchObject({ method: "PUT", body: JSON.stringify({ enabled: true }) })
    expect(h.apiFetch.mock.calls.some(([u, init]) => String(u).endsWith("/backups/settings") && (init as RequestInit | undefined)?.method === "PUT")).toBe(false)
  })

  it("shows the inbox preview, who hears, when, and the heartbeat", async () => {
    show("keys")
    expect(await screen.findByText("Backup needs attention")).toBeInTheDocument()
    expect(screen.getByText(/instance admins \(3\)/)).toBeInTheDocument()
    expect(screen.getByLabelText("newest backup older than 36 h")).toBeChecked()
    expect(screen.getByLabelText("Heartbeat URL")).toHaveValue("https://hc.example.com/ping/…")
  })

  it("will not add a key that is not an AGE public key", async () => {
    show("keys")
    fireEvent.click(await screen.findByRole("button", { name: "+ Add a backup key" }))
    fireEvent.change(screen.getByLabelText("Key name"), { target: { value: "ops-2027" } })
    fireEvent.change(screen.getByLabelText("Public key"), { target: { value: "ssh-rsa AAAA" } })
    expect(screen.getByRole("button", { name: "Add key" })).toBeDisabled()
  })
})

describe("Data retention", () => {
  it("offers 7 d / 30 d / 90 d / 1 y / Forever per row and keeps housekeeping read-only", async () => {
    const { container } = show("retention")
    await screen.findAllByText("Approvals")
    const table = container.querySelector("[data-slot=retention-table]") as HTMLElement
    const row = within(table).getByText("Approvals").closest("tr") as HTMLElement
    expect(within(row).getByRole("button", { name: "90 d" })).toHaveAttribute("aria-pressed", "true")
    for (const o of ["7 d", "30 d", "1 y", "Forever"]) expect(within(row).getByRole("button", { name: o })).toBeInTheDocument()
    // Routine runs cannot be kept forever: its sweep always needs a limit.
    const runs = within(table).getByText("Routine runs").closest("tr") as HTMLElement
    expect(within(runs).queryByRole("button", { name: "Forever" })).toBeNull()
    const hk = container.querySelector("tr[data-key=orphans]") as HTMLElement
    expect(within(hk).queryByRole("button")).toBeNull()
    expect(within(hk).getByText("hourly sweep")).toBeInTheDocument()
  })

  it("warns about several workspaces and confirms a dry run before saving", async () => {
    const { container } = show("retention")
    expect(await screen.findByText("5 workspaces selected.")).toBeInTheDocument()
    const table = container.querySelector("[data-slot=retention-table]") as HTMLElement
    const row = within(table).getByText("Chats").closest("tr") as HTMLElement
    fireEvent.click(within(row).getByRole("button", { name: "90 d" }))
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    const dialog = await screen.findByTestId("confirm-dialog")
    expect(within(dialog).getByText(/Apply 1 limit to every existing workspace\?/)).toBeInTheDocument()
    expect(within(dialog).getByText(/Dess · Chats: Forever → 90 d · 120 rows go at the next sweep/)).toBeInTheDocument()
    expect(within(dialog).getByText(/600 rows go at the next sweep; older backups still hold them/)).toBeInTheDocument()
  })

  it("with one workspace ticked there is no warning", async () => {
    show("retention", "?demo=1&ws=dess")
    await screen.findAllByText("Routine runs")
    expect(screen.queryByText(/workspaces selected/)).toBeNull()
  })

  it("sets defaults for new workspaces as a separate save that touches no existing workspace", async () => {
    const { container } = show("retention")
    await screen.findAllByText("Chats")
    const card = container.querySelector("[data-slot=retention-defaults]") as HTMLElement
    expect(within(card).getByRole("button", { name: "Save defaults for new workspaces…" })).toBeDisabled()
    const row = within(card).getByText("Chats").closest("tr") as HTMLElement
    fireEvent.click(within(row).getByRole("button", { name: "30 d" }))
    fireEvent.click(within(card).getByRole("button", { name: "Save defaults for new workspaces…" }))
    const dialog = await screen.findByTestId("confirm-dialog")
    expect(within(dialog).getByText("Change 1 default for new workspaces?")).toBeInTheDocument()
    expect(within(dialog).getByText("No existing workspace changes.")).toBeInTheDocument()
    expect(within(dialog).getByText("Chats: Forever → 30 d")).toBeInTheDocument()
  })
})

describe("a server without the new endpoints", () => {
  it("says so quietly instead of inventing numbers", async () => {
    show("overview", "")
    expect(await screen.findByText(/Not available on this server yet/)).toBeInTheDocument()
    expect(screen.queryByText("Partial restore verified")).toBeNull()
  })

  it("falls back to the bundles the legacy list holds", async () => {
    h.apiFetch.mockImplementation(async (url: string) => {
      if (String(url).startsWith("/api/v1/admin/backups?")) {
        return new Response(JSON.stringify({ data: [{ path: "/b/x.cship", file_name: "x.cship", size_bytes: 400e6, scope: "workspace", encrypted: true, created_at: "2026-09-29T10:00:00Z", format_version: 2 }] }), { status: 200 })
      }
      if (String(url).startsWith("/api/v1/admin/workspaces")) return new Response(JSON.stringify([{ id: "ws-dess", name: "Dess", slug: "dess" }]), { status: 200 })
      return new Response("{}", { status: 404 })
    })
    show("history", "")
    expect(await screen.findByText(/Run history is not available on this server yet/)).toBeInTheDocument()
    expect(screen.getByText("400 MB")).toBeInTheDocument()
  })
})
