import type { ReactNode } from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import { CrewsContainersSection } from "../crews-containers-section"

// Container limits are written by PATCH /api/v1/crews/{id}, which the server
// gates at roleManage (ADMIN and up). These tests pin the two halves of that
// contract in the UI: an admin gets real inputs, everyone else gets the same
// numbers as text — never a disabled input, which reads as "try me" and then
// 403s.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

// Role is the only thing this section takes from the abilities hook; steer it
// per test rather than standing up workspace/session plumbing.
let role = "OWNER"
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({
    abilities: { can: () => true },
    role,
    capabilities: null,
    hasCapability: () => false,
    loading: false,
  }),
}))

// Workspace-scoped card with its own fetches — out of scope here.
vi.mock("../privileged-credentials-card", () => ({
  PrivilegedCredentialsCard: () => <div data-testid="privileged-credentials-card" />,
}))

// motion spreads animation props into the DOM under test; strip them, and keep
// AnimatePresence eager so expanded content is queryable synchronously.
vi.mock("motion/react", () => ({
  AnimatePresence: ({ children }: { children?: ReactNode }) => <>{children}</>,
  motion: {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    div: ({ children, initial: _i, animate: _a, exit: _e, transition: _t, ...rest }: any) => (
      <div {...rest}>{children}</div>
    ),
  },
}))

// AnimatedNumber is pure motion internals; a plain span keeps the counts assertable.
vi.mock("@/components/ui/animated-number", () => ({
  AnimatedNumber: ({ value }: { value: number }) => <span>{value}</span>,
}))

const CREWS = [
  {
    id: "c1",
    name: "Alpha Crew",
    slug: "alpha",
    status: "active",
    container_memory_mb: 2048,
    container_cpus: 0.5,
    network_mode: "restricted",
    allowed_domains: "github.com, api.openai.com",
    _count: { agents: 3 },
  },
  {
    id: "c2",
    name: "Bravo Crew",
    slug: "bravo",
    status: "active",
    _count: { agents: 1 },
  },
]

function renderSection() {
  return render(<CrewsContainersSection workspaceId="ws1" />)
}

/** Open the accordion for a crew and hand back its name row. */
async function expand(name: string) {
  const row = await screen.findByRole("button", { name: new RegExp(name) })
  fireEvent.click(row)
  return row
}

describe("CrewsContainersSection — container limits", () => {
  beforeEach(() => {
    cleanup()
    role = "OWNER"
    apiFetch.mockReset()
    apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => CREWS })
  })

  it("lists every crew in the workspace", async () => {
    renderSection()
    expect(await screen.findByText("Alpha Crew")).toBeInTheDocument()
    expect(screen.getByText("Bravo Crew")).toBeInTheDocument()
    const [url] = apiFetch.mock.calls[0] as [string]
    expect(url).toBe("/api/v1/crews?workspace_id=ws1")
  })

  it("gives an ADMIN editable memory / CPU / network controls", async () => {
    role = "ADMIN"
    renderSection()
    await expand("Alpha Crew")

    // Memory + CPUs are Radix selects (role=combobox); the network mode is a
    // two-button group and the domain list a real textbox.
    expect(screen.getAllByRole("combobox")).toHaveLength(2)
    expect(screen.getByRole("button", { name: /Restricted/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^Free$/ })).toBeInTheDocument()
    expect(screen.getByRole("textbox", { name: /allowed domains/i })).toHaveValue(
      "github.com, api.openai.com",
    )
  })

  it("surfaces a Save control to an ADMIN only once something changed", async () => {
    role = "ADMIN"
    renderSection()
    await expand("Alpha Crew")

    expect(screen.queryByRole("button", { name: /save/i })).toBeNull()
    fireEvent.change(screen.getByRole("textbox", { name: /allowed domains/i }), {
      target: { value: "github.com" },
    })
    expect(screen.getByRole("button", { name: /save network/i })).toBeInTheDocument()
  })

  it("shows a MEMBER the limits as text — no inputs, no save affordance", async () => {
    role = "MEMBER"
    renderSection()
    await expand("Alpha Crew")

    expect(screen.getByText("2 GB")).toBeInTheDocument()
    expect(screen.getByText("0.5")).toBeInTheDocument()
    expect(screen.getByText("Restricted")).toBeInTheDocument()
    expect(screen.getByText("github.com, api.openai.com")).toBeInTheDocument()

    // A disabled input is still an invitation; there must be nothing to type in.
    expect(screen.queryAllByRole("textbox")).toHaveLength(0)
    expect(screen.queryAllByRole("combobox")).toHaveLength(0)
    expect(screen.queryByRole("button", { name: /save/i })).toBeNull()
    expect(screen.getByText(/managed by workspace admins/i)).toBeInTheDocument()
  })

  it("still shows a MEMBER which crews exist and how big they are", async () => {
    role = "MEMBER"
    renderSection()

    expect(await screen.findByText("Alpha Crew")).toBeInTheDocument()
    expect(screen.getByText("Bravo Crew")).toBeInTheDocument()
    expect(screen.getByText("alpha")).toBeInTheDocument()
    // Read access is unaffected — the overview counts still render.
    expect(screen.getByText("Agents")).toBeInTheDocument()
  })

  it("never PATCHes for a MEMBER — there is no control that could", async () => {
    role = "MEMBER"
    renderSection()
    await expand("Alpha Crew")

    expect(apiFetch.mock.calls.filter(([, init]) => (init as RequestInit)?.method)).toHaveLength(0)
  })
})
