// The AI providers space's cards and list (Harbor 2). What is worth pinning:
// a quota the server does not report is said, never drawn; the list is the
// default look from 13 accounts; the health strip opens the views it counts.

import * as React from "react"
import { describe, it, expect, vi } from "vitest"
import { fireEvent, render, screen, within } from "@testing-library/react"

import { AccountCard, AccountsList, ProviderHead, ProviderHealth } from "../provider-accounts"
import type { LoginCredential, ProviderLogin } from "@/lib/credentials/provider-logins"

function login(over: Partial<ProviderLogin> = {}): ProviderLogin {
  return {
    mode: "subscription",
    provider: "ANTHROPIC",
    plan: "max",
    plan_label: "Max 20×",
    owner_user_id: "u1",
    owner_email: "pavel@example.test",
    expires_at: null,
    refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null },
    quota: null,
    delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" },
    pays_for: { agents: 2, crews: 0 },
    ...over,
  }
}
function acc(id: string, over: Partial<ProviderLogin> = {}, status = "ACTIVE"): LoginCredential {
  return { id, name: `Account ${id}`, provider: over.provider ?? "ANTHROPIC", status, login: login(over) }
}

describe("AccountCard", () => {
  it('quota null renders "No usage data yet", no bars', () => {
    render(<AccountCard c={acc("a")} onOpen={() => {}} />)
    expect(screen.getByText("No usage data yet")).toBeInTheDocument()
    expect(screen.queryAllByRole("progressbar")).toHaveLength(0)
    expect(screen.getByText("pays for 2 agents")).toBeInTheDocument()
    expect(screen.getByText("pavel@example.test")).toBeInTheDocument()
    expect(screen.getByTestId("account-state-ready")).toHaveTextContent("Ready")
  })

  it("draws both windows when the server reports them", () => {
    render(<AccountCard c={acc("a", { quota: { window_5h_pct: 62, window_weekly_pct: 41, resets_at: null } })} onOpen={() => {}} />)
    expect(screen.getAllByRole("progressbar")).toHaveLength(2)
    expect(screen.queryByText("No usage data yet")).not.toBeInTheDocument()
  })

  it("draws 'Near the limit' only when a reported window is at 80% or more", () => {
    const { rerender } = render(<AccountCard c={acc("a", { quota: { window_5h_pct: 62, window_weekly_pct: 41, resets_at: null } })} onOpen={() => {}} />)
    expect(screen.queryByText("Near the limit")).not.toBeInTheDocument()
    rerender(<AccountCard c={acc("a", { quota: { window_5h_pct: 62, window_weekly_pct: 84, resets_at: null } })} onOpen={() => {}} />)
    expect(screen.getByText("Near the limit")).toBeInTheDocument()
    rerender(<AccountCard c={acc("a")} onOpen={() => {}} />)
    expect(screen.queryByText("Near the limit")).not.toBeInTheDocument()
  })

  it("with no quota but recorded usage, says the tokens and calls of the last 30 days instead of 'No usage data yet'", () => {
    render(<AccountCard c={acc("a")} usage={{ tokens: 1_250_000, calls: 47 }} onOpen={() => {}} />)
    expect(screen.queryByText("No usage data yet")).not.toBeInTheDocument()
    expect(screen.getByText("1.3M tokens · 47 calls · last 30 days")).toBeInTheDocument()
    expect(screen.queryAllByRole("progressbar")).toHaveLength(0)
    expect(document.body.textContent).not.toMatch(/\$\d/)
  })

  it("quota bars and usage line sit together when both exist", () => {
    render(<AccountCard c={acc("a", { quota: { window_5h_pct: 10, window_weekly_pct: 20, resets_at: null } })} usage={{ tokens: 900, calls: 1 }} onOpen={() => {}} />)
    expect(screen.getAllByRole("progressbar")).toHaveLength(2)
    expect(screen.getByText("900 tokens · 1 call · last 30 days")).toBeInTheDocument()
  })

  it("a signed-out account says Sign in again; one that pays for nobody says Not used", () => {
    const { rerender } = render(<AccountCard c={acc("a", { refresh: { supported: true, status: "needs_relogin", last_at: null, next_at: null, error: null } })} onOpen={() => {}} />)
    expect(screen.getByTestId("account-state-signin")).toHaveTextContent("Sign in again")
    rerender(<AccountCard c={acc("b", { pays_for: { agents: 0, crews: 0 } })} onOpen={() => {}} />)
    expect(screen.getByTestId("account-state-idle")).toHaveTextContent("Not used")
    expect(screen.getByText("pays for no one")).toBeInTheDocument()
  })

  it("opens the account", () => {
    const onOpen = vi.fn()
    render(<AccountCard c={acc("a")} onOpen={onOpen} />)
    fireEvent.click(screen.getByRole("button", { name: /Account a/ }))
    expect(onOpen).toHaveBeenCalledWith("a")
  })
})

describe("AccountsList", () => {
  const list = (n: number) => Array.from({ length: n }, (_, i) => acc(`a${i}`))
  const props = { onLayoutChange: vi.fn(), group: "none" as const, onGroupChange: vi.fn(), onOpen: vi.fn(), poolOf: () => null }

  it("cards up to 12 accounts, the list from 13", () => {
    const { rerender } = render(<AccountsList rows={list(12)} layout={null} {...props} onAdd={() => {}} />)
    expect(document.querySelector("[data-slot=provider-accounts]")).toHaveAttribute("data-layout", "cards")
    expect(screen.getByRole("button", { name: /Add AI provider/ })).toBeInTheDocument()
    rerender(<AccountsList rows={list(13)} layout={null} {...props} />)
    expect(document.querySelector("[data-slot=provider-accounts]")).toHaveAttribute("data-layout", "list")
    expect(within(screen.getByRole("table", { name: "AI provider accounts" })).getAllByRole("row")).toHaveLength(14)
  })

  it("a phone keeps cards; the switch picks either", () => {
    const onLayoutChange = vi.fn()
    render(<AccountsList rows={list(20)} layout={null} {...props} onLayoutChange={onLayoutChange} isPhone />)
    expect(document.querySelector("[data-slot=provider-accounts]")).toHaveAttribute("data-layout", "cards")
    fireEvent.click(screen.getByRole("radio", { name: "List" }))
    expect(onLayoutChange).toHaveBeenCalledWith("list")
  })

  it("loads 50 more and groups the list by provider", () => {
    const rows = [...list(55), acc("oa", { provider: "OPENAI" })]
    const { rerender } = render(<AccountsList rows={rows} layout="list" {...props} />)
    expect(screen.getByText("Showing 50 of 56")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /Load 50 more/ }))
    expect(screen.getByText("Showing 56 of 56")).toBeInTheDocument()
    rerender(<AccountsList rows={rows} layout="list" {...props} group="provider" />)
    expect(screen.getByRole("region", { name: "ChatGPT / OpenAI · 1" })).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Claude / Anthropic · 55" })).toBeInTheDocument()
  })
})

describe("AccountsList · kit Segmented (COM-02)", () => {
  const list = (n: number) => Array.from({ length: n }, (_, i) => acc(`a${i}`))
  const props = { onLayoutChange: vi.fn(), group: "none" as const, onGroupChange: vi.fn(), onOpen: vi.fn(), poolOf: () => null }

  it("Show accounts as and Group accounts are the kit control: one tab stop, arrows select", () => {
    const onGroupChange = vi.fn()
    render(<AccountsList rows={list(13)} layout="list" {...props} onGroupChange={onGroupChange} />)
    for (const name of ["Show accounts as", "Group accounts"]) {
      expect(screen.getByRole("radiogroup", { name })).toHaveAttribute("data-slot", "segmented")
    }
    const group = screen.getByRole("radiogroup", { name: "Group accounts" })
    expect(within(group).getAllByRole("radio").filter((r) => r.tabIndex === 0)).toHaveLength(1)
    fireEvent.keyDown(within(group).getByRole("radio", { name: "None" }), { key: "ArrowRight" })
    expect(onGroupChange).toHaveBeenCalledWith("provider")
  })
})

describe("ProviderHealth", () => {
  it("counts and opens the views; people is not a door", () => {
    const onOpen = vi.fn()
    render(<ProviderHealth counts={{ accounts: 7, subscriptions: 4, apiKeys: 3, signIn: 1, idle: 2, people: 3 }} onOpen={onOpen} />)
    fireEvent.click(screen.getByRole("button", { name: /^1 need a new sign-in/i }))
    expect(onOpen).toHaveBeenCalledWith("attention")
    fireEvent.click(screen.getByRole("button", { name: /^2 pay for nothing/i }))
    expect(onOpen).toHaveBeenCalledWith("unassigned")
    expect(screen.queryByRole("button", { name: /people with an account/i })).not.toBeInTheDocument()
    expect(screen.getByText("4 subscriptions · 3 API keys")).toBeInTheDocument()
  })
})

describe("ProviderHead", () => {
  it("says how the provider signs in, who runs it, how agents get it and the workspace default", () => {
    render(<ProviderHead provider="OPENAI" rows={[acc("c", { provider: "OPENAI", delivery: { kind: "file", target: ".codex/auth.json" }, refresh: { supported: true, status: "ok", last_at: null, next_at: null, error: null } })]} workspaceDefault="OpenAI production" onAdd={() => {}} />)
    expect(screen.getByRole("heading", { level: 1, name: "ChatGPT / OpenAI" })).toBeInTheDocument()
    expect(screen.getByText("Codex CLI, OpenCode")).toBeInTheDocument()
    expect(screen.getByText("file .codex/auth.json")).toBeInTheDocument()
    expect(screen.getByText("Renews itself on the server")).toBeInTheDocument()
    expect(screen.getByText("OpenAI production")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Add ChatGPT account/ })).toBeInTheDocument()
  })
})

describe("AccountsList · usage (phone)", () => {
  it("passes each account's usage to its card on a phone", () => {
    const props = { onLayoutChange: vi.fn(), group: "none" as const, onGroupChange: vi.fn(), onOpen: vi.fn(), poolOf: () => null }
    render(<AccountsList rows={[acc("a"), acc("b")]} layout={null} {...props} isPhone usage={{ a: { tokens: 2000, calls: 3 } }} />)
    expect(screen.getByText("2k tokens · 3 calls · last 30 days")).toBeInTheDocument()
    expect(screen.getAllByText("No usage data yet")).toHaveLength(1)
  })
})
