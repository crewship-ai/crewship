"use client"

import { useEffect, useMemo, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import { personLabel } from "@/components/ui/user-avatar"

import type { AuditPerson } from "./audit-toolbar"

/** The workspace's people, for the Person filter. Empty until it loads; a
 *  failure leaves the filter with "Everyone" only. */
export function useWorkspacePeople(workspaceId: string): AuditPerson[] {
  const [people, setPeople] = useState<AuditPerson[]>([])
  useEffect(() => {
    let cancelled = false
    apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members`)
      .then(async (res) => (res.ok ? res.json() : []))
      .then((rows: unknown) => {
        if (cancelled || !Array.isArray(rows)) return
        const list = rows
          .map((r) => (r && typeof r === "object" ? (r as { user?: { id?: string; email?: string; full_name?: string | null } }).user : null))
          .filter((u): u is { id: string; email: string; full_name?: string | null } => Boolean(u?.id))
          .map((u) => ({ id: u.id, label: personLabel(u.full_name, u.email ?? "") || u.email }))
          .sort((a, b) => a.label.localeCompare(b.label))
        setPeople(list)
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [workspaceId])
  return useMemo(() => people, [people])
}
