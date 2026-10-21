"use client"

import { useCallback, useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { toast } from "@/lib/toast"

import { apiFetch } from "@/lib/api-fetch"
import { humanizePatchError, memberCapabilitiesKey } from "@/components/admin/member-capabilities"

import { ROLE_LABEL, type Member } from "./members-model"

/**
 * The writes Members makes for one or many people at once: commit the drafted
 * capability sets (`{"set"}`, behind the page's Save) and change the role.
 * Each person is one PATCH — the API has no batch endpoint — and a role change
 * ends in one toast that counts what went through, never one per person.
 *
 * Capabilities are addressed by USER id, the role by MEMBERSHIP id; that is
 * the API's split, kept here so the page never has to know it.
 */
export function useAccessWrites(workspaceId: string, onRoleChanged: () => void) {
  const queryClient = useQueryClient()
  const [busy, setBusy] = useState(false)
  const ws = encodeURIComponent(workspaceId)

  const run = useCallback(async (people: Member[], send: (m: Member) => Promise<Response>, done: string): Promise<number> => {
    if (people.length === 0) return 0
    setBusy(true)
    try {
      const results = await Promise.all(people.map(async (m) => {
        try {
          const res = await send(m)
          return { ok: res.ok, status: res.status }
        } catch {
          return { ok: false, status: 0 }
        }
      }))
      const ok = results.filter((r) => r.ok).length
      const failed = results.filter((r) => !r.ok)
      if (failed.length === 0) toast.success(done)
      else if (ok === 0) toast.error(people.length === 1 ? humanizePatchError(failed[0].status) : `Nothing changed: ${humanizePatchError(failed[0].status)}`)
      else toast.warning(`${ok} of ${people.length} changed; ${failed.length} refused (${humanizePatchError(failed[0].status)})`)
      return ok
    } finally {
      setBusy(false)
    }
  }, [])

  const who = (people: Member[]) => (people.length === 1 ? (people[0].user.full_name?.trim() || people[0].user.email) : `${people.length} people`)

  return {
    busy,
    /** Commit drafted capability sets, one `{"set"}` PATCH per person. Resolves
     *  to the user ids the server took; the caller keeps the rest drafted.
     *  Quiet on success — the page's save bar says "Saved". */
    setCapabilities: async (entries: readonly (readonly [Member, string[]])[]): Promise<string[]> => {
      if (entries.length === 0) return []
      setBusy(true)
      try {
        const results = await Promise.all(entries.map(async ([m, set]) => {
          try {
            const res = await apiFetch(`/api/v1/workspaces/${ws}/members/${encodeURIComponent(m.user.id)}/capabilities?workspace_id=${ws}`, {
              method: "PATCH",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ set }),
            })
            return res.ok ? m.user.id : null
          } catch {
            return null
          }
        }))
        const done = results.filter((id): id is string => id !== null)
        if (done.length > 0) await queryClient.invalidateQueries({ queryKey: memberCapabilitiesKey(workspaceId) })
        return done
      } finally {
        setBusy(false)
      }
    },
    changeRole: async (people: Member[], role: string) => {
      const ok = await run(people, (m) => apiFetch(`/api/v1/workspaces/${ws}/members/${encodeURIComponent(m.id)}?workspace_id=${ws}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ role }),
      }), `${who(people)}: ${ROLE_LABEL[role] ?? role}`)
      if (ok > 0) onRoleChanged()
      return ok
    },
  }
}
