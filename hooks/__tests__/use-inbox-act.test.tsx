import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor, act } from "@testing-library/react"

import { useInbox, InboxActError, type InboxItem } from "@/hooks/use-inbox"

// #2398 — acting on a run_needs_human card from the web inbox.
//
// The server side (B15, #2389) is `POST /api/v1/inbox/{id}/act`; until this
// hook grew actOnInboxItem, nothing in the web UI called it. These pin the
// contract the card component relies on: the body the endpoint expects, the
// in-place cache flip that lets the card show resolved without a reload, and
// the two 409s that are NOT failures from the person's point of view.

vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => {},
}))

function makeWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

function newQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
}

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response
}

function errJSON(status: number, body: unknown): Response {
  return {
    ok: false,
    status,
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response
}

function card(overrides: Partial<InboxItem> = {}): InboxItem {
  return {
    id: "ibx_run_needs_human_asg_1",
    workspace_id: "ws-1",
    kind: "run_needs_human",
    source_id: "asg_1",
    title: "Casey needs your input on ENG-7",
    state: "unread",
    priority: "high",
    blocking: true,
    attention_class: "input",
    thread_key: "issue:ws-1:m_1",
    actions: [
      { id: "answer", label: "Answer", effect: "Delivers your input to the agent's session and resumes the run", irreversible: false },
      { id: "take_over", label: "Take over", effect: "The agent's session goes idle", irreversible: false },
      { id: "dismiss", label: "Dismiss", effect: "No further work now", irreversible: false },
    ],
    payload: { who_can_act: ["role:MANAGER"], context: { issue: "ENG-7", run: "asg_1" } },
    created_at: "2026-09-05T10:00:00Z",
    updated_at: "2026-09-05T10:00:00Z",
    ...overrides,
  }
}

const receipt = {
  action: "answer",
  acted_by: "usr_1",
  acted_at: "2026-09-05T10:05:00Z",
  inbox_item_id: "ibx_run_needs_human_asg_1",
  session_id: "ses_1",
  agent_version: 3,
  source_run_id: "asg_1",
  comment_id: "cmt_1",
  delivery_id: "mcm_1",
  run_id: "asg_2",
  dispatch_state: "dispatched",
  event_id: "act_1",
  seq: 14,
}

describe("useInbox().actOnInboxItem", () => {
  let mockFetch: ReturnType<typeof vi.fn>
  let qc: QueryClient

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    qc = newQueryClient()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    qc.clear()
  })

  it("POSTs {action, input} to /inbox/{id}/act and returns the receipt", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ rows: [card()], count: 1, unread_count: 1 }))
    const { result } = renderHook(() => useInbox("ws-1", "all"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(1))

    mockFetch.mockResolvedValueOnce(
      okJSON({ id: "ibx_run_needs_human_asg_1", state: "resolved", action: "answer", receipt }),
    )
    let out: unknown
    await act(async () => {
      out = await result.current.actOnInboxItem("ibx_run_needs_human_asg_1", "answer", "Use staging.")
    })

    const [url, init] = mockFetch.mock.calls[1] as [string, RequestInit]
    expect(url).toBe("/api/v1/inbox/ibx_run_needs_human_asg_1/act?workspace_id=ws-1")
    expect(init.method).toBe("POST")
    expect(JSON.parse(init.body as string)).toEqual({ action: "answer", input: "Use staging." })
    expect(out).toMatchObject({ state: "resolved", action: "answer", receipt: { run_id: "asg_2", seq: 14 } })
  })

  it("omits input for take_over / dismiss", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ rows: [card()], count: 1, unread_count: 1 }))
    const { result } = renderHook(() => useInbox("ws-1", "all"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(1))

    mockFetch.mockResolvedValueOnce(
      okJSON({ id: "ibx_run_needs_human_asg_1", state: "resolved", action: "dismiss", receipt: { ...receipt, action: "dismiss" } }),
    )
    await act(async () => {
      await result.current.actOnInboxItem("ibx_run_needs_human_asg_1", "dismiss")
    })
    const [, init] = mockFetch.mock.calls[1] as [string, RequestInit]
    expect(JSON.parse(init.body as string)).toEqual({ action: "dismiss" })
  })

  it("flips the cached row to resolved with the receipt on it — no reload, no refetch", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ rows: [card()], count: 1, unread_count: 1 }))
    const { result } = renderHook(() => useInbox("ws-1", "all"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(1))

    mockFetch.mockResolvedValueOnce(
      okJSON({ id: "ibx_run_needs_human_asg_1", state: "resolved", action: "answer", receipt }),
    )
    await act(async () => {
      await result.current.actOnInboxItem("ibx_run_needs_human_asg_1", "answer", "Use staging.")
    })

    await waitFor(() => expect(result.current.items[0].state).toBe("resolved"))
    const row = result.current.items[0]
    expect(row.resolved_action).toBe("answer")
    expect(row.resolved_at).toBe("2026-09-05T10:05:00Z")
    // The receipt is merged into payload exactly where the server puts it, so
    // a card rendered from the cache and one rendered from a fresh GET agree.
    expect(row.payload?.receipt).toMatchObject({ run_id: "asg_2", seq: 14 })
    expect(row.payload?.who_can_act).toEqual(["role:MANAGER"])
    expect(result.current.unreadCount).toBe(0)
    // One list GET, one act POST — nothing refetched behind the flip.
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })

  it("drops the row from a list whose filter it no longer matches (active view)", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ rows: [card(), card({ id: "other" })], count: 2, unread_count: 2 }))
    const { result } = renderHook(() => useInbox("ws-1", "active"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(2))

    mockFetch.mockResolvedValueOnce(
      okJSON({ id: "ibx_run_needs_human_asg_1", state: "resolved", action: "take_over", receipt: { ...receipt, action: "take_over" } }),
    )
    await act(async () => {
      await result.current.actOnInboxItem("ibx_run_needs_human_asg_1", "take_over")
    })
    await waitFor(() => expect(result.current.items.map((it) => it.id)).toEqual(["other"]))
    expect(result.current.unreadCount).toBe(1)
  })

  it("maps 409 already-acted to InboxActError{code: already_acted} and refetches the list", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ rows: [card()], count: 1, unread_count: 1 }))
    const { result } = renderHook(() => useInbox("ws-1", "all"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(1))

    mockFetch.mockResolvedValueOnce(
      errJSON(409, { error: "already acted on", resolved_action: "dismiss", resolved_by_user_id: "usr_2" }),
    )
    // The refetch the 409 triggers — the server's view of the card.
    mockFetch.mockResolvedValueOnce(
      okJSON({ rows: [card({ state: "resolved", resolved_action: "dismiss" })], count: 1, unread_count: 0 }),
    )

    let caught: unknown
    await act(async () => {
      try {
        await result.current.actOnInboxItem("ibx_run_needs_human_asg_1", "answer", "hello")
      } catch (e) {
        caught = e
      }
    })
    expect(caught).toBeInstanceOf(InboxActError)
    const err = caught as InboxActError
    expect(err.code).toBe("already_acted")
    expect(err.status).toBe(409)
    expect(err.resolvedAction).toBe("dismiss")

    await waitFor(() => expect(result.current.items[0].state).toBe("resolved"))
    expect(result.current.items[0].resolved_action).toBe("dismiss")
    // Somebody else finishing first is not an inbox failure — the page-level
    // error banner stays quiet.
    expect(result.current.error).toBeNull()
  })

  it("maps 409 undeliverable to InboxActError{code: undeliverable} carrying the server's detail", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ rows: [card()], count: 1, unread_count: 1 }))
    const { result } = renderHook(() => useInbox("ws-1", "all"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(1))

    mockFetch.mockResolvedValueOnce(
      errJSON(409, {
        error: "the answer was recorded as a comment but could not be delivered to the agent's session",
        dispatch_state: "held",
        detail: "agent casey is held",
        comment_id: "cmt_9",
      }),
    )
    let caught: unknown
    await act(async () => {
      try {
        await result.current.actOnInboxItem("ibx_run_needs_human_asg_1", "answer", "hello")
      } catch (e) {
        caught = e
      }
    })
    const err = caught as InboxActError
    expect(err).toBeInstanceOf(InboxActError)
    expect(err.code).toBe("undeliverable")
    expect(err.detail).toBe("agent casey is held")
    expect(err.dispatchState).toBe("held")
    // The card stays open: the row is untouched and nothing refetched.
    expect(result.current.items[0].state).toBe("unread")
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })

  it("maps other failures to InboxActError{code: other} with the server's message", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ rows: [card()], count: 1, unread_count: 1 }))
    const { result } = renderHook(() => useInbox("ws-1", "all"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(1))

    mockFetch.mockResolvedValueOnce(errJSON(400, { error: "answer needs a non-empty input" }))
    await expect(
      act(async () => {
        await result.current.actOnInboxItem("ibx_run_needs_human_asg_1", "answer", "")
      }),
    ).rejects.toMatchObject({ code: "other", status: 400, message: "answer needs a non-empty input" })
  })
})
