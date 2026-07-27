"use client"

import { useCallback, useEffect, useState } from "react"
import { Laptop, LogOut, Smartphone, Terminal, HelpCircle } from "lucide-react"
import { toast } from "sonner"

import { apiFetch } from "@/lib/api-fetch"
import { describeUserAgent, type DeviceKind } from "@/lib/user-agent"
import { formatShortDate, timeAgo } from "@/lib/time"
import { Button } from "@/components/ui/button"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { SettingsRow, SettingsEmpty } from "../shared"

/**
 * The "Browsers & devices" half of Settings → Profile → Sessions & access.
 *
 * Its job is not inventory, it is detection: someone scanning this list
 * should be able to notice a login that isn't theirs and end it. Everything
 * below follows from that.
 *
 * Lives in its own file rather than inline in profile-section (already
 * ~1000 lines) so the fetch/revoke logic is testable on its own. The CLI
 * token half stays in profile-section — it shares the create-form state —
 * and the two render as two groups inside one card.
 */

interface SessionDTO {
  id: string
  created_at: string
  last_used_at: string
  user_agent: string
  ip: string
  is_current: boolean
}

function DeviceIcon({ kind }: { kind: DeviceKind }) {
  const Icon = kind === "mobile" ? Smartphone : kind === "cli" ? Terminal : kind === "unknown" ? HelpCircle : Laptop
  return (
    <span className="size-7 rounded-lg bg-muted/50 grid place-items-center shrink-0">
      <Icon className="size-3.5 text-muted-foreground" />
    </span>
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
  const [busyId, setBusyId] = useState<string | null>(null)
  const [confirmBulk, setConfirmBulk] = useState(false)
  const [bulkRunning, setBulkRunning] = useState(false)

  const load = useCallback(async () => {
    try {
      const res = await apiFetch("/api/v1/auth/sessions")
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
    const results = await Promise.all(others.map(async (s) => {
      try {
        const res = await apiFetch(`/api/v1/auth/sessions/${s.id}/revoke`, { method: "POST" })
        return res.ok
      } catch {
        return false
      }
    }))
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

  if (error) {
    return (
      <SettingsEmpty>
        <div className="space-y-2">
          <div className="text-destructive">{error}</div>
          <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" onClick={() => { setError(null); load() }}>
            Retry
          </Button>
        </div>
      </SettingsEmpty>
    )
  }

  if (sessions === null) {
    return (
      <div className="px-4 py-3 space-y-2">
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
      </div>
    )
  }

  // Current device first: it is the reader's anchor for judging the rest.
  const ordered = [...sessions].sort((a, b) => Number(b.is_current) - Number(a.is_current))

  return (
    <>
      <div className="flex items-center justify-between px-4 pt-2.5 pb-1.5">
        <span className="text-[9.5px] uppercase tracking-[0.1em] text-muted-foreground/70 font-semibold">
          Browsers &amp; devices
        </span>
        {others.length > 0 && (
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-[11px] text-destructive hover:text-destructive hover:bg-destructive/10"
            onClick={() => setConfirmBulk(true)}
          >
            Sign out everywhere else
          </Button>
        )}
      </div>

      {ordered.map((s) => {
        const { label, kind } = describeUserAgent(s.user_agent)
        return (
          <SettingsRow
            key={s.id}
            label={
              <span className="flex items-center gap-2.5">
                <DeviceIcon kind={kind} />
                <span className="min-w-0">
                  <span className="flex items-center gap-2">
                    <span className="truncate">{label}</span>
                    {s.is_current && (
                      <span className="text-[10px] px-1.5 py-px rounded-full bg-success/15 text-success border border-success/30">
                        this device
                      </span>
                    )}
                  </span>
                  <span className="block text-[11px] text-muted-foreground/80 mt-0.5">
                    {s.ip} · {timeAgo(s.last_used_at)} · signed in {formatShortDate(s.created_at)}
                    {s.is_current && currentExpiresIn ? ` · expires in ${currentExpiresIn}` : ""}
                  </span>
                </span>
              </span>
            }
          >
            {s.is_current ? (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 px-2.5 text-xs text-destructive hover:text-destructive hover:bg-destructive/10"
                onClick={onSignOut}
              >
                <LogOut className="size-3 mr-1.5" />
                Sign out
              </Button>
            ) : (
              <Button
                variant="ghost"
                size="sm"
                aria-label={`Revoke ${label}`}
                disabled={busyId === s.id}
                className="h-7 px-2.5 text-xs text-destructive hover:text-destructive hover:bg-destructive/10"
                onClick={() => revoke(s, label)}
              >
                {busyId === s.id ? "Signing out…" : "Revoke"}
              </Button>
            )}
          </SettingsRow>
        )
      })}

      {others.length === 0 && (
        <div className="px-4 pb-3 text-[11px] text-muted-foreground">
          You&rsquo;re not signed in anywhere else.
        </div>
      )}

      <AlertDialog open={confirmBulk} onOpenChange={(o) => !bulkRunning && setConfirmBulk(o)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Sign out everywhere else?</AlertDialogTitle>
            <AlertDialogDescription>
              This ends {others.length} other session{others.length === 1 ? "" : "s"}. This
              device stays signed in, and CLI tokens are not affected — revoke those
              individually below.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs" disabled={bulkRunning}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 text-xs bg-destructive/15 text-destructive border border-destructive/30 hover:bg-destructive/25"
              disabled={bulkRunning}
              onClick={(e) => { e.preventDefault(); void revokeOthers() }}
            >
              {bulkRunning ? "Signing out…" : `Sign out ${others.length} device${others.length === 1 ? "" : "s"}`}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
