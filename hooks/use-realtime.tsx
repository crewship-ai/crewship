"use client"

import { resolveWsBase } from "@/lib/server-base"
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
} from "react"
import { useWebSocket, type WSMessage, type WSStatus } from "@/hooks/use-websocket"
import { useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"

/** All supported real-time event types broadcast over the workspace WebSocket channel. */
export type RealtimeEventType =
  | "run.started"
  | "run.completed"
  | "run.failed"
  | "agent.status"
  | "agent.created"
  | "agent.updated"
  | "agent.deleted"
  // PR-D F5 ephemeral lifecycle events. Distinct from agent.created
  // because the dashboard renders ephemerals differently (TTL badge,
  // ghost state, rehire affordance) and needs to know which axis of
  // change to refresh.
  | "agent.hired"
  | "agent.expired"
  | "agent.rehired"
  | "assignment.updated"
  // Per-assignment lifecycle events introduced for the per-crew
  // admission queue (PR #396 — Phase 1B of the queue mechanism).
  // Distinct from the generic "assignment.updated" because the queue
  // pump fires both: "assignment_unqueued" first (UI can animate the
  // dequeue), then "assignment_running" once runAssignment starts.
  // Underscore naming matches the backend broadcast — session-channel
  // events have always used snake_case while workspace-wide events
  // use dot-notation; these are emitted on BOTH channels so the
  // toolbar (workspace-scoped) and chat view (session-scoped) can
  // each subscribe with the same name.
  | "assignment_queued"
  | "assignment_unqueued"
  | "escalation.created"
  | "escalation.resolved"
  | "mission.updated"
  // An issue changed — `{id, identifier?}`, and `status`/`assignee` where the
  // handler has one. `id` is the mission id at every emitter without
  // exception, which is what makes an id filter on the subscriber side safe;
  // `docs/api-reference/websocket.mdx` documents the same shape. Broadcast
  // from a dozen places — code links, attachments, the PATCH path, workflow,
  // relations, comments, agent-to-agent assignment and the internal
  // agent-facing routes, several of them via `issueEvents.record`
  // (internal/api/issue_events.go). It is NOT the same thing as
  // "mission.updated": that one comes from the mission ENGINE and reports the
  // run. Both are needed. This one was emitted for a long time with nothing
  // able to receive it, because the type was missing here and from
  // VALID_REALTIME_TYPES — so an issue a human had open never learned that an
  // agent had written to it.
  | "issue.updated"
  // Issue lifecycle events emitted alongside issue.updated but, until A6
  // (docs/prd/PRD-ISSUES-AND-ROUTINES-2026.md, #2125), missing from both this
  // union and VALID_REALTIME_TYPES below — so handleMessage silently dropped
  // every one of them. `issue.created` — `internal/api/issue_handler_create.go`
  // (payload `{id}`) — also `issues_internal.go`, `recurring_issue_dispatcher.go`,
  // `pages_wake_issue.go`. `issue.deleted` — `internal/api/issue_handler_update.go`
  // (payload `{identifier}`), the only emitter. `issue.started` —
  // `internal/api/issue_handler_workflow.go` (payload `{id, identifier, status}`),
  // the only emitter. See `hooks/__tests__/realtime-allowlist-issue-events.test.ts`
  // for the guard that keeps this list honest against the Go source.
  | "issue.created"
  | "issue.deleted"
  | "issue.started"
  // An issue's STATUS specifically changed — `{id, identifier, crew_id,
  // status, from, to}`. Distinct from the generic issue.updated above:
  // that one says "refetch this issue", this one carries enough to move a
  // card between board columns (or decide the change is off-screen)
  // without a fetch. Emitted alongside issue.updated by every
  // status-transition endpoint (#2257): the human and agent PATCH, and
  // the review/stop workflow actions.
  | "issue.status_changed"
  // Multiple issues changed in one bulk-edit request — `{count}`, no per-row
  // identity, so a subscriber's only correct response is a full refetch.
  | "issues.bulk_updated"
  // A comment mentioning an agent was received and durably recorded — pushed
  // BEFORE any model call, from mentionRecorder.record
  // (internal/api/issue_mentions.go, PRD-ISSUES-AND-ROUTINES-2026 §9.3/§15,
  // work package B2, #2337). `{mission_id, identifier, agent_id,
  // delivery_id, event_id, seq}`. This is the "Frontend received your
  // message" signal §15 calls for — it lands well before the run itself
  // could ever produce one, closing the gap where a human watching the
  // issue learned nothing until the agent's own comment appeared.
  | "issue.delivery.acked"
  | "task.updated"
  | "peer_conversation.updated"
  | "crew.created"
  | "crew.updated"
  | "crew.deleted"
  // Broadcast on the workspace channel after a workspace is cascade-deleted
  // (#866/#890). Lets other connected tabs/users of the now-gone workspace
  // redirect out instead of hammering dead endpoints.
  | "workspace.deleted"
  | "agent.log"
  | "file.event"
  | "container.stats"
  | "provision.started"
  | "provision.progress"
  // Structured per-step / per-feature provisioning frame (resolve, build,
  // each feature install, container create, ready, failure) with a bounded
  // BuildKit log tail. Richer source than provision.progress — the
  // provisioning hook prefers it for the granular "installing ansible" view.
  | "provision.event"
  | "provision.completed"
  | "provision.failed"
  | "pipeline.run.started"
  | "pipeline.run.completed"
  | "pipeline.run.failed"
  | "pipeline.step.started"
  | "pipeline.step.completed"
  | "pipeline.step.failed"
  | "pipeline.step.validation_failed"
  | "pipeline.waitpoint.created"
  // The catalog moved: a routine was saved, approved, rejected,
  // disabled, enabled or deleted. Run events only ever said "a routine
  // RAN"; nothing said "a routine appeared", which is why the routines
  // overview needed a Refresh button to see an agent's work.
  | "pipeline.saved"
  | "inbox.updated"
  // A chat session was renamed — `{agent_id, chat_id, title}`. Emitted by
  // PATCH /agents/{id}/chats/{chatId} (internal/api/agent_chats_rename.go) so
  // a sidebar open elsewhere repaints the row instead of polling for it.
  // snake_case matches the backend broadcast, like the assignment_* events.
  | "chat_renamed"
  // A producer pushed a panel payload. Broadcast on the per-page channel
  // `page:{pageId}` and carrying NO payload, only "panel X changed" — the
  // client re-reads through the normal authorised path so the per-panel
  // permission filter cannot be bypassed by a broadcast reaching a subscriber
  // who should not see the data (docs/prd/pages.md §10b.5b).
  | "page.panel.updated"
  // Feed-relevant journal rows forwarded by the journal→WS bridge
  // (internal/server/journal_ws_bridge.go), carrying the same serialized shape
  // the SSE stream serves (lib/types/journal.ts). NOTE: this is opt-in
  // plumbing with NO consumer yet — the bridge broadcasts on the dedicated
  // `journal:{workspaceId}` channel, which nothing here subscribes to, so the
  // event does not currently arrive. The journal feed is still served by the
  // SSE stream (hooks/use-journal-stream.ts), which keeps the gap-free
  // Last-Event-ID replay this best-effort channel does not. The type + set
  // entry below are scaffolding so a future consumer can dispatch it without a
  // wire-contract change; until then it is deliberately inert.
  | "journal.entry"
  // #2125: the rest of the documented `workspace:{id}` vocabulary
  // (docs/api-reference/websocket.mdx). These 40 types were already
  // emitted server-side and silently dropped by VALID_REALTIME_TYPES below
  // — no subscriber uses them yet (that is the scope A6 drew for the
  // issue.* subset, and this PR keeps: registering the type is the durable
  // fix, wiring a consumer per type is separate, future work). The parity
  // gate in hooks/__tests__/realtime-allowlist-docs-parity.test.ts fails
  // if this list and the Set below ever drift from the docs table again.
  | "mission.created"
  | "confidence.low"
  | "approval.required"
  | "approval.resolved"
  | "project.created"
  | "project.updated"
  | "project.deleted"
  | "milestone.created"
  | "milestone.updated"
  | "milestone.deleted"
  | "integration.created"
  | "integration.updated"
  | "integration.deleted"
  | "credential.expired"
  | "escalation.expired"
  | "escalation.cancelled"
  | "agent.hire_approved"
  | "agent.skill_assigned"
  | "agent.skill_unassigned"
  | "port_expose.created"
  | "port_expose.revoked"
  | "pipeline.step.skipped"
  | "pipeline.step.retrying"
  | "instance_setting.updated"
  | "feature_flag.created"
  | "feature_flag.updated"
  | "feature_flag.deleted"
  | "feature_flag.override_set"
  | "feature_flag.override_cleared"
  | "workflow_template.created"
  | "workflow_template.updated"
  | "workflow_template.deleted"
  | "triage_rule.created"
  | "triage_rule.updated"
  | "triage_rule.deleted"
  | "triage.processed"
  | "recurring_issue.created"
  | "recurring_issue.updated"
  | "recurring_issue.deleted"
  // Synthetic client-side event — NEVER sent by the server (and deliberately
  // absent from VALID_REALTIME_TYPES so a wire message can't spoof it).
  // Dispatched by the provider after the socket RE-connects, so pure-WS
  // consumers (no poll backstop — e.g. the file browser) can refetch state
  // whose change events they may have missed during the gap.
  | "realtime.reconnected"

/** A real-time event received from the WebSocket, with typed payload and timestamp. */
export interface RealtimeEvent {
  type: RealtimeEventType
  payload: Record<string, unknown>
  timestamp: Date
}

type EventCallback = (event: RealtimeEvent) => void

interface RealtimeContextValue {
  status: WSStatus
  subscribe: (eventType: RealtimeEventType, callback: EventCallback) => () => void
  subscribeChannel: (channel: string) => () => void
}

// Exported (only) so the guard test can check it against the Go emitters —
// nothing in the app should read this directly; subscribe via
// useRealtimeEvent instead.
export const VALID_REALTIME_TYPES: Set<string> = new Set([
  "run.started", "run.completed", "run.failed",
  "agent.status", "agent.created", "agent.updated", "agent.deleted",
  // PR-D F5 ephemeral lifecycle. Without these in the allowlist the
  // ghost UI never updates without a manual page refresh.
  "agent.hired", "agent.expired", "agent.rehired",
  "assignment.updated", "assignment_queued", "assignment_unqueued",
  "escalation.created",
  "escalation.resolved", "mission.updated", "task.updated",
  // Without this, handleMessage drops every issue broadcast and the issue
  // detail can only learn about an agent's write by being reloaded.
  "issue.updated",
  // A6 (#2125): these three were emitted server-side and dropped here.
  // Registered together with issue.updated; see the RealtimeEventType union
  // above for the emitter file:line for each.
  "issue.created", "issue.deleted", "issue.started",
  // #2257: a status transition specifically, not just "the issue changed".
  "issue.status_changed",
  // #2337 (B2): the mention-received ack, pushed before dispatch even
  // starts. Without this in the allowlist, handleMessage drops it and a
  // human watching the issue has no signal until the agent's own reply
  // appears — exactly the gap §15's "Acknowledgement under one second"
  // exists to close.
  "issue.delivery.acked",
  "peer_conversation.updated", "crew.created", "crew.updated", "crew.deleted",
  // Without this in the allowlist, workspace.deleted is dropped by
  // handleMessage and the redirect-on-delete listener never fires (#890).
  "workspace.deleted",
  "agent.log", "file.event", "container.stats",
  "provision.started", "provision.progress", "provision.event", "provision.completed", "provision.failed",
  // Pipeline run events — RunsView + WaitpointRunDetail subscribe.
  "pipeline.run.started", "pipeline.run.completed", "pipeline.run.failed",
  "pipeline.step.started", "pipeline.step.completed", "pipeline.step.failed",
  "pipeline.step.validation_failed",
  // Inbox + waitpoint events. Without these in the allowlist
  // handleMessage drops them and the bell + /inbox stop refreshing
  // in real time — silent regression that would only surface as
  // "the badge count looks stuck."
  "pipeline.waitpoint.created",
  "pipeline.saved",
  "inbox.updated",
  // Without this in the allowlist handleMessage drops the rename and every
  // sidebar but the one that issued the PATCH keeps the old title until a
  // reload.
  "chat_renamed",
  // Pages liveness. handleMessage drops any type missing from this set, so
  // without the entry an open page would simply never update — the exact
  // "easy to forget" step docs/prd/pages.md §10b.5b calls out by name.
  "page.panel.updated",
  // Journal entries forwarded by the journal→WS bridge on the opt-in
  // `journal:{workspaceId}` channel. Allowlisted so a future consumer's
  // subscription dispatches them; nothing subscribes to that channel yet, so
  // today no such frame is delivered. See the type union above.
  "journal.entry",
  // #2125: the rest of the documented workspace-channel vocabulary — see
  // the matching block in the RealtimeEventType union above for why these
  // are registered with no subscriber yet, and the parity gate that keeps
  // this Set from drifting from docs/api-reference/websocket.mdx again.
  "mission.created", "issues.bulk_updated",
  "confidence.low", "approval.required", "approval.resolved",
  "project.created", "project.updated", "project.deleted",
  "milestone.created", "milestone.updated", "milestone.deleted",
  "integration.created", "integration.updated", "integration.deleted", "credential.expired",
  "escalation.expired", "escalation.cancelled",
  "agent.hire_approved", "agent.skill_assigned", "agent.skill_unassigned",
  "port_expose.created", "port_expose.revoked",
  "pipeline.step.skipped", "pipeline.step.retrying",
  "instance_setting.updated",
  "feature_flag.created", "feature_flag.updated", "feature_flag.deleted",
  "feature_flag.override_set", "feature_flag.override_cleared",
  "workflow_template.created", "workflow_template.updated", "workflow_template.deleted",
  "triage_rule.created", "triage_rule.updated", "triage_rule.deleted", "triage.processed",
  "recurring_issue.created", "recurring_issue.updated", "recurring_issue.deleted",
])

// warnedUnknownTypes tracks which dropped frame TYPES have already been
// logged this page load. #2125: handleMessage's drop was completely silent
// — the server logged a successful broadcast, the client discarded it, and
// nothing anywhere said a type had been sent and refused. Module-level (not
// a ref) so the warning survives a RealtimeProvider remount and still fires
// exactly once per type, not once per frame — a hot type like container.stats
// arriving every few seconds must not spam the console once it's been seen.
const warnedUnknownTypes = new Set<string>()

const RealtimeContext = createContext<RealtimeContextValue | null>(null)

function getWsUrl(): string {
  // During SSR resolveWsBase() returns "" so useWebSocket skips connecting;
  // the client-side re-render computes the real URL. Same-origin default
  // uses the page host:port (dev-server.mjs proxies /ws; in production the
  // Go binary serves both), a desktop shell's server base overrides it.
  const base = resolveWsBase()
  return base === "" ? "" : `${base}/ws`
}

/**
 * Context provider that manages a single WebSocket connection for real-time events.
 * Auto-subscribes to the workspace channel and re-subscribes component channels after reconnect.
 *
 * The previous version cached the WS ticket in state and silently swallowed
 * a 401 from /ws-token (`.catch(() => {})`), which is exactly the failure
 * mode the user hit: backend restart → token state stays null → useWebSocket
 * skips connect → ReconnectBanner cycles "Reconnecting…" forever. We now
 * pass a `getToken` callback that re-fetches per (re)connect attempt and
 * lets apiFetch propagate auth failures upward — a 401 emits the global
 * session-expired event, which the AuthProvider turns into a hard redirect.
 */
export function RealtimeProvider({ children }: { children: React.ReactNode }) {
  const { workspaceId } = useWorkspace()
  const listenersRef = useRef<Map<string, Set<EventCallback>>>(new Map())
  const activeChannelsRef = useRef<Set<string>>(new Set())
  const statusRef = useRef<string>("disconnected")

  const getToken = useCallback(async (): Promise<string | null> => {
    // Two error paths, deliberately handled differently:
    //   - 401 / 403: real auth death. Return null; useWebSocket
    //     stops trying (apiFetch already emitted session-expired
    //     globally for the 401 case).
    //   - apiFetch throws (network rejection, abort) OR non-2xx
    //     non-auth status (5xx, 429) OR malformed JSON: transient.
    //     Throw; useWebSocket's catch path schedules the next
    //     backoff attempt instead of terminating.
    const res = await apiFetch("/api/v1/ws-token")
    if (res.status === 401 || res.status === 403) return null
    if (!res.ok) {
      throw new Error(`/api/v1/ws-token returned ${res.status}`)
    }
    const data = await res.json() // throws on malformed JSON — also transient
    if (typeof data?.token !== "string") {
      throw new Error("/api/v1/ws-token response missing token field")
    }
    return data.token
  }, [])

  const dispatchEvent = useCallback(
    (type: RealtimeEventType, payload: Record<string, unknown>) => {
      const event: RealtimeEvent = { type, payload, timestamp: new Date() }
      const callbacks = listenersRef.current.get(type)
      if (callbacks) {
        for (const cb of callbacks) {
          try { cb(event) } catch { /* prevent subscriber errors from breaking others */ }
        }
      }
    },
    [],
  )

  const handleMessage = useCallback(
    (msg: WSMessage) => {
      if (!VALID_REALTIME_TYPES.has(msg.type)) {
        if (!warnedUnknownTypes.has(msg.type)) {
          warnedUnknownTypes.add(msg.type)
          // Intentional: this is the one signal that a real, server-sent
          // event type is being silently dropped (#2125). See
          // warnedUnknownTypes above for why it's deduped per type rather
          // than logged per frame.
          console.warn(
            `[realtime] dropping unknown event type "${msg.type}" — ` +
              "not in VALID_REALTIME_TYPES (hooks/use-realtime.tsx). " +
              "If the server is meant to send this, add it there.",
          )
        }
        return
      }
      dispatchEvent(
        msg.type as RealtimeEventType,
        (typeof msg.payload === "object" && msg.payload !== null
          ? msg.payload as Record<string, string>
          : {}),
      )
    },
    [dispatchEvent],
  )

  // Reconnect resync: the chat socket resumes its own stream via onConnect,
  // but this shared provider previously gave subscribers NO signal that a
  // WS gap happened — hooks with a poll backstop self-healed while pure-WS
  // consumers (file browser) stayed stale until their next event. On every
  // reconnect after the first successful connect, broadcast a synthetic
  // "realtime.reconnected" so those consumers refetch what they missed.
  const hasConnectedOnceRef = useRef(false)
  const handleConnect = useCallback(() => {
    if (!hasConnectedOnceRef.current) {
      hasConnectedOnceRef.current = true
      return
    }
    dispatchEvent("realtime.reconnected", {})
  }, [dispatchEvent])

  const { status, send } = useWebSocket({
    url: getWsUrl(),
    getToken,
    onMessage: handleMessage,
    onConnect: handleConnect,
  })

  useEffect(() => { statusRef.current = status }, [status])

  // Subscribe to workspace channel when connected
  useEffect(() => {
    if (status !== "connected" || !workspaceId) return
    send({ type: "subscribe", channel: `workspace:${workspaceId}` })
    // Re-subscribe any component-registered channels after reconnect
    for (const ch of activeChannelsRef.current) {
      send({ type: "subscribe", channel: ch })
    }
    return () => {
      send({ type: "unsubscribe", channel: `workspace:${workspaceId}` })
    }
  }, [status, workspaceId, send])

  const subscribeChannel = useCallback(
    (channel: string): (() => void) => {
      activeChannelsRef.current.add(channel)
      if (status === "connected") {
        send({ type: "subscribe", channel })
      }
      return () => {
        activeChannelsRef.current.delete(channel)
        if (statusRef.current === "connected") {
          send({ type: "unsubscribe", channel })
        }
      }
    },
    [status, send],
  )

  const subscribe = useCallback(
    (eventType: RealtimeEventType, callback: EventCallback): (() => void) => {
      if (!listenersRef.current.has(eventType)) {
        listenersRef.current.set(eventType, new Set())
      }
      listenersRef.current.get(eventType)!.add(callback)
      return () => {
        listenersRef.current.get(eventType)?.delete(callback)
      }
    },
    [],
  )

  const contextValue = useMemo(
    () => ({ status, subscribe, subscribeChannel }),
    [status, subscribe, subscribeChannel],
  )

  return (
    <RealtimeContext.Provider value={contextValue}>
      {children}
    </RealtimeContext.Provider>
  )
}

/** Access the real-time event system (status, subscribe, subscribeChannel). Must be used within RealtimeProvider. */
export function useRealtime(): RealtimeContextValue {
  const ctx = useContext(RealtimeContext)
  if (!ctx) {
    throw new Error("useRealtime must be used within a RealtimeProvider")
  }
  return ctx
}

/**
 * Provider-tolerant status read — the `useRealtimeEventSafe` of the connection
 * state. Returns the socket's status, or null when no RealtimeProvider is
 * mounted (a unit test, a public surface), which callers must treat as "not
 * connected" rather than "unknown, probably fine".
 *
 * Exists because a surface with NO poll backstop has to be able to say so:
 * `RealtimeStatusBanner` shows the app-wide outage after three seconds, but a
 * per-page indicator needs the same state without throwing outside the
 * provider (`components/features/pages/live-indicator.tsx`).
 */
export function useRealtimeStatusSafe(): WSStatus | null {
  return useContext(RealtimeContext)?.status ?? null
}

/**
 * Subscribe to a specific realtime event type.
 * The callback is called whenever the event fires.
 * Returns the latest event of this type (or null).
 */
export function useRealtimeEvent(
  eventType: RealtimeEventType,
  callback: EventCallback,
): void {
  const { subscribe } = useRealtime()
  const callbackRef = useRef(callback)
  useEffect(() => { callbackRef.current = callback }, [callback])

  useEffect(() => {
    return subscribe(eventType, (event) => callbackRef.current(event))
  }, [eventType, subscribe])
}

/**
 * Provider-tolerant variant of useRealtimeEvent: subscribes when a
 * RealtimeProvider is mounted and silently no-ops when it isn't. For hooks
 * that must stay usable (and unit-testable) outside the dashboard layout —
 * data hooks like use-paymaster gain liveness when realtime is available
 * without hard-requiring the provider.
 */
export function useRealtimeEventSafe(
  eventType: RealtimeEventType,
  callback: EventCallback,
): void {
  const ctx = useContext(RealtimeContext)
  const callbackRef = useRef(callback)
  useEffect(() => { callbackRef.current = callback }, [callback])

  const subscribe = ctx?.subscribe
  useEffect(() => {
    if (!subscribe) return
    return subscribe(eventType, (event) => callbackRef.current(event))
  }, [eventType, subscribe])
}

/** Subscribe to a WebSocket channel (e.g. "agent:{id}") for the lifetime of the calling component. */
export function useRealtimeChannel(channel: string | null): void {
  const { subscribeChannel } = useRealtime()
  useEffect(() => {
    if (!channel) return
    return subscribeChannel(channel)
  }, [channel, subscribeChannel])
}

/**
 * Provider-tolerant channel subscription — the `useRealtimeEventSafe` of
 * channels. A data hook that subscribes to a per-record channel (Pages'
 * `page:{pageId}`, §10b.5b) is otherwise untestable without mounting the
 * provider, and mounting the provider in a unit test opens a socket.
 */
export function useRealtimeChannelSafe(channel: string | null): void {
  const ctx = useContext(RealtimeContext)
  const subscribeChannel = ctx?.subscribeChannel
  useEffect(() => {
    if (!channel || !subscribeChannel) return
    return subscribeChannel(channel)
  }, [channel, subscribeChannel])
}
