"use client"

import * as React from "react"
import { toast } from "sonner"

import { apiFetch } from "@/lib/api-fetch"
import { readApiError } from "@/lib/api-error"
import type { AdminScope } from "@/app/(dashboard)/admin/types"
import type { Person, Role, Workspace } from "./people-model"

/**
 * Admin › People & workspaces: the two admin lists and every instance action,
 * in one place. The lists come from GET /admin/users and /admin/workspaces
 * (instance-wide for an instance admin); every write goes to
 * /admin/instance/… and reloads both, so the page never shows a membership
 * the server did not accept.
 */

export interface SetupLink { url: string; expires_at: string; email: string }

const INSTANCE = "/api/v1/admin/instance"
const enc = encodeURIComponent

async function send(path: string, method: string, body?: unknown): Promise<Response> {
  return apiFetch(path, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

/**
 * workspaceLoading: while the current workspace is still being resolved the
 * lists wait for it; once there is none (an instance admin need not belong to
 * any workspace) they are read without one, which the server answers for the
 * whole instance.
 */
export function usePeople(workspaceId: string | null, workspaceLoading = false) {
  const [people, setPeople] = React.useState<Person[]>([])
  const [workspaces, setWorkspaces] = React.useState<Workspace[]>([])
  const [scope, setScope] = React.useState<AdminScope | undefined>()
  const [loading, setLoading] = React.useState(true)
  const [error, setError] = React.useState<string | null>(null)
  const [busy, setBusy] = React.useState<string | null>(null)

  const reload = React.useCallback(async () => {
    if (!workspaceId && workspaceLoading) return
    try {
      const q = workspaceId ? `?workspace_id=${enc(workspaceId)}` : ""
      const [u, w] = await Promise.all([apiFetch(`/api/v1/admin/users${q}`), apiFetch(`/api/v1/admin/workspaces${q}`)])
      if (!u.ok || !w.ok) {
        setError(await readApiError(u.ok ? w : u, "People and workspaces could not be read"))
        return
      }
      const users = (await u.json()) as Person[]
      setPeople(users.map((p) => ({ ...p, memberships: p.memberships ?? [] })))
      setWorkspaces((await w.json()) as Workspace[])
      const s = u.headers?.get?.("X-Admin-Scope")
      setScope(s === "instance" || s === "workspace" ? s : undefined)
      setError(null)
    } catch {
      setError("People and workspaces could not be read")
    } finally {
      setLoading(false)
    }
  }, [workspaceId, workspaceLoading])

  React.useEffect(() => { void reload() }, [reload])

  /** Run one write: say what happened, reload, and hand back the body. */
  const act = React.useCallback(async <T,>(key: string, path: string, method: string, body: unknown, done: string, fallback: string): Promise<T | null> => {
    setBusy(key)
    try {
      const res = await send(path, method, body)
      if (!res.ok) {
        toast.error(await readApiError(res, fallback))
        return null
      }
      const out = res.status === 204 ? ({} as T) : ((await res.json().catch(() => ({}))) as T)
      if (done) toast.success(done)
      await reload()
      return out
    } catch {
      toast.error(fallback)
      return null
    } finally {
      setBusy(null)
    }
  }, [reload])

  const actions = React.useMemo(() => ({
    setRole: (userId: string, wsId: string, role: Role, done: string) =>
      act(`m:${userId}:${wsId}`, `${INSTANCE}/workspaces/${enc(wsId)}/members/${enc(userId)}`, "PUT", { role }, done, "That access change was refused"),
    removeAccess: (userId: string, wsId: string, done: string) =>
      act(`m:${userId}:${wsId}`, `${INSTANCE}/workspaces/${enc(wsId)}/members/${enc(userId)}`, "DELETE", undefined, done, "That person could not be removed"),
    createPerson: async (email: string, fullName: string, memberships: { workspace_id: string; role: Role }[]) => {
      const out = await act<{ user_id: string; setup_url: string; expires_at: string; email: string }>("create-person", `${INSTANCE}/people`, "POST",
        { email, full_name: fullName, memberships }, "", "The account could not be created")
      return out ? { userId: out.user_id, link: { url: out.setup_url, expires_at: out.expires_at, email: out.email } as SetupLink } : null
    },
    createWorkspace: async (name: string, slug: string, ownerUserId: string) => {
      const out = await act<{ id: string }>("create-ws", `${INSTANCE}/workspaces`, "POST",
        { name, slug, owner_user_id: ownerUserId }, `${name} created`, "The workspace could not be created")
      return out?.id ?? null
    },
    transfer: (wsId: string, userId: string, done: string) =>
      act(`t:${wsId}`, `${INSTANCE}/workspaces/${enc(wsId)}/transfer-ownership`, "POST", { user_id: userId }, done, "Ownership could not be transferred"),
    deleteWorkspace: (wsId: string, slug: string, done: string) =>
      act(`d:${wsId}`, `${INSTANCE}/workspaces/${enc(wsId)}`, "DELETE", { confirm_slug: slug }, done, "The workspace could not be deleted"),
    suspend: (userId: string, reason: string, done: string) =>
      act(`s:${userId}`, `${INSTANCE}/people/${enc(userId)}/suspend`, "POST", { reason }, done, "The account could not be suspended"),
    reactivate: (userId: string, done: string) =>
      act(`s:${userId}`, `${INSTANCE}/people/${enc(userId)}/reactivate`, "POST", {}, done, "The account could not be reactivated"),
    issueLink: async (userId: string, email: string) => {
      const out = await act<{ setup_url: string; expires_at: string }>(`l:${userId}`, `${INSTANCE}/people/${enc(userId)}/setup-link`, "POST", {}, "", "No setup link could be issued")
      return out ? ({ url: out.setup_url, expires_at: out.expires_at, email } as SetupLink) : null
    },
    revokeLink: (userId: string, done: string) =>
      act(`l:${userId}`, `${INSTANCE}/people/${enc(userId)}/setup-link`, "DELETE", undefined, done, "The setup link could not be voided"),
    setInstanceAdmin: (userId: string, on: boolean, done: string) =>
      act(`a:${userId}`, `${INSTANCE}/admins/${enc(userId)}`, on ? "PUT" : "DELETE", on ? {} : undefined, done, "That change was refused"),
    unlock: (userId: string, done: string) =>
      // Without a workspace the route answers an instance admin for the
      // whole instance, like the lists above.
      act(`u:${userId}`, `/api/v1/admin/users/${enc(userId)}/unlock${workspaceId ? `?workspace_id=${enc(workspaceId)}` : ""}`, "POST", {}, done, "The account could not be unlocked"),
  }), [act, workspaceId])

  return { people, workspaces, scope, loading, error, busy, reload, actions }
}
