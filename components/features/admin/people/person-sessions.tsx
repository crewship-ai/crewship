"use client"

import React from "react"
import { toast } from "sonner"
import { LogOut, Monitor, Smartphone, Terminal } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { readApiError } from "@/lib/api-error"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import type { AdminUser, AdminUserSessions } from "@/app/(dashboard)/admin/types"
import { DrawerSection, ago } from "@/app/(dashboard)/admin/tabs/admin-kit"

// No workspace (an instance admin need not belong to one): the route answers
// for the whole instance, so the query is left off rather than sent empty.
const adminUserURL = (userId: string, workspaceId: string, tail: string) =>
  `/api/v1/admin/users/${encodeURIComponent(userId)}/${tail}${workspaceId ? `?workspace_id=${encodeURIComponent(workspaceId)}` : ""}`
export function UserSessions({ user, workspaceId, isMe, onChanged }: { user: AdminUser; workspaceId: string; isMe: boolean; onChanged: () => void }) {
  const [data, setData] = React.useState<AdminUserSessions | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const [busy, setBusy] = React.useState<string | null>(null)
  const load = React.useCallback(async () => {
    try {
      const res = await apiFetch(adminUserURL(user.id, workspaceId, "sessions"))
      if (!res.ok) { setError(await readApiError(res, "Sessions could not be read")); return }
      setData(await res.json())
      setError(null)
    } catch {
      setError("Sessions could not be read")
    }
  }, [user.id, workspaceId])
  React.useEffect(() => { void load() }, [load])

  const post = async (tail: string, key: string, done: string) => {
    setBusy(key)
    try {
      const res = await apiFetch(adminUserURL(user.id, workspaceId, tail), { method: "POST" })
      if (!res.ok) { toast.error(await readApiError(res, "That did not go through")); return }
      toast.success(done)
      await load()
      onChanged()
    } catch {
      toast.error("That did not go through")
    } finally {
      setBusy(null)
    }
  }

  /** Sign out several sessions of one device; one request each, then reload. */
  const revokeMany = async (key: string, ids: string[]) => {
    setBusy(key)
    let failed = 0
    for (const id of ids) {
      try {
        const res = await apiFetch(adminUserURL(user.id, workspaceId, `sessions/${encodeURIComponent(id)}/revoke`), { method: "POST" })
        if (!res.ok) failed++
      } catch {
        failed++
      }
    }
    if (failed) toast.error(`${failed} of ${ids.length} sessions could not be signed out`)
    else toast.success(ids.length > 1 ? `Signed out of ${ids.length} sessions` : "Signed out of that device")
    await load()
    onChanged()
    setBusy(null)
  }

  if (error) return <p className="text-[12px] text-destructive">{error}</p>
  if (!data) return <div className="grid gap-2"><Skeleton className="h-10 w-full" /><Skeleton className="h-10 w-full" /></div>
  const mobile = (ua: string | null) => !!ua && /iphone|android|mobile|ipad/i.test(ua)
  // The same browser on the same address signs in again and again (every
  // expired cookie is a new session); a list of 200 identical rows hides the
  // one device that matters. Group them, newest first.
  const currentId = data.sessions.find((x) => x.current)?.id
  const groups = Object.values(
    data.sessions.reduce<Record<string, { key: string; ua: string | null; ip: string | null; ids: string[]; last: string; current: boolean }>>((acc, x) => {
      const key = `${describeAgent(x.user_agent)}|${x.ip ?? ""}`
      const g = (acc[key] ??= { key, ua: x.user_agent, ip: x.ip, ids: [], last: x.last_used_at, current: false })
      g.ids.push(x.id)
      if (x.last_used_at > g.last) g.last = x.last_used_at
      if (x.current) g.current = true
      return acc
    }, {}),
  ).sort((a, b) => Number(b.current) - Number(a.current) || b.last.localeCompare(a.last))
  return (
    <>
      <DrawerSection label="Signed-in devices">
        {groups.length === 0 ? <p className="text-[12px] text-muted-foreground">Not signed in anywhere.</p> : (
          <ul className="overflow-hidden rounded-lg border border-border">
            {groups.map((g) => {
              const Icon = mobile(g.ua) ? Smartphone : Monitor
              const revocable = g.ids.filter((id) => id !== currentId)
              return (
                <li key={g.key} className="flex items-center gap-2.5 border-b border-border/60 px-3 py-2 last:border-b-0" data-slot="admin-session-group">
                  <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[12.5px]" title={g.ua ?? ""}>
                      {describeAgent(g.ua)}
                      {g.ids.length > 1 && <span className="ml-1.5 font-mono text-[11px] text-muted-foreground">× {g.ids.length}</span>}
                      {g.current && <span className="ml-1.5 text-[11px] text-success">this session</span>}
                    </span>
                    <span className="block truncate text-[11px] text-muted-foreground">last used {ago(g.last)}{g.ip ? ` · ${g.ip}` : ""}</span>
                  </span>
                  {revocable.length > 0 && (
                    <Button variant="outline" size="sm" className="h-7 text-xs" disabled={busy === g.key}
                      onClick={() => revokeMany(g.key, revocable)}>
                      {revocable.length > 1 ? `Sign out ${revocable.length}` : "Sign out"}
                    </Button>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </DrawerSection>
      <DrawerSection label="CLI tokens">
        {data.cli_tokens.length === 0 ? <p className="text-[12px] text-muted-foreground">No CLI tokens.</p> : (
          <ul className="overflow-hidden rounded-lg border border-border">
            {data.cli_tokens.map((t) => (
              <li key={t.id} className="flex items-center gap-2.5 border-b border-border/60 px-3 py-2 last:border-b-0">
                <Terminal className="h-4 w-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[12.5px]">{t.name}</span>
                  <span className="block truncate text-[11px] text-muted-foreground">
                    {Array.isArray(t.scopes) ? t.scopes.join(", ") : t.scopes || "all scopes"} · used {ago(t.last_used_at)}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        )}
      </DrawerSection>
      {data.sessions.some((s) => !s.current) && !isMe && (
        <Button variant="outline" size="sm" className="h-8 w-fit text-xs text-destructive" disabled={busy === "all"}
          onClick={() => { if (window.confirm(`Sign ${user.full_name ?? user.email} out of every device? Their password stays the same.`)) void post("sessions/revoke-all", "all", "Signed out everywhere") }}>
          <LogOut className="mr-1.5 h-3 w-3" />Sign out everywhere
        </Button>
      )}
    </>
  )
}

/** "Chrome · macOS" from a user-agent string; the raw string is on hover. */
export function describeAgent(ua: string | null): string {
  if (!ua) return "Unknown device"
  if (/crewship-cli|go-http-client/i.test(ua)) return "Crewship CLI"
  const browser = /edg\//i.test(ua) ? "Edge" : /firefox\//i.test(ua) ? "Firefox" : /chrome\//i.test(ua) ? "Chrome" : /safari\//i.test(ua) ? "Safari" : "Browser"
  const os = /iphone|ipad/i.test(ua) ? "iOS" : /android/i.test(ua) ? "Android" : /mac os x/i.test(ua) ? "macOS" : /windows/i.test(ua) ? "Windows" : /linux/i.test(ua) ? "Linux" : ""
  return os ? `${browser} · ${os}` : browser
}

