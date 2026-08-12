// The credentials landing pane became a dashboard, for the reason /routines'
// did: the rail to the left was already the list, and the copy in the main pane
// was the one you could not search.
//
// The arithmetic is covered by lib/credentials/__tests__/overview.test.ts and
// tiers.test.ts. What these cover is the wiring those cannot: that the right
// derived number reaches the right tile, that clicking an arc narrows the rail
// by tier, and that the table the page passes in is still on the page.
//
// StatusDonut is stubbed rather than rendered — it is a dashboard component with
// its own SVG concerns, and what is under test here is the data handed to it and
// what comes back out of the click.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

vi.mock("@/components/features/dashboard/status-donut", () => ({
  StatusDonut: ({
    data,
    onSelect,
  }: {
    data: { key: string; label: string; count: number }[]
    onSelect?: (k: string) => void
  }) => (
    <div data-testid="donut">
      {data.map((d) => (
        <button key={d.key} type="button" onClick={() => onSelect?.(d.key)}>
          {d.label} {d.count}
        </button>
      ))}
    </div>
  ),
}))

import { CredentialsOverview } from "../credentials-overview"
import type { CredentialsOverviewCredential } from "../credentials-overview"

const DAY = 24 * 3600 * 1000
// Half a day past the boundary. `daysUntilExpiry` floors, so an exact `+3 * DAY`
// resolves to 2 whenever a millisecond elapses between building the string and
// reading it — a test that fails once every few runs and looks like a real bug.
const inDays = (n: number) => new Date(Date.now() + (n + 0.5) * DAY).toISOString()
const daysAgo = (n: number) => new Date(Date.now() - n * DAY).toISOString()

function cred(
  over: Partial<CredentialsOverviewCredential> & { id: string },
): CredentialsOverviewCredential {
  return {
    name: over.id,
    provider: "NONE",
    status: "ACTIVE",
    scope: "WORKSPACE",
    type: "API_KEY",
    security_level: 1,
    ...over,
  }
}

function renderOverview(
  over: {
    credentials?: CredentialsOverviewCredential[]
    missingToolIds?: Set<string>
    crewsChecked?: number
    readinessLoading?: boolean
    onSelect?: (id: string) => void
    onSelectTier?: (t: string) => void
    onSelectStatus?: (s: "all" | "attention" | "missing-tool") => void
    children?: React.ReactNode
  } = {},
) {
  const onSelect = over.onSelect ?? vi.fn()
  const onSelectTier = over.onSelectTier ?? vi.fn()
  const onSelectStatus = over.onSelectStatus ?? vi.fn()
  render(
    <CredentialsOverview
      credentials={over.credentials ?? []}
      missingToolIds={over.missingToolIds ?? new Set()}
      crewsChecked={over.crewsChecked ?? 1}
      readinessLoading={over.readinessLoading ?? false}
      onSelect={onSelect}
      onSelectTier={onSelectTier}
      onSelectStatus={onSelectStatus}
    >
      {over.children}
    </CredentialsOverview>,
  )
  return { onSelect, onSelectTier, onSelectStatus }
}

/** The KPI tile with this label, as the element that also holds its number. */
function tile(label: RegExp | string) {
  return screen.getByText(label).parentElement!
}

describe("the KPI strip", () => {
  it("reports what is usable out of the whole vault", () => {
    renderOverview({
      credentials: [
        cred({ id: "a", last_used_at: daysAgo(1) }),
        cred({ id: "b", last_used_at: daysAgo(2) }),
        cred({ id: "c", status: "REVOKED" }),
      ],
    })
    const active = tile("Active")
    expect(within(active).getByText("2")).toBeInTheDocument()
    expect(within(active).getByText("of 3 total")).toBeInTheDocument()
  })

  // The tier tile is the one this page did not have. L3 and up are the tiers
  // Keeper mediates per read; everything below is handed to the agent whole.
  it("counts L3 and L4 as guarded, and says what the rest are", () => {
    renderOverview({
      credentials: [
        cred({ id: "a", security_level: 1 }),
        cred({ id: "b", security_level: 2 }),
        cred({ id: "c", security_level: 4 }),
      ],
    })
    const guarded = tile(/Guarded/)
    expect(within(guarded).getByText("1")).toBeInTheDocument()
    expect(within(guarded).getByText(/2 self-service/)).toBeInTheDocument()
  })

  it("says so plainly when nothing in the vault is guarded", () => {
    renderOverview({ credentials: [cred({ id: "a", security_level: 1 })] })
    expect(screen.getByText("every secret is self-service")).toBeInTheDocument()
  })

  it("narrows the rail to the guarded tiers when the tile is clicked", () => {
    const { onSelectTier } = renderOverview({ credentials: [cred({ id: "a" })] })
    fireEvent.click(screen.getByText(/Guarded/))
    expect(onSelectTier).toHaveBeenCalledWith("3")
  })

  it("distinguishes 'no gap' from 'nobody reported' on the tools tile", () => {
    renderOverview({ credentials: [cred({ id: "a" })], crewsChecked: 0 })
    expect(within(tile(/Tools missing/)).getByText("no crew reported")).toBeInTheDocument()
  })
})

describe("the tier donut", () => {
  it("draws every tier, including the ones holding nothing", () => {
    renderOverview({
      credentials: [cred({ id: "a", security_level: 1 }), cred({ id: "b", security_level: 3 })],
    })
    const donut = screen.getByTestId("donut")
    expect(within(donut).getByRole("button", { name: "L1 · low 1" })).toBeInTheDocument()
    expect(within(donut).getByRole("button", { name: "L2 · medium 0" })).toBeInTheDocument()
    expect(within(donut).getByRole("button", { name: "L3 · high 1" })).toBeInTheDocument()
    expect(within(donut).getByRole("button", { name: "L4 · critical 0" })).toBeInTheDocument()
  })

  it("is the tier filter's front door — clicking an arc narrows the rail", () => {
    const { onSelectTier } = renderOverview({
      credentials: [cred({ id: "a", security_level: 4 })],
    })
    fireEvent.click(within(screen.getByTestId("donut")).getByRole("button", { name: /L4/ }))
    expect(onSelectTier).toHaveBeenCalledWith("4")
  })
})

describe("needs attention", () => {
  it("lists what is broken with the reason, worst first", () => {
    renderOverview({
      credentials: [
        cred({ id: "stale", name: "OLD_KEY", last_used_at: daysAgo(200) }),
        cred({ id: "dead", name: "REVOKED_KEY", status: "REVOKED" }),
      ],
    })
    expect(screen.getByText("revoked")).toBeInTheDocument()
    expect(screen.getByText("unused for over 90 days")).toBeInTheDocument()
  })

  it("opens the credential when its row is clicked", () => {
    const { onSelect } = renderOverview({
      credentials: [cred({ id: "dead", name: "REVOKED_KEY", status: "REVOKED" })],
    })
    fireEvent.click(screen.getByRole("button", { name: /REVOKED_KEY/ }))
    expect(onSelect).toHaveBeenCalledWith("dead")
  })

  it("routes Review to the rail's attention filter", () => {
    const { onSelectStatus } = renderOverview({
      credentials: [cred({ id: "dead", status: "REVOKED" })],
    })
    fireEvent.click(screen.getByRole("button", { name: /review/i }))
    expect(onSelectStatus).toHaveBeenCalledWith("attention")
  })

  it("says everything is fine rather than showing an empty list", () => {
    renderOverview({ credentials: [cred({ id: "a", last_used_at: daysAgo(1) })] })
    expect(screen.getByText(/Nothing is expired, stale, or waiting/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /review/i })).not.toBeInTheDocument()
  })
})

describe("the type breakdown", () => {
  it("counts the vault by type", () => {
    renderOverview({
      credentials: [
        cred({ id: "a", type: "API_KEY" }),
        cred({ id: "b", type: "API_KEY" }),
        cred({ id: "c", type: "SSH_KEY" }),
      ],
    })
    expect(screen.getByText("api key")).toBeInTheDocument()
    expect(screen.getByText("ssh key")).toBeInTheDocument()
    expect(screen.getByText("2 types")).toBeInTheDocument()
  })
})

describe("expiring versus recently used", () => {
  // One slot, two questions, and the urgent one wins it.
  it("shows what is expiring when anything is", () => {
    renderOverview({
      credentials: [
        cred({ id: "a", name: "SOON", token_expires_at: inDays(3) }),
        cred({ id: "b", name: "USED", last_used_at: daysAgo(1) }),
      ],
    })
    expect(screen.getByText("Expiring soon")).toBeInTheDocument()
    expect(screen.getByText("3d")).toBeInTheDocument()
    expect(screen.queryByText("Recently used")).not.toBeInTheDocument()
  })

  it("falls back to what was actually used when nothing is expiring", () => {
    renderOverview({ credentials: [cred({ id: "b", name: "USED", last_used_at: daysAgo(1) })] })
    expect(screen.getByText("Recently used")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /USED/ })).toBeInTheDocument()
  })

  it("carries the tier next to the name on both lists", () => {
    renderOverview({
      credentials: [
        cred({ id: "a", name: "PROD_DSN", token_expires_at: inDays(2), security_level: 4 }),
      ],
    })
    expect(screen.getByRole("button", { name: /PROD_DSN L4/ })).toBeInTheDocument()
  })
})

// No table below the cards. The rail is the list, and a second copy of it in
// the main pane was the one you could not search — see credentials-overview.tsx.
describe("the list is not repeated here", () => {
  it("does not render a credential table under the dashboard", () => {
    renderOverview({
      credentials: [cred({ id: "a", name: "GH_TOKEN" }), cred({ id: "b", name: "AWS_MAIN" })],
    })
    expect(screen.queryByRole("table")).not.toBeInTheDocument()
    // The names only appear where a card has a reason to name them, never as a
    // roll call of the whole vault.
    expect(screen.queryByText("AWS_MAIN")).not.toBeInTheDocument()
  })
})
