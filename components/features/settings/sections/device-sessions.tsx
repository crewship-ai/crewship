"use client"

import { StatusPill } from "@/components/ui/status-pill"
import { useCallback, useEffect, useState, type ReactNode } from "react"
import { Laptop, LogOut, MonitorSmartphone, Smartphone, Terminal, HelpCircle } from "lucide-react"
import { toast } from "@/lib/toast"

import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { describeUserAgent, type DeviceKind } from "@/lib/user-agent"
import { formatShortDate, timeAgo } from "@/lib/time"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { SettingsCard, SettingsCardActions, SettingsRow, SettingsEmpty } from "../shared"

/**
 * Settings › Sessions & tokens › "Browsers & devices" — its own card (Harbor
 * 2), beside the CLI tokens card.
 *
 * Its job is not inventory, it is detection: someone scanning this list
 * should be able to notice a login that isn't theirs and end it. Everything
 * below follows from that. One device ends at once with a toast; ending all
 * the others asks first (ConfirmDialog), and the dialog waits for the loop.
 */

interface SessionDTO {
  id: string
  created_at: string
  last_used_at: string
  user_agent: string
  ip: string
  is_current: boolean
}

/** Rows shown before "Show all": the ones anyone looks at. */
export const VISIBLE_SESSIONS = 5

/** This device first — the reader's anchor — then most recently used. */
export function orderSessions(sessions: SessionDTO[]): SessionDTO[] {
  return [...sessions].sort(
    (a, b) => Number(b.is_current) - Number(a.is_current) || Date.parse(b.last_used_at) - Date.parse(a.last_used_at),
  )
}

export function DeviceSessions({
  onSignOut,
  currentExpiresIn,
}: {
  onSignOut?: () => void
  /** Countdown for the current session, e.g. "9m". Rendered on the current
   *  row so removing the old standalone "Session" card does not lose the
   *  one genuinely useful thing it said. */
  currentExpiresIn?: string
}) {
  const [sessions, setSessions] = useState<SessionDTO[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [signedOut, setSignedOut] = useState(false)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [confirmBulk, setConfirmBulk] = useState(false)
  const [bulkRunning, setBulkRunning] = useState(false)
  const [showAll, setShowAll] = useState(false)

  const load = useCallback(async () => {
    try {
      const res = await apiFetch("/api/v1/auth/sessions")
      if (res.status === 401) {
        // apiFetch already retried through the refresh endpoint; a 401
        // reaching here means refresh failed too, so AuthProvider is
        // mid-redirect to /login. Claiming the sessions "couldn't load"
        // would blame the endpoint for the user being signed out, and
        // offer a Retry that cannot possibly work. Stay quiet and let the
        // redirect happen.
        setSignedOut(true)
        return
      }
      if (res.status === 429) {
        // The endpoint is fine; the caller went too fast. Saying "couldn't
        // load" sends someone hunting a breakage that isn't there.
        setError("Too many requests — wait a moment, then retry.")
        return
      }
      if (!res.ok) throw new Error(String(res.status))
      const data = await res.json()
      setSessions(Array.isArray(data) ? data : (data?.data ?? []))
      setError(null)
    } catch {
      // Deliberately not falling back to an empty list: "no other devices"
      // is both wrong and reassuring, which is the worst pair of properties
      // a failure mode can have on a security screen.
      setError("Couldn't load your sessions.")
    }
  }, [])

  useEffect(() => { load() }, [load])

  const revoke = useCallback(async (s: SessionDTO, label: string) => {
    setBusyId(s.id)
    try {
      const res = await apiFetch(`/api/v1/auth/sessions/${s.id}/revoke`, { method: "POST" })
      if (!res.ok) throw new Error(String(res.status))
      toast.success(`Signed out ${label}`)
      await load()
    } catch {
      toast.error(`Couldn't sign out ${label}`)
    } finally {
      setBusyId(null)
    }
  }, [load])

  const others = (sessions ?? []).filter((s) => !s.is_current)

  const revokeOthers = useCallback(async () => {
    setBulkRunning(true)
    // No bulk endpoint exists; this is a loop. Partial failure is therefore
    // a real outcome and gets reported as one — a single silent "done" would
    // leave a live session the user believes they killed.
    // Sequential, not Promise.all: these share one per-IP bucket, so
    // firing them together throttles the very action just confirmed and
    // reports it back as a partial failure the user did nothing to cause.
    const results: boolean[] = []
    for (const s of others) {
      try {
        const res = await apiFetch(`/api/v1/auth/sessions/${s.id}/revoke`, { method: "POST" })
        results.push(res.ok)
      } catch {
        results.push(false)
      }
    }
    const failed = results.filter((ok) => !ok).length
    if (failed === 0) {
      toast.success(`Signed out ${results.length} device${results.length === 1 ? "" : "s"}`)
    } else {
      toast.error(`${failed} of ${results.length} could not be signed out`, {
        description: "The remaining sessions are still active.",
      })
    }
    setBulkRunning(false)
    setConfirmBulk(false)
    await load()
  }, [others, load])

  // Nothing to say while the sign-out redirect is in flight.
  if (signedOut) return null

  const card = (body: ReactNode, actions?: ReactNode) => (
    <SettingsCard icon={MonitorSmartphone} title="Browsers & devices" description="Where you are signed in">
      {body}
      {actions}
    </SettingsCard>
  )

  if (error) {
    return card(
      <SettingsEmpty>
        <div className="space-y-2">
          <div role="alert" className="text-destructive">{error}</div>
          <Button variant="outline" size="sm" onClick={() => { setError(null); load() }}>
            Retry
          </Button>
        </div>
      </SettingsEmpty>,
    )
  }

  if (sessions === null) {
    return card(
      <div className="px-4 py-3 space-y-2" role="status" aria-label="Loading sessions">
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
      </div>,
    )
  }

  const ordered = orderSessions(sessions)
  const visible = showAll ? ordered : ordered.slice(0, VISIBLE_SESSIONS)
  const hidden = ordered.length - VISIBLE_SESSIONS

  return (
    <>
      {card(
        // Past five, the list scrolls inside the card instead of growing the
        // page: ~70 sessions from a script used to fill the whole screen.
        <div className={cn(showAll && hidden > 0 && "max-h-[26rem] overflow-y-auto overscroll-contain")} data-slot="session-list">
          {visible.map((s) => {
            const { label, kind } = describeUserAgent(s.user_agent)
            return (
              <SettingsRow
                key={s.id}
                lead={deviceIcon(kind)}
                label={
                  <span className="inline-flex flex-wrap items-center gap-2">
                    <span className="truncate">{label}</span>
                    {s.is_current && <StatusPill tone="success" label="This device" />}
                  </span>
                }
                description={`Signed in ${formatShortDate(s.created_at)}${s.is_current && currentExpiresIn ? ` · expires in ${currentExpiresIn}` : ""}`}
              >
                <span className="font-mono text-label text-muted-foreground">{s.ip} · used {timeAgo(s.last_used_at)}</span>
                {s.is_current ? (
                  <Button variant="outline" size="sm" onClick={onSignOut}>
                    <LogOut className="size-3 mr-1.5" />
                    Sign out
                  </Button>
                ) : (
                  // One device ends at once with a toast (Harbor 2): the row
                  // is the confirmation, and it can be signed in again.
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-label={`Revoke ${label}`}
                    disabled={busyId === s.id}
                    className="text-destructive hover:text-destructive hover:bg-destructive/10"
                    onClick={() => revoke(s, label)}
                  >
                    {busyId === s.id ? "Signing out…" : "Revoke"}
                  </Button>
                )}
              </SettingsRow>
            )
          })}
        </div>,
        <SettingsCardActions className="border-t border-border pt-3">
          {others.length > 0 ? (
            <Button variant="outline" size="sm" onClick={() => setConfirmBulk(true)}>
              Sign out everywhere else
            </Button>
          ) : (
            <span className="text-label text-muted-foreground">You&rsquo;re not signed in anywhere else.</span>
          )}
          {hidden > 0 && (
            <Button variant="link" size="sm" className="px-0 text-primary-hover" onClick={() => setShowAll((v) => !v)}>
              {showAll ? "Show fewer" : `Show all ${ordered.length} sessions`}
            </Button>
          )}
        </SettingsCardActions>,
      )}

      <ConfirmDialog
        open={confirmBulk}
        onOpenChange={(o) => { if (!bulkRunning) setConfirmBulk(o) }}
        title="Sign out everywhere else?"
        description={`This ends ${others.length} other session${others.length === 1 ? "" : "s"}.`}
        consequences={[
          { tone: "lost", text: `${others.length} other browser${others.length === 1 ? "" : "s"} and device${others.length === 1 ? "" : "s"} must sign in again` },
          { tone: "kept", text: "This device stays signed in" },
          { tone: "kept", text: "CLI tokens are not affected — revoke those individually below" },
        ]}
        confirmLabel={`Sign out ${others.length} device${others.length === 1 ? "" : "s"}`}
        destructive
        onConfirm={revokeOthers}
      />
    </>
  )
}

function deviceIcon(kind: DeviceKind) {
  return kind === "mobile" ? Smartphone : kind === "cli" ? Terminal : kind === "unknown" ? HelpCircle : Laptop
}
