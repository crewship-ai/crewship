"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { journalEntrySchema, type JournalEntry } from "@/lib/types/journal"
import { apiFetch } from "@/lib/api-fetch"

/** Connection state for the journal SSE stream. */
export type JournalStreamStatus = "idle" | "connecting" | "connected" | "error" | "polling"

interface UseJournalStreamOptions {
  workspaceId: string | null
  /** Query params forwarded to the endpoint (entry_type, crew_id, severity, …). */
  params?: Record<string, string | undefined>
  enabled?: boolean
  /** Called whenever an entry arrives (live stream or poll cycle). */
  onEntry: (entry: JournalEntry) => void
}

interface UseJournalStreamResult {
  status: JournalStreamStatus
  lastError: string | null
  /**
   * The polling fallback could not walk back far enough to cover
   * everything that happened while the stream was down, so entries between
   * the last watermark and the oldest row it fetched were never delivered.
   * Sticky until the caller reconnects — a silently short feed is the one
   * failure the user cannot detect for themselves.
   */
  gapDetected: boolean
  /** Abandon the backoff and re-open the stream now. Clears `gapDetected`. */
  reconnect: () => void
}

/** Poll page size. Deliberately small: most ticks return far fewer. */
const POLL_LIMIT = 50
/**
 * How many pages one poll tick will walk to close a gap. 4 × 50 = 200
 * entries per 5 s tick, which covers a stream outage of a few minutes on a
 * busy workspace without turning a reconnect into an unbounded backfill of
 * the entire retention window.
 */
const POLL_MAX_PAGES = 4
const POLL_INTERVAL_MS = 5000
/** First reconnect delay; doubles per consecutive failure. */
const RECONNECT_BASE_MS = 1000
/** Ceiling on the reconnect delay. Attempts themselves are unbounded — the
 *  poll fallback keeps the UI fed meanwhile, so giving up entirely would
 *  only strand the user on a slower feed with no way back. */
const RECONNECT_CEILING_MS = 30000
/** Shown when the catch-up walk could not reach the watermark. */
const GAP_MESSAGE = "Some entries may be missing — reconnect to reload the window."

/**
 * Equal-jitter exponential backoff: half of the window is fixed and half is
 * random, so N tabs knocked offline by one server restart do not all retry
 * on the same millisecond. Returns a delay in [d/2, d) where d is the
 * doubling schedule clamped to the ceiling.
 */
function reconnectDelay(attempt: number): number {
  const capped = Math.min(RECONNECT_BASE_MS * 2 ** attempt, RECONNECT_CEILING_MS)
  return capped / 2 + Math.random() * (capped / 2)
}

/**
 * Subscribe to `GET /api/v1/journal/stream` as an EventSource. If the stream
 * drops (server restart, proxy timeout, offline), fall back to polling
 * `GET /api/v1/journal` on a rolling `since` watermark AND keep trying to
 * re-open the stream on a jittered exponential backoff — the fallback is a
 * bridge, not a destination.
 *
 * The watermark advances from stream-delivered entries as well as polled
 * ones. It used not to, which meant a stream that dropped after a busy hour
 * re-requested from the moment the page loaded and got back one capped page:
 * everything in between was dropped with nothing saying so.
 */
export function useJournalStream(opts: UseJournalStreamOptions): UseJournalStreamResult {
  const { workspaceId, params, enabled = true, onEntry } = opts
  const [status, setStatus] = useState<JournalStreamStatus>("idle")
  const [lastError, setLastError] = useState<string | null>(null)
  const [gapDetected, setGapDetected] = useState(false)
  const onEntryRef = useRef(onEntry)
  // Set by the live effect so the caller-facing reconnect() reaches the
  // current subscription's closure without re-running the effect.
  const reconnectRef = useRef<(() => void) | null>(null)

  // Keep the latest handler without re-subscribing — onEntry is usually
  // reconstructed on every render, which would otherwise churn EventSource.
  useEffect(() => {
    onEntryRef.current = onEntry
  }, [onEntry])

  // Serialise filter params so the effect only re-runs when their *content*
  // changes, not every render that rebuilds the object literal.
  const paramsKey = params
    ? Object.entries(params)
        .filter(([, v]) => v !== undefined && v !== "")
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([k, v]) => `${k}=${v}`)
        .join("&")
    : ""

  useEffect(() => {
    if (!enabled || !workspaceId) {
      setStatus("idle")
      return
    }

    let cancelled = false
    let es: EventSource | null = null
    let pollTimer: ReturnType<typeof setInterval> | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null
    let pollInFlight = false
    // Mirrors the gapDetected state inside this effect's closure, which
    // cannot read it back. A standing gap warning must survive a
    // reconnect: re-opening the stream does not fill the hole, only
    // re-reading the head does.
    let gapped = false
    let attempt = 0
    // The watermark is tracked as both an instant and the exact string to
    // send back as `since`. It cannot be compared as a string: the API
    // serialises ts with RFC3339Nano, which strips trailing zeros, so an
    // entry landing on a whole second carries no fractional part and
    // "…01.500Z" sorts BELOW "…01Z" ('.' < 'Z'). A string compare therefore
    // refuses to advance past any whole-second entry, and the next poll
    // re-requests rows already delivered. (internal/journal/queries.go's
    // formatSinceBound documents the same trap on the server side.)
    let watermarkMs = Date.now()
    let pollWatermark = new Date(watermarkMs).toISOString()

    gapped = false
    setGapDetected(false)

    const query = new URLSearchParams()
    query.set("workspace_id", workspaceId)
    if (paramsKey) {
      for (const kv of paramsKey.split("&")) {
        const idx = kv.indexOf("=")
        if (idx === -1) continue
        query.set(kv.slice(0, idx), kv.slice(idx + 1))
      }
    }

    /** Emit an entry and let it move the watermark, whichever door it came
     *  through. This is what makes a later poll resume from where the live
     *  stream actually stopped. */
    function emit(entry: JournalEntry) {
      onEntryRef.current(entry)
      const ms = Date.parse(entry.ts)
      // An unparseable ts must not move the watermark — better to re-fetch
      // and dedupe than to skip forward past entries that were never seen.
      if (Number.isNaN(ms) || ms <= watermarkMs) return
      watermarkMs = ms
      pollWatermark = entry.ts
    }

    function handleEntryData(raw: unknown) {
      const parsed = journalEntrySchema.safeParse(raw)
      if (parsed.success) emit(parsed.data)
    }

    /**
     * One poll cycle. Walks up to POLL_MAX_PAGES of the keyset cursor so a
     * backlog deeper than one page is actually delivered instead of being
     * skipped by the watermark jumping to the newest row. Pages come back
     * newest-first and each subsequent page is older, so everything is
     * collected first and replayed oldest-first at the end.
     */
    async function pollOnce() {
      // A cursor walk can outlast the 5 s tick. Two concurrent cycles would
      // both read the same watermark, re-fetch the same rows and race on
      // advancing it — so a slow one simply skips the next tick.
      if (pollInFlight) return
      pollInFlight = true
      try {
        await pollPages()
      } finally {
        pollInFlight = false
      }
    }

    async function pollPages() {
      const collected: JournalEntry[] = []
      let cursor: string | undefined
      let truncated = false
      for (let page = 0; page < POLL_MAX_PAGES; page++) {
        if (cancelled) return
        const pollParams = new URLSearchParams(query)
        pollParams.set("since", pollWatermark)
        pollParams.set("limit", String(POLL_LIMIT))
        if (cursor) pollParams.set("cursor", cursor)
        const res = await apiFetch(`/api/v1/journal?${pollParams.toString()}`)
        if (!res.ok) return
        const json = await res.json()
        const entries = Array.isArray(json?.entries) ? json.entries : []
        for (const raw of entries) {
          const parsed = journalEntrySchema.safeParse(raw)
          if (parsed.success) collected.push(parsed.data)
        }
        // A short page means we reached the watermark; nothing is missing.
        if (entries.length < POLL_LIMIT) break
        cursor = typeof json?.next_cursor === "string" && json.next_cursor ? json.next_cursor : undefined
        if (!cursor) break
        // Still full and still more to come when the walk runs out of pages:
        // the rows below this point are about to be skipped.
        if (page === POLL_MAX_PAGES - 1) truncated = true
      }
      if (cancelled) return
      for (let i = collected.length - 1; i >= 0; i--) emit(collected[i])
      if (truncated) {
        gapped = true
        setGapDetected(true)
        setLastError(GAP_MESSAGE)
      }
    }

    function startPolling() {
      if (cancelled) return
      // The status is set even when the timer is already running: a manual
      // reconnect() flips the badge to "Connecting", and if that attempt
      // also fails the badge has to fall back to "Polling" rather than
      // sitting on "Connecting" for as long as the outage lasts.
      setStatus("polling")
      if (pollTimer) return
      pollTimer = setInterval(() => {
        if (cancelled) return
        // Poll failures are tolerated silently; the next tick retries.
        void pollOnce().catch(() => {})
      }, POLL_INTERVAL_MS)
    }

    function stopPolling() {
      if (pollTimer) {
        clearInterval(pollTimer)
        pollTimer = null
      }
    }

    function scheduleReconnect() {
      if (cancelled || reconnectTimer) return
      const delay = reconnectDelay(attempt)
      attempt++
      reconnectTimer = setTimeout(() => {
        reconnectTimer = null
        if (cancelled) return
        connectStream(true)
      }, delay)
    }

    /**
     * @param isRetry keeps the badge on "Polling" while a background retry is
     * in flight. Flipping to "Connecting" on every attempt would strobe the
     * status for the whole outage while telling the user nothing new.
     */
    function connectStream(isRetry = false) {
      if (!isRetry) setStatus("connecting")
      try {
        es = new EventSource(`/api/v1/journal/stream?${query.toString()}`)
      } catch {
        if (!gapped) setLastError("Failed to open stream")
        startPolling()
        scheduleReconnect()
        return
      }

      es.onopen = () => {
        if (cancelled) return
        attempt = 0
        stopPolling()
        setStatus("connected")
        if (!gapped) setLastError(null)
      }

      es.addEventListener("entry", (event) => {
        try {
          const data = JSON.parse((event as MessageEvent).data)
          handleEntryData(data)
        } catch {
          // Malformed frame — ignore.
        }
      })

      // Some servers send messages without an explicit event: type. Treat
      // those as entries too so the client stays permissive.
      es.onmessage = (event) => {
        if (!event.data) return
        try {
          const data = JSON.parse(event.data)
          handleEntryData(data)
        } catch {
          // ignore
        }
      }

      es.onerror = () => {
        if (cancelled) return
        if (!gapped) setLastError("SSE connection lost")
        es?.close()
        es = null
        // Poll so the UI doesn't appear frozen, and keep reaching for the
        // stream underneath it.
        startPolling()
        scheduleReconnect()
      }
    }

    reconnectRef.current = () => {
      if (cancelled) return
      if (reconnectTimer) {
        clearTimeout(reconnectTimer)
        reconnectTimer = null
      }
      attempt = 0
      gapped = false
      setGapDetected(false)
      setLastError(null)
      es?.close()
      es = null
      connectStream()
    }

    connectStream()

    return () => {
      cancelled = true
      reconnectRef.current = null
      es?.close()
      es = null
      stopPolling()
      if (reconnectTimer) {
        clearTimeout(reconnectTimer)
        reconnectTimer = null
      }
    }
  }, [enabled, workspaceId, paramsKey])

  const reconnect = useCallback(() => {
    reconnectRef.current?.()
  }, [])

  return { status, lastError, gapDetected, reconnect }
}
