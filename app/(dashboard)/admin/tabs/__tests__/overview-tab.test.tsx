import { describe, it, expect } from "vitest"
import { fireEvent, render, screen, within } from "@testing-library/react"

import { OverviewTab } from "../overview-tab"

// The overview showed four counters, three green dots, the licence and the
// telemetry toggle — and claimed everything was fine, because it only
// rendered things that were fine. The instance meanwhile knew its rate
// limiter was off, did not say which build it was running, had disk figures
// nobody read, and verified a tamper-evident journal that nothing displayed.

const HEALTH = {
  uptime_seconds: 93784,
  db: { connected: true },
  disk: { path: "/var/lib/crewship", free_bytes: 15_300_000_000, total_bytes: 48_000_000_000, used_pct: 68.1 },
  log_level: { level: "info", baseline: "info" },
  encryption_key_source: "external",
}

const LICENSE = {
  edition: "community",
  max_crews: 15,
  max_agents_per_crew: 10,
  max_members: 5,
  features: ["keeper", "scrubber"],
}

function renderTab(over: Record<string, unknown> = {}) {
  return render(
    <OverviewTab
      stats={{ workspaces: 1, users: 2, crews: 3, agents: 8, running: 0 }}
      runtimeAvailable
      runtimeInfo={{ runtime: "docker", version: "29.3.0", socket: "/var/run/docker.sock" }}
      health={HEALTH}
      license={LICENSE}
      telemetry={{ enabled: true }}
      version={{ current: "v0.9.2", latest: "v0.9.4", newer: true, url: "https://example.test/rel" }}
      posture={{
        environment: "",
        warnings: [
          { key: "rate_limit_disabled", severity: "medium", message: "The API rate limiter is OFF." },
          { key: "private_egress_open", severity: "info", message: "Private-egress ceiling is open." },
        ],
      }}
      journal={{ ok: true, entries_verified: 32007, checkpoints: 2 }}
      keeper={null}
      {...over}
    />,
  )
}

describe("Admin overview — what needs attention comes first", () => {
  it("shows the warnings the instance already computed", () => {
    renderTab()
    const panel = screen.getByRole("region", { name: /needs attention/i })
    expect(within(panel).getByText(/rate limiter is OFF/i)).toBeInTheDocument()
    expect(within(panel).getByText(/private-egress/i)).toBeInTheDocument()
  })

  // A long list scrolls inside the card; each finding is its first sentence,
  // with the rest one click away.
  it("keeps findings to a line each, in a scrolling list", () => {
    renderTab({ posture: { environment: "", warnings: [
      { key: "rate_limit_disabled", severity: "high", message: "The API rate limiter is OFF. Anyone can hammer the login endpoint." },
    ] } })
    expect(document.querySelector("[data-slot=admin-findings]")?.className).toContain("overflow-y-auto")
    expect(screen.getByText("The API rate limiter is OFF.")).toBeInTheDocument()
    const details = screen.getByRole("button", { name: "Details" })
    expect(details).toHaveAttribute("aria-expanded", "false")
    fireEvent.click(details)
    expect(screen.getByRole("button", { name: "Less" })).toHaveAttribute("aria-expanded", "true")
  })

  it("says everything is clear rather than hiding the block", () => {
    renderTab({ posture: { environment: "", warnings: [] } })
    // A missing block reads as "not checked". An explicit all-clear is a
    // different claim, and the only one that is true here.
    expect(screen.getByRole("region", { name: /needs attention/i })).toBeInTheDocument()
    expect(screen.getByText(/nothing needs attention/i)).toBeInTheDocument()
  })

  it("does not claim an all-clear when the posture could not be read", () => {
    renderTab({ posture: null })
    expect(screen.queryByText(/nothing needs attention/i)).toBeNull()
  })
})

describe("Admin overview — instance identity", () => {
  it("names the build and the update waiting for it", () => {
    renderTab()
    const hero = screen.getByRole("region", { name: "Instance status" })
    expect(within(hero).getByText("v0.9.2")).toBeInTheDocument()
    expect(within(hero).getByText(/v0\.9\.4 available/)).toBeInTheDocument()
  })

  it("keeps quiet about updates when this build is current", () => {
    renderTab({ version: { current: "v0.9.4", latest: "v0.9.4", newer: false } })
    expect(within(screen.getByRole("region", { name: "Instance status" })).getByText("v0.9.4")).toBeInTheDocument()
    expect(screen.queryByText(/available/i)).toBeNull()
  })

  it("names the edition, not the licence id", () => {
    renderTab()
    expect(within(screen.getByRole("region", { name: "Instance status" })).getByText("Community Edition")).toBeInTheDocument()
  })

  it("reports disk headroom on the volume that fills", () => {
    renderTab()
    const platform = screen.getByRole("region", { name: "Platform" })
    expect(within(platform).getByText("68%")).toBeInTheDocument()
    expect(within(platform).getByText(/15\.3 GB free/)).toBeInTheDocument()
  })

  it("does not render missing disk figures as zero", () => {
    renderTab({ health: { ...HEALTH, disk: { error: "statfs: not supported" } } })
    expect(screen.queryByText(/0 B free/)).toBeNull()
    expect(screen.getByText(/statfs: not supported/)).toBeInTheDocument()
  })
})

describe("Admin overview — capacity against the licence", () => {
  it("reads each count against the ceiling that applies to it", () => {
    renderTab()
    const panel = screen.getByRole("region", { name: /capacity/i })
    expect(within(panel).getByText("3 / 15")).toBeInTheDocument()
    expect(within(panel).getByText("2 / 5")).toBeInTheDocument()
  })

  it("shows a bare count where the edition imposes no ceiling", () => {
    renderTab({ license: { ...LICENSE, max_members: 0 } })
    const panel = screen.getByRole("region", { name: /capacity/i })
    expect(within(panel).getByText("2")).toBeInTheDocument()
  })
})

// The verdict line reads the same probes as the cards below it, and the one
// slow probe says it is still running instead of claiming a result.
describe("Admin overview — health checks", () => {
  it("calls the instance healthy only when every check passes and nothing is flagged", () => {
    renderTab({ posture: { environment: "", warnings: [] } })
    expect(screen.getByText("All systems healthy")).toBeInTheDocument()
  })

  it("counts findings to review", () => {
    renderTab()
    expect(screen.getByText("1 finding to review")).toBeInTheDocument()
  })

  it("puts a failing check ahead of findings", () => {
    renderTab({ health: { ...HEALTH, db: { connected: false, error: "locked" } } })
    expect(screen.getByText("1 check failing")).toBeInTheDocument()
    expect(document.querySelector('[data-check="db"]')?.getAttribute("data-state")).toBe("bad")
  })

  it("shows the journal walk as pending, not as unchecked", () => {
    renderTab({ journal: null, journalPending: true })
    expect(document.querySelector('[data-check="journal"]')?.getAttribute("data-state")).toBe("pending")
    expect(screen.getAllByText(/Verifying…/).length).toBeGreaterThan(0)
  })

  it("reads the host daemon and the week's runs", () => {
    renderTab({
      daemon: { status: "ok", connections: 2, uptime: "1h2m3.5s" },
      runs: [{ ts: "2026-09-28T00:00:00Z", value: 3 }, { ts: "2026-09-29T00:00:00Z", value: 4 }],
    })
    expect(within(screen.getByRole("region", { name: "Platform" })).getByText(/up 1h 2m/)).toBeInTheDocument()
    expect(within(screen.getByRole("region", { name: "Runs this week" })).getByText(/runs in the last 7 days/)).toBeInTheDocument()
  })
})

describe("Admin overview — integrity", () => {
  it("reports the journal chain's own verdict", () => {
    renderTab()
    const panel = screen.getByRole("region", { name: /integrity/i })
    expect(within(panel).getByText(/32,?007/)).toBeInTheDocument()
  })

  // The verify endpoint answers {ok, count, checkpoints}; reading only the
  // older field names printed "0 entries verified" over a 284k-entry chain.
  it("reads the entry count the server actually sends", () => {
    renderTab({ journal: { ok: true, count: 284249, checkpoints: 4 } })
    expect(within(screen.getByRole("region", { name: /integrity/i })).getByText(/284,249 entries verified/)).toBeInTheDocument()
  })

  it("says where the encryption key came from", () => {
    renderTab()
    const panel = screen.getByRole("region", { name: /integrity/i })
    expect(within(panel).getByText(/external/i)).toBeInTheDocument()
  })

  // "generated" means the key file sits next to the database, so a copied
  // disk carries the ciphertext and what opens it. The word alone does not
  // say that.
  it("spells out what a generated key means", () => {
    renderTab({ health: { ...HEALTH, encryption_key_source: "generated" } })
    expect(screen.getByText(/beside the database/i)).toBeInTheDocument()
  })

  // The runtime line used to capitalise whatever string arrived, so a machine
  // running OrbStack read "Orbstack" and one running Rancher Desktop read
  // "Rancher". Product names are not a capitalisation of their socket label.
  it("names the runtime in use the way its vendor writes it", () => {
    renderTab({ runtimeInfo: { runtime: "orbstack", version: "29.4.0", socket: "/var/run/docker.sock" } })
    expect(within(screen.getByRole("region", { name: "Platform" })).getByText(/OrbStack 29\.4\.0/)).toBeInTheDocument()
  })

  // Runtimes installed, none driving anything: the server started without a
  // container provider. That is neither "Docker 29" nor "Not detected", and
  // the old label rendered it as "Unknown " (#1690).
  it("distinguishes a detected runtime from one that is actually in use", () => {
    renderTab({ runtimeAvailable: true, runtimeInfo: null })
    expect(within(screen.getByRole("region", { name: "Platform" })).getByText(/none in use/i)).toBeInTheDocument()
    expect(screen.queryByText(/unknown/i)).toBeNull()
  })

  it("still says so when nothing is detected at all", () => {
    renderTab({ runtimeAvailable: false, runtimeInfo: null })
    expect(within(screen.getByRole("region", { name: "Platform" })).getByText(/not detected/i)).toBeInTheDocument()
  })
})
