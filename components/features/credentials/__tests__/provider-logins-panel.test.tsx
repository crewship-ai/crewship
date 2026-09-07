// The Providers tab (PRD provider-logins §6.1). What is worth pinning: every
// state the wireframe draws — empty, at limit, expiring, needs re-login,
// unassigned — reaches the screen with its own words, the tiles say what the
// rows say, and a fact the server did not send is drawn as unknown rather
// than as a number.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { ProviderLoginsPanel } from "../provider-logins-panel"
import type { LoginCredential, ProviderLogin } from "@/lib/credentials/provider-logins"

const DAY = 24 * 3600 * 1000
// An hour of slack on every expiry: the fixture's Date.now() runs a few ms
// ahead of the render's, and "212 d" floors to 211 without it.
const SLACK = 3600 * 1000

function login(over: Partial<ProviderLogin> = {}): ProviderLogin {
  return {
    mode: "subscription",
    provider: "ANTHROPIC",
    plan: "max",
    plan_label: "Max 20×",
    owner_user_id: "u1",
    owner_email: "pavel@unify.cz",
    expires_at: new Date(Date.now() + 212 * DAY + SLACK).toISOString(),
    refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null },
    quota: { window_5h_pct: 62, window_weekly_pct: 31, resets_at: null },
    delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" },
    pays_for: { agents: 3, crews: 1 },
    ...over,
  }
}

function row(name: string, over: Partial<ProviderLogin> = {}, status = "ACTIVE"): LoginCredential {
  return { id: name, name, provider: over.provider ?? "ANTHROPIC", status, login: login(over) }
}

const active = row("Claude Max · pavel")
const expiring = row("Claude Max · jana", { expires_at: new Date(Date.now() + 18 * DAY + SLACK).toISOString(), pays_for: { agents: 2, crews: 0 } })
const atLimit = row("ChatGPT Plus · jana", {
  provider: "OPENAI",
  plan: "plus",
  plan_label: "ChatGPT Plus",
  owner_email: "jana@unify.cz",
  expires_at: new Date(Date.now() + 2 * DAY + 4 * 3600_000 + 60_000).toISOString(),
  refresh: { supported: true, status: "ok", last_at: null, next_at: null, error: null },
  quota: { window_5h_pct: 100, window_weekly_pct: 71, resets_at: new Date(Date.now() + 2 * 3600_000).toISOString() },
  delivery: { kind: "file", target: ".codex/auth.json" },
  pays_for: { agents: 1, crews: 0 },
})
const unassigned = row("Cursor Pro · pavel", { provider: "CURSOR", plan: "pro", plan_label: "Pro", expires_at: null, quota: null, pays_for: { agents: 0, crews: 0 } })
const relogin = row("Gemini · jana", {
  provider: "GOOGLE",
  refresh: { supported: true, status: "needs_relogin", last_at: null, next_at: null, error: "invalid_grant" },
  pays_for: { agents: 1, crews: 0 },
})
const all = [active, expiring, atLimit, unassigned, relogin]

function renderPanel(over: Partial<React.ComponentProps<typeof ProviderLoginsPanel>> = {}) {
  const onSelect = vi.fn()
  const onSelectStatus = vi.fn()
  const onAdd = vi.fn()
  const onAssign = vi.fn()
  render(
    <ProviderLoginsPanel
      logins={all}
      visible={all}
      onSelect={onSelect}
      onSelectStatus={onSelectStatus}
      onAdd={onAdd}
      onAssign={onAssign}
      {...over}
    />,
  )
  return { onSelect, onSelectStatus, onAdd, onAssign }
}

describe("empty", () => {
  it("says what a provider login is and offers the wizard", () => {
    const { onAdd } = renderPanel({ logins: [], visible: [] })
    expect(screen.getByText(/no provider logins yet/i)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /add provider login/i }))
    expect(onAdd).toHaveBeenCalled()
  })

  it("hides the button from a reader who cannot create", () => {
    renderPanel({ logins: [], visible: [], onAdd: undefined })
    expect(screen.queryByRole("button", { name: /add provider login/i })).not.toBeInTheDocument()
  })
})

describe("the tiles", () => {
  it("count seats, at limit, expiring and unassigned from the rows", () => {
    renderPanel()
    expect(screen.getByRole("button", { name: /^seats/i })).toHaveTextContent("5")
    expect(screen.getByRole("button", { name: /^seats/i })).toHaveTextContent("5 subscription · 0 API keys")
    expect(screen.getByRole("button", { name: /^at limit/i })).toHaveTextContent("1")
    expect(screen.getByRole("button", { name: /^at limit/i })).toHaveTextContent(/resets \d{1,2}:\d{2} · 1 agent waiting/)
    expect(screen.getByRole("button", { name: /^expiring/i })).toHaveTextContent("auto-refresh handles 1 of 2")
    expect(screen.getByRole("button", { name: /^unassigned/i })).toHaveTextContent("no agent pays with it yet")
  })

  it("are the rail's front door", () => {
    const { onSelectStatus } = renderPanel()
    fireEvent.click(screen.getByRole("button", { name: /^at limit/i }))
    expect(onSelectStatus).toHaveBeenCalledWith("at_limit")
    fireEvent.click(screen.getByRole("button", { name: /^unassigned/i }))
    expect(onSelectStatus).toHaveBeenCalledWith("unassigned")
  })
})

describe("the rows", () => {
  it("draw plan, expiry, pays-for, quota and the status pill in the wireframe's words", () => {
    renderPanel()
    const list = screen.getByRole("list", { name: /provider logins/i })
    const rows = within(list).getAllByRole("listitem")
    expect(rows).toHaveLength(5)

    const claude = within(rows[0])
    expect(claude.getByText("Claude Max · pavel")).toBeInTheDocument()
    expect(claude.getByText("Anthropic · subscription · CLAUDE_CODE_OAUTH_TOKEN")).toBeInTheDocument()
    expect(claude.getByText("Max 20×")).toBeInTheDocument()
    expect(claude.getByText("in 212 d")).toBeInTheDocument()
    expect(claude.getByText("3 agents · 1 crew")).toBeInTheDocument()
    expect(claude.getByRole("progressbar", { name: /5h window/i })).toHaveAttribute("aria-valuenow", "62")
    expect(claude.getByTestId("login-status-active")).toHaveTextContent("active")

    const codex = within(rows[2])
    expect(codex.getByText("OpenAI · subscription · file .codex/auth.json")).toBeInTheDocument()
    expect(codex.getByText(/in 2 d 4 h · auto/)).toBeInTheDocument()
    expect(codex.getByTestId("login-status-at_limit")).toHaveTextContent(/at limit → \d{1,2}:\d{2}/)

    expect(within(rows[1]).getByTestId("login-status-expiring")).toHaveTextContent("expiring")
    expect(within(rows[4]).getByTestId("login-status-needs_relogin")).toHaveTextContent("needs re-login")
  })

  it("says when quota cannot be read, and never invents an expiry", () => {
    renderPanel()
    const cursor = within(screen.getAllByRole("listitem")[3])
    expect(cursor.getByText(/quota not readable for this provider/i)).toBeInTheDocument()
    expect(cursor.getByText("—")).toBeInTheDocument()
    expect(cursor.getByText("nobody yet")).toBeInTheDocument()
  })

  it("an unassigned seat carries Assign; the others do not", () => {
    const { onAssign, onSelect } = renderPanel()
    const rows = screen.getAllByRole("listitem")
    expect(within(rows[0]).queryByRole("button", { name: /^assign$/i })).not.toBeInTheDocument()
    fireEvent.click(within(rows[3]).getByRole("button", { name: /^assign$/i }))
    expect(onAssign).toHaveBeenCalledWith("Cursor Pro · pavel")
    // Assign is its own control — it does not also open the seat.
    expect(onSelect).not.toHaveBeenCalled()
  })

  it("opens the seat when the row is clicked", () => {
    const { onSelect } = renderPanel()
    fireEvent.click(screen.getByRole("button", { name: /open ChatGPT Plus · jana/i }))
    expect(onSelect).toHaveBeenCalledWith("ChatGPT Plus · jana")
  })

  it("says how many the filters left", () => {
    renderPanel({ visible: [atLimit] })
    expect(screen.getByText("1 of 5")).toBeInTheDocument()
    expect(screen.getAllByRole("listitem")).toHaveLength(1)
  })
})
