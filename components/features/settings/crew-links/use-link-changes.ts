"use client"

import { useEffect, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"

import type { LinkAuditEntry } from "./crew-links-model"

/** The audit list is read newest first, a few pages deep: this is a card, not the log. */
const PAGE = 100
const PAGES = 3

function parse(raw: unknown): Record<string, unknown> | null {
  let v = raw
  if (typeof v === "string") {
    try { v = JSON.parse(v) } catch { return null }
  }
  return v && typeof v === "object" ? (v as Record<string, unknown>) : null
}

/**
 * Every crew-link change in the audit trail, newest first
 * (GET /api/v1/audit?entity_type=CREW_LINK). The server cannot filter by crew,
 * so the page keeps the ones that name the open crew. ADMIN and up only: the
 * endpoint refuses anyone else, so `enabled` stays false below that and
 * nothing is requested.
 */
export function useLinkChanges(workspaceId: string | null, enabled: boolean, version: unknown) {
  const [entries, setEntries] = useState<LinkAuditEntry[] | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (!workspaceId || !enabled) return
    const controller = new AbortController()
    ;(async () => {
      const out: LinkAuditEntry[] = []
      try {
        for (let page = 1; page <= PAGES; page++) {
          const params = new URLSearchParams({ workspace_id: workspaceId, entity_type: "CREW_LINK", page: String(page), limit: String(PAGE) })
          const res = await apiFetch(`/api/v1/audit?${params}`, { signal: controller.signal })
          if (!res.ok) throw new Error(String(res.status))
          const body = await res.json()
          const rows: Record<string, unknown>[] = Array.isArray(body?.data) ? body.data : []
          for (const r of rows) {
            const meta = parse(r.metadata)
            out.push({
              id: String(r.id ?? `${page}-${out.length}`),
              action: String(r.action ?? ""),
              at: String(r.created_at ?? ""),
              actor: (r.user_name as string | null) || (r.user_email as string | null) || null,
              previous: parse(meta?.previous),
              current: parse(meta?.current),
            })
          }
          if (page >= Number(body?.pagination?.total_pages ?? 1)) break
        }
        if (!controller.signal.aborted) { setEntries(out); setFailed(false) }
      } catch {
        if (!controller.signal.aborted) setFailed(true)
      }
    })()
    return () => controller.abort()
    // `version` re-reads after a write: the change just made belongs in the card.
  }, [workspaceId, enabled, version])

  return { entries, failed }
}
