"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { toast } from "sonner"

import { apiFetch } from "@/lib/api-fetch"
import { readApiError } from "@/lib/api-error"
import { useAbilities } from "@/hooks/use-abilities"
import { isManagerTier } from "@/lib/permissions/tiers"

import { connectionBetween, stateOf, type Connection, type Crew, type FileAccess, type PairState } from "./crew-links-model"

const PAIR_WORDS: Record<PairState, string> = { none: "not linked", out: "sends work", in: "receives work", both: "both ways" }

/**
 * Crew links data and every write, for all three views.
 *
 * The writes are the ones the old settings card made, unchanged: a pair is
 * replaced (delete + create) when a one-way link has to turn, a failed create
 * restores what was deleted and says so if even that fails, removing the last
 * direction asks first, and every write carries the connection's
 * access_version so a stale screen cannot overwrite another admin's change.
 */
export function useCrewLinks(workspaceId: string | null) {
  // POST and DELETE /crew-connections and PUT …/file-access are MANAGER+.
  const { role } = useAbilities()
  const canManage = isManagerTier(role)

  const [crews, setCrews] = useState<Crew[]>([])
  const [connections, setConnections] = useState<Connection[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState(false)
  const [pending, setPending] = useState(false)
  // Writes chain (link, then its file level); each step needs the rows the
  // previous one produced, not the ones this render closed over.
  const latest = useRef<Connection[]>([])

  const reload = useCallback(async () => {
    if (!workspaceId) return
    try {
      const [connsRes, crewsRes] = await Promise.all([
        apiFetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`),
        apiFetch(`/api/v1/crews?workspace_id=${workspaceId}&limit=500`),
      ])
      if (!connsRes.ok || !crewsRes.ok) throw new Error("Access could not be loaded")
      const [nextConnections, nextCrews] = (await Promise.all([connsRes.json(), crewsRes.json()])) as [Connection[], Crew[]]
      latest.current = nextConnections
      setConnections(nextConnections)
      setCrews(nextCrews)
      setLoadError(false)
    } catch {
      setLoadError(true)
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => { void reload() }, [reload])

  /** Set the pair (self, other) to `next`, seen from `self`. */
  const setPair = useCallback(async (self: Crew, other: Crew, next: PairState): Promise<boolean> => {
    if (!workspaceId || !canManage) return false
    const existing = connectionBetween(latest.current, self.id, other.id)
    const current = stateOf(existing, self.id)
    if (current === next) return true

    setPending(true)
    let removed: Connection | null = null
    // A failed rollback surfaces: the user reads "failed" as "nothing
    // happened", which is wrong when the delete did happen (#1594).
    const restoreRemoved = async (): Promise<boolean> => {
      if (!removed) return true
      try {
        const res = await apiFetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            from_crew_id: removed.from_crew_id,
            to_crew_id: removed.to_crew_id,
            direction: removed.direction,
            forward_file_access: removed.forward_file_access,
            reverse_file_access: removed.reverse_file_access,
          }),
        })
        return res.ok
      } catch {
        return false
      }
    }
    const reportFailure = async (reason: string) => {
      const restored = await restoreRemoved()
      toast.error(restored ? reason : `${reason} — and the previous link could not be restored. ${self.name} and ${other.name} are now unlinked; re-create the link manually.`)
    }
    const version = (c: Connection) => (c.access_version ? `&expected_version=${c.access_version}` : "")

    try {
      // Removing the link severs every dispatch, message and shared-file path
      // between two crews — it keeps its question.
      if (next === "none") {
        if (!existing) return true
        if (!window.confirm(`Unlink ${self.name} and ${other.name}? Agents will no longer be able to hand work between them.`)) return false
        const res = await apiFetch(`/api/v1/crew-connections/${existing.id}?workspace_id=${workspaceId}${version(existing)}`, { method: "DELETE" })
        if (!res.ok) { toast.error(await readApiError(res, "Failed to unlink")); await reload(); return false }
        toast.success(`${self.name} and ${other.name} unlinked`)
        await reload()
        return true
      }

      const from = next === "in" ? other.id : self.id
      const to = next === "in" ? self.id : other.id
      const direction = next === "both" ? "bidirectional" : "unidirectional"
      // A one-way link has to point the right way; POST would widen, so the
      // pair is replaced.
      const needsReplace = existing !== undefined && direction === "unidirectional" && (existing.direction === "bidirectional" || existing.from_crew_id !== from)
      if (needsReplace && existing) {
        const del = await apiFetch(`/api/v1/crew-connections/${existing.id}?workspace_id=${workspaceId}${version(existing)}`, { method: "DELETE" })
        if (!del.ok) { toast.error(await readApiError(del, "Failed to change the link")); await reload(); return false }
        removed = existing
      }
      const res = await apiFetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          from_crew_id: from, to_crew_id: to, direction,
          ...(removed ? {
            forward_file_access: from === removed.from_crew_id ? removed.forward_file_access : removed.reverse_file_access,
            reverse_file_access: from === removed.from_crew_id ? removed.reverse_file_access : removed.forward_file_access,
          } : !existing ? { forward_file_access: "none", reverse_file_access: "none" } : {}),
        }),
      })
      if (!res.ok) {
        await reportFailure(await readApiError(res, "Failed to change the link"))
        await reload()
        return false
      }
      toast.success(`${self.name} → ${other.name}: ${PAIR_WORDS[next]}`)
      await reload()
      return true
    } catch {
      await reportFailure("Failed to change the link")
      await reload()
      return false
    } finally {
      setPending(false)
    }
  }, [workspaceId, canManage, reload])

  /** What `requester` may do with `target`'s shared files. */
  const setFileAccess = useCallback(async (requester: Crew, target: Crew, level: FileAccess): Promise<boolean> => {
    if (!workspaceId || !canManage) return false
    const connection = connectionBetween(latest.current, requester.id, target.id)
    if (!connection?.access_version) {
      toast.error("This server does not report shared-file access, so it cannot be changed here.")
      return false
    }
    setPending(true)
    try {
      const res = await apiFetch(`/api/v1/crew-connections/${connection.id}/file-access?workspace_id=${workspaceId}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ requester_crew_id: requester.id, level, expected_version: connection.access_version }),
      })
      if (!res.ok) toast.error(await readApiError(res, "Could not update shared-file access"))
      else toast.success("Shared-file access updated")
      await reload()
      return res.ok
    } catch {
      toast.error("Could not verify shared-file access. Reload before making another change.")
      return false
    } finally {
      setPending(false)
    }
  }, [workspaceId, canManage, reload])

  return { crews, connections, loading, loadError, pending, canManage, reload, setPair, setFileAccess }
}
