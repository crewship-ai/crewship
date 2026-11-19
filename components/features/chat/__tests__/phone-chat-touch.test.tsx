import React from "react"
import { readFileSync } from "node:fs"
import path from "node:path"
import { cleanup, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"

// Phone chat at 390 px with a touch pointer (#3869, #3871, #3874, #3883).

const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))

import { EditableUserMessage } from "../messages/editable-user-message"
import { RunPreflight, type ChatRunPreflight } from "../run-preflight"

const root = path.resolve(__dirname, "../../../..")
const src = (p: string) => readFileSync(path.join(root, p), "utf8")
const classes = (el: Element | null) => (el?.getAttribute("class") ?? "").split(/\s+/)

afterEach(() => { cleanup(); fetch.mockReset() })

describe("#3874 Edit & resend on touch and keyboard", () => {
  it("shows on a coarse pointer and when focus is inside, not only on hover", () => {
    render(<EditableUserMessage text="Hello" timestamp={new Date("2026-11-19T10:00:00Z")} onSave={() => {}} />)
    const row = screen.getByRole("button", { name: /Edit/ }).closest(".opacity-0")
    expect(classes(row)).toEqual(expect.arrayContaining(["group-hover:opacity-100", "focus-within:opacity-100", "pointer-coarse:opacity-100"]))
  })
})

describe("#3883 run preflight on a phone", () => {
  it("keeps the composer's gutter and centres its summary in the 44 px touch row", async () => {
    const base: ChatRunPreflight = { scope: "configured_chat_agent", agent_id: "a", model: "m", payer: { state: "ready", category: "connected_provider_account", label: "Payer" }, budgets: [], gate: { configured_mode: "sync", predicted_mode: "none", rule_matches: false, blocking: false, reason: "" }, estimate_usd: null, estimate_state: "unknown", constraints: [] }
    fetch.mockResolvedValue(new Response(JSON.stringify(base)))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
    render(<QueryClientProvider client={client}><RunPreflight chatId="c" workspaceId="w" userId="u" enabled /></QueryClientProvider>)
    const box = await screen.findByTestId("run-preflight")
    expect(classes(box)).toContain("mx-2")
    expect(classes(box.querySelector("summary"))).toEqual(expect.arrayContaining(["flex", "items-center", "coarse:min-h-[44px]"]))
    // A flex summary has no ::marker; it must still read as something that opens.
    expect(box.querySelector("summary [data-testid=run-preflight-chevron]")).not.toBeNull()
  })
})

describe("#3883 Copy link on a phone", () => {
  it("centres its icon in the 44 px touch target and shows Copied with the icon", () => {
    const panel = src("components/features/chat/chat-panel.tsx")
    const fn = panel.slice(panel.indexOf("function CopyLinkButton"), panel.indexOf("function CopyLinkButton") + 1600)
    expect(fn).toMatch(/inline-flex items-center justify-center/)
    expect(fn).toMatch(/coarse:min-w-\[44px\]/)
    expect(fn).toMatch(/copied\s*\?\s*<Check/)
    expect(fn).toMatch(/role="status">\{copied \? "Link copied"/)
  })
})

describe("#3869 empty chat on a phone or with the list folded", () => {
  it("does not point at a list that is not on screen, and offers to open it", () => {
    const client = src("app/(dashboard)/chat/chat-client.tsx")
    const empty = client.slice(client.indexOf("Pick a conversation"), client.indexOf("Pick a conversation") + 1400)
    expect(empty).toMatch(/isMobile \|\| leftCollapsed\s*\?\s*"Or start one with an agent\."/)
    expect(empty).toMatch(/Open conversation list/)
    expect(empty).toMatch(/isMobile \? setDrawerOpen\(true\) : setLeftCollapsed\(false\)/)
  })
})

describe("#3871 chat header on a phone", () => {
  it("marks the Crews path as the area crumb, which a phone hides next to a back control", () => {
    const toolbar = src("components/layout/app-toolbar.tsx")
    const chat = toolbar.slice(toolbar.indexOf("if (isChatPage && chatAgentSlug)"), toolbar.indexOf("if (isChatPage && chatAgentSlug)") + 1800)
    expect(chat).toMatch(/data-slot="area-crumb"/)
    expect(src("app/globals.css")).toMatch(/header:has\(\[data-phone-back\]\) \[data-slot="area-crumb"\] \{ display: none; \}/)
  })

  it("titles the phone bar with the agent's name", () => {
    // Keyed on the phone breakpoint (md), where SubBar hides the Crews path —
    // not the wider compact chat layout, where the path still shows.
    const client = src("app/(dashboard)/chat/chat-client.tsx")
    expect(client).toMatch(/const isPhone = useIsMobile\(\)/)
    expect(client).toMatch(/title=\{isPhone && agent \? agent\.name : "Chat"\}/)
  })
})
