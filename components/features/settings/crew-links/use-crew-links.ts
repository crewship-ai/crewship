"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { toast } from "@/lib/toast"

import { apiFetch } from "@/lib/api-fetch"
import { readApiError } from "@/lib/api-error"
import { useAbilities } from "@/hooks/use-abilities"
import { isManagerTier } from "@/lib/permissions/tiers"

import { LEVEL_LABEL, connectionBetween, pairLevels, planLevels, stateOf, type Connection, type Crew, type CrewAgent, type FileAccess, type Level, type PairState } from "./crew-links-model"

/** `quiet`: the caller says one thing for the whole change instead of one toast per write. */
interface WriteOptions { quiet?: boolean }

export type ApplyResult = "done" | "asked" | "failed"

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
  // Agents, grouped by crew — who is in each crew. Null until (or unless)
  // they load: a failed fetch only costs the profile its faces.
  const [agentsByCrew, setAgentsByCrew] = useState<Map<string, CrewAgent[]> | null>(null)
  // Writes chain (link, then its file level); each step needs the rows the
  // previous one produced, not the ones this render closed over.
  const latest = useRef<Connection[]>([])
  // The unlink the page is asking about (ConfirmDialog), or null.
  const [pendingUnlink, setPendingUnlinkState] = useState<{ self: Crew; other: Crew } | null>(null)
  const pendingUnlinkRef = useRef<{ self: Crew; other: Crew } | null>(null)
  const setPendingUnlink = useCallback((v: { self: Crew; other: Crew } | null) => {
    pendingUnlinkRef.current = v
    setPendingUnlinkState(v)
  }, [])

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

  useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    apiFetch(`/api/v1/agents?workspace_id=${workspaceId}&limit=500`)
      .then(async (res) => (res.ok ? res.json() : null))
      .then((body: unknown) => {
        if (cancelled || body == null) return
        const list = (Array.isArray(body) ? body : (body as { agents?: unknown }).agents) as CrewAgent[] | undefined
        if (!Array.isArray(list)) return
        const grouped = new Map<string, CrewAgent[]>()
        for (const a of list) {
          if (!a?.crew_id) continue
          const bucket = grouped.get(a.crew_id)
          if (bucket) bucket.push(a); else grouped.set(a.crew_id, [a])
        }
        setAgentsByCrew(grouped)
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [workspaceId])

  /** Set the pair (self, other) to `next`, seen from `self`. */
  const setPair = useCallback(async (self: Crew, other: Crew, next: PairState, opts: WriteOptions = {}): Promise<boolean> => {
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
      // between two crews — it keeps its question, asked by the page's
      // ConfirmDialog (pendingUnlink → confirmUnlink), never window.confirm.
      if (next === "none") {
        if (existing) setPendingUnlink({ self, other })
        return !existing
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
      if (!opts.quiet) toast.success(`${self.name} → ${other.name}: ${PAIR_WORDS[next]}`)
      await reload()
      return true
    } catch {
      await reportFailure("Failed to change the link")
      await reload()
      return false
    } finally {
      setPending(false)
    }
  }, [workspaceId, canManage, reload, setPendingUnlink])

  /** What `requester` may do with `target`'s shared files. */
  const setFileAccess = useCallback(async (requester: Crew, target: Crew, level: FileAccess, opts: WriteOptions = {}): Promise<boolean> => {
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
      else if (!opts.quiet) toast.success("Shared-file access updated")
      await reload()
      return res.ok
    } catch {
      toast.error("Could not verify shared-file access. Reload before making another change.")
      return false
    } finally {
      setPending(false)
    }
  }, [workspaceId, canManage, reload])

  /**
   * Move a pair to two ladders: `out` is what `self` may do with `other`,
   * `in` the other way round. One write path for the crew page (one ladder
   * moves, saved at once) and the link dialog (both, on Save): the pair is
   * created, widened, turned or narrowed first, then each changed direction's
   * file level is set against the version the server now holds. Removing the
   * last direction only asks ("asked"); ConfirmDialog does the rest.
   */
  const applyLevels = useCallback(async (self: Crew, other: Crew, next: { out: Level; in: Level }): Promise<ApplyResult> => {
    if (!workspaceId || !canManage) return "failed"
    const before = pairLevels(connectionBetween(latest.current, self.id, other.id), self.id)
    const plan = planLevels(before, next)
    if (!plan.pair && plan.files.length === 0) return "done"
    if (plan.unlink) {
      await setPair(self, other, "none")
      return "asked"
    }
    if (plan.pair && !(await setPair(self, other, plan.pair, { quiet: true }))) return "failed"
    for (const { side, level } of plan.files) {
      const [requester, target] = side === "out" ? [self, other] : [other, self]
      const conn = connectionBetween(latest.current, self.id, other.id)
      // Old servers send no file levels: the ladder was Off / Work only and there is nothing to set.
      if (!conn || conn.forward_file_access === undefined) continue
      const held = (conn.from_crew_id === requester.id ? conn.forward_file_access : conn.reverse_file_access) ?? "none"
      if (held === level) continue
      if (!(await setFileAccess(requester, target, level, { quiet: true }))) return "failed"
    }
    const said = (["out", "in"] as const)
      .filter((side) => next[side] !== before[side])
      .map((side) => `${side === "out" ? self.name : other.name} → ${side === "out" ? other.name : self.name}: ${LEVEL_LABEL[next[side]]}`)
    toast.success(said.join(" · "))
    return "done"
  }, [workspaceId, canManage, setPair, setFileAccess])

  /**
   * Remove the link the page is asking about. Throws on a refusal, so the
   * ConfirmDialog stays open where the person can retry or cancel; the
   * reason is the error toast.
   */
  const confirmUnlink = useCallback(async (): Promise<void> => {
    const ask = pendingUnlinkRef.current
    if (!workspaceId || !canManage || !ask) return
    const existing = connectionBetween(latest.current, ask.self.id, ask.other.id)
    if (!existing) { setPendingUnlink(null); return }
    const version = existing.access_version ? `&expected_version=${existing.access_version}` : ""
    setPending(true)
    try {
      let res: Response
      try {
        res = await apiFetch(`/api/v1/crew-connections/${existing.id}?workspace_id=${workspaceId}${version}`, { method: "DELETE" })
      } catch (e) {
        toast.error("Failed to unlink")
        throw e
      }
      if (!res.ok) {
        const reason = await readApiError(res, "Failed to unlink")
        toast.error(reason)
        await reload()
        throw new Error(reason)
      }
      toast.success(`${ask.self.name} and ${ask.other.name} unlinked`)
      setPendingUnlink(null)
      await reload()
    } finally {
      setPending(false)
    }
  }, [workspaceId, canManage, reload, setPendingUnlink])

  const cancelUnlink = useCallback(() => setPendingUnlink(null), [setPendingUnlink])

  return {
    crews, connections, agentsByCrew, loading, loadError, pending, canManage, reload, setPair, setFileAccess, applyLevels,
    pendingUnlink, confirmUnlink, cancelUnlink,
  }
}
