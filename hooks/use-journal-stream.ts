"use client"

import { useEffect, useRef, useState } from "react"
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
}

/**
 * Subscribe to `GET /api/v1/journal/stream` as an EventSource. If the browser
 * can't open the stream (404 / CORS / offline), fall back to polling
 * `GET /api/v1/journal` every 5 s with a rolling `since` watermark so the UI
 * keeps animating even while the backend handler is still being written.
 */
export function useJournalStream(opts: UseJournalStreamOptions): UseJournalStreamResult {
  const { workspaceId, params, enabled = true, onEntry } = opts
  const [status, setStatus] = useState<JournalStreamStatus>("idle")
  const [lastError, setLastError] = useState<string | null>(null)
  const onEntryRef = useRef(onEntry)

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
    let pollWatermark = new Date().toISOString()

    const query = new URLSearchParams()
    query.set("workspace_id", workspaceId)
    if (paramsKey) {
      for (const kv of paramsKey.split("&")) {
        const idx = kv.indexOf("=")
        if (idx === -1) continue
        query.set(kv.slice(0, idx), kv.slice(idx + 1))
      }
    }

    function handleEntryData(raw: unknown) {
      const parsed = journalEntrySchema.safeParse(raw)
      if (parsed.success) {
        onEntryRef.current(parsed.data)
      }
    }

    async function startPolling() {
      if (cancelled) return
      setStatus("polling")
      pollTimer = setInterval(async () => {
        if (cancelled) return
        try {
          const pollParams = new URLSearchParams(query)
          pollParams.set("since", pollWatermark)
          pollParams.set("limit", "50")
          const res = await apiFetch(`/api/v1/journal?${pollParams.toString()}`)
          if (!res.ok) return
          const json = await res.json()
          const entries = Array.isArray(json?.entries) ? json.entries : []
          // Oldest first so the UI appends in chronological order.
          for (let i = entries.length - 1; i >= 0; i--) {
            const parsed = journalEntrySchema.safeParse(entries[i])
            if (parsed.success) {
              onEntryRef.current(parsed.data)
              if (parsed.data.ts > pollWatermark) pollWatermark = parsed.data.ts
            }
          }
        } catch {
          // Silently tolerate poll failures; the next tick retries.
        }
      }, 5000)
    }

    function connectStream() {
      setStatus("connecting")
      try {
        es = new EventSource(`/api/v1/journal/stream?${query.toString()}`)
      } catch {
        setLastError("Failed to open stream")
        startPolling()
        return
      }

      es.onopen = () => {
        if (cancelled) return
        setStatus("connected")
        setLastError(null)
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
        setStatus("error")
        setLastError("SSE connection lost")
        es?.close()
        es = null
        // Fall back to polling so the UI doesn't appear frozen.
        startPolling()
      }
    }

    connectStream()

    return () => {
      cancelled = true
      es?.close()
      es = null
      if (pollTimer) {
        clearInterval(pollTimer)
        pollTimer = null
      }
    }
  }, [enabled, workspaceId, paramsKey])

  return { status, lastError }
}
