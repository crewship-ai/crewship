import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// A deep link to a routine transcript must not open into an empty column.
//
// The conversations column pages by kind now: `?kind=direct` narrows inside
// the query, before LIMIT, which is the only place a routine minting one chat
// per step can be stopped from evicting a person's conversations. The scope
// starts at `direct` on every mount, and that is deliberate — arriving at this
// page is arriving at your conversations.
//
// But the page is also arrived at sideways. `/chat/<slug>?session=<id>` is the
// shape internal/chatnotify puts in an inbox item, `crewship open` builds, and
// every routines / crews / dashboard link points at. A session of any other
// kind is then not in the fan-out at all, and the surface had no way to say so:
// the transcript rendered, the column showed a Direct list that did not contain
// it, nothing was selected, and the connection bar lost the origin chip because
// `activeThread` resolves out of the same fan-out.
//
// So: when the scoped fan-out settles WITHOUT the session the URL named, the
// page asks once what kind that session is and moves the scope to match.
// =============================================================================

let searchParams = new URLSearchParams()
vi.mock("next/navigation", () => ({ useSearchParams: () => searchParams }))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
}))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => {} }))
vi.mock("@/lib/telemetry", () => ({ emitChatEvent: vi.fn() }))

// The column is stubbed down to the two things this file is about: which
// scope it was handed, and whether the open session is among its rows.
vi.mock("@/components/features/chat/conversations-sidebar", async () => {
  const actual = await vi.importActual<
    typeof import("@/components/features/chat/conversations-sidebar")
  >("@/components/features/chat/conversations-sidebar")
  return {
    ...actual,
    ConversationsSidebar: ({
      scope,
      onScopeChange,
      threadsByAgent,
      activeThreadId,
    }: {
      scope: string
      onScopeChange: (s: string) => void
      threadsByAgent: Record<string, { id: string }[]>
      activeThreadId?: string | null
    }) => (
      <div
        data-testid="sidebar"
        data-scope={scope}
        data-holds-active={String(
          Object.values(threadsByAgent).some((list) =>
            list.some((t) => t.id === activeThreadId),
          ),
        )}
      >
        <button data-testid="scope-routine" onClick={() => onScopeChange("routine")} />
      </div>
    ),
  }
})

vi.mock("@/components/features/chat/chat-panel", () => ({
  ChatPanel: ({
    agentSlug,
    sessionId,
    sessionOrigin,
  }: {
    agentSlug: string
    sessionId: string
    sessionOrigin?: string | null
  }) => (
    <div
      data-testid="chat-panel"
      data-agent={agentSlug}
      data-session={sessionId}
      data-origin={sessionOrigin ?? "(none)"}
    />
  ),
}))

import { ChatClient } from "../chat-client"

const casey = { id: "a-casey", name: "Casey", slug: "casey", status: "IDLE", crew_id: "c1" }

const DIRECT_ROW = {
  id: "direct-1",
  title: "Deploy rollback",
  status: "ACTIVE",
  message_count: 4,
  started_at: "2026-08-30T09:00:00Z",
  last_activity_at: "2026-08-30T09:00:00Z",
  kind: "direct",
  origin: "UI",
  mode: "CHAT",
}
const ROUTINE_ROW = {
  id: "run-step-9",
  title: "Daily digest · summarize",
  status: "ACTIVE",
  message_count: 2,
  started_at: "2026-08-31T07:20:00Z",
  last_activity_at: "2026-08-31T07:20:00Z",
  kind: "routine",
  origin: "ROUTINE",
  mode: "CHAT",
}

const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetchMock(...args),
}))

function ok(body: unknown, headers: Record<string, string> = {}) {
  return Promise.resolve({
    ok: true,
    status: 200,
    headers: new Headers(headers),
    json: () => Promise.resolve(body),
  } as Response)
}

function setUrl(pathname: string, search: string) {
  Object.defineProperty(window, "location", {
    configurable: true,
    value: { pathname, search, href: `http://localhost${pathname}${search}` },
  })
  searchParams = new URLSearchParams(search)
}

/** Every `/chats` call the page made, as the raw URL string. */
const chatCalls = () =>
  apiFetchMock.mock.calls.map((c) => String(c[0])).filter((u) => u.includes("/chats"))

describe("<ChatClient> — a deep link brings the column to the conversation", () => {
  beforeEach(() => {
    Object.defineProperty(window, "innerWidth", { value: 1280, writable: true, configurable: true })
    window.history.replaceState = vi.fn()
    apiFetchMock.mockReset()
    apiFetchMock.mockImplementation((url: string) => {
      const u = String(url)
      if (/\/api\/v1\/agents\?/.test(u)) return ok([casey])
      if (/\/api\/v1\/agents\/a-casey\/chats/.test(u)) {
        // The server narrows BEFORE its LIMIT, so a scoped request simply does
        // not contain the other kinds. Modelling that faithfully is the whole
        // point — a stub that returns everything regardless of `kind` would
        // pass whether or not the page ever sends the parameter.
        const kind = new URL(u, "http://x").searchParams.get("kind") ?? ""
        const all = [DIRECT_ROW, ROUTINE_ROW]
        const rows = kind === "" || kind === "all"
          ? all
          : all.filter((r) => kind.split(",").includes(r.kind))
        return ok(rows, { "X-Chat-Kind-Counts": "direct=1,routine=1,issue=0,agent=0" })
      }
      return ok({})
    })
  })
  afterEach(() => vi.restoreAllMocks())

  it("moves the scope to Routines for a routine session the Direct fetch cannot hold", async () => {
    setUrl("/chat/casey", "?session=run-step-9")
    render(<ChatClient />)

    await waitFor(() =>
      expect(screen.getByTestId("sidebar").getAttribute("data-scope")).toBe("routine"),
    )
    // …and the column now actually holds the row, which is the thing the
    // reader sees. A scope that moved without the list following it would be
    // the same silent absence wearing a different label.
    await waitFor(() =>
      expect(screen.getByTestId("sidebar").getAttribute("data-holds-active")).toBe("true"),
    )
  })

  it("recovers the origin chip, which resolves out of the same fan-out", async () => {
    setUrl("/chat/casey", "?session=run-step-9")
    render(<ChatClient />)

    await waitFor(() =>
      expect(screen.getByTestId("chat-panel").getAttribute("data-origin")).toBe("ROUTINE"),
    )
  })

  it("leaves a direct session alone — no probe, no scope change", async () => {
    // The common case must cost nothing. A page that re-asks the server on
    // every arrival has turned a fix for the sideways path into a tax on the
    // main one.
    setUrl("/chat/casey", "?session=direct-1")
    render(<ChatClient />)

    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())
    await new Promise((r) => setTimeout(r, 50))

    expect(screen.getByTestId("sidebar").getAttribute("data-scope")).toBe("direct")
    expect(chatCalls().some((u) => u.includes("kind=all"))).toBe(false)
  })

  it("does not drag the column back when the reader changes scope by hand", async () => {
    // The probe's own failure mode, and it is worse than the gap it closes.
    // Switching to Routines makes the OPEN direct conversation absent from the
    // fan-out — which is the probe's exact trigger — so it resolved that
    // session, found `direct`, and set the scope back. The bucket strip
    // bounced under the cursor and Routines was unreachable while any direct
    // conversation was open.
    //
    // Bringing the column to the conversation is for ARRIVING. Once the reader
    // has said where they want to be, they have answered the question the
    // probe exists to ask.
    setUrl("/chat/casey", "?session=direct-1")
    render(<ChatClient />)
    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())

    fireEvent.click(screen.getByTestId("scope-routine"))

    await waitFor(() =>
      expect(screen.getByTestId("sidebar").getAttribute("data-scope")).toBe("routine"),
    )
    // …and it stays there. The probe would have moved it back on the next
    // settle of the fan-out.
    await new Promise((r) => setTimeout(r, 150))
    expect(screen.getByTestId("sidebar").getAttribute("data-scope")).toBe("routine")
  })

  it("asks once, not once per render", async () => {
    // The failure mode of a resolve-then-refetch effect: the probe changes the
    // scope, the scope re-runs the fan-out, the fan-out settles, and the effect
    // fires again. A session that is genuinely absent — a draft with no row
    // yet — would loop forever.
    setUrl("/chat/casey", "?session=nowhere-at-all")
    render(<ChatClient />)

    await waitFor(() => expect(screen.getByTestId("chat-panel")).toBeInTheDocument())
    await new Promise((r) => setTimeout(r, 120))

    const probes = chatCalls().filter((u) => u.includes("kind=all"))
    expect(probes).toHaveLength(1)
    // An unresolvable session leaves the scope where the reader put it.
    expect(screen.getByTestId("sidebar").getAttribute("data-scope")).toBe("direct")
  })
})
