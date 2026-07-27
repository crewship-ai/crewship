"use client"

// PR-E F6 — User Privacy section.
//
// Lives under Settings → Privacy. Three responsibilities:
//
//   1. Show + flip the user's peer card opt-out for the current
//      workspace. Opt-out is the GDPR primitive; flipping it ON
//      triggers immediate purge of every existing card across
//      every agent in the workspace.
//
//   2. List every peer card mentioning the requesting user, with
//      content visible. This is the "show me what you know about
//      me" surface a SAR (subject access request) workflow would
//      exercise.
//
//   3. Provide a single "delete all my peer cards" button that
//      walks the same purge path as the opt-out (minus the consent
//      flip — a user can delete current state without committing
//      to forever-opt-out).
//
// Both controls are atomic (no draft/Save step) — they PUT/DELETE
// immediately, same as before. The only UI addition here is a toast
// on success and a confirm dialog in place of window.confirm(), plus
// matching the shared SettingsCard/SettingsRow chrome the rest of the
// page uses (this used to be the visual odd-one-out with raw boxes
// and hardcoded emerald/red/zinc colours).

import { useCallback, useEffect, useState } from "react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { formatDateTime } from "@/lib/time"
import { SettingsCard, SettingsRow, SettingsEmpty } from "../shared"

interface ConsentResp {
  user_id: string
  workspace_id: string
  opted_out: boolean
  opted_out_at?: string
}

interface PeerEntry {
  id: string
  agent_id: string
  agent_slug: string
  user_slug: string
  bytes: number
  created_at: string
  updated_at: string
  content?: string
}

export function PrivacySection({ workspaceId }: { workspaceId: string }) {
  const [consent, setConsent] = useState<ConsentResp | null>(null)
  const [cards, setCards] = useState<PeerEntry[]>([])
  const [err, setErr] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [acting, setActing] = useState(false)
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setErr(null)
    try {
      const headers = { "X-Workspace-ID": workspaceId }
      const [c, p] = await Promise.all([
        apiFetch("/api/v1/users/me/peer-consent", { headers }),
        apiFetch("/api/v1/users/me/peer-cards", { headers }),
      ])
      // Fail fast on non-2xx so the UI never presents stale consent
      // state as if it were the operator's actual choice. Both routes
      // are required for the screen to be coherent — partial failure
      // would mislead a user into thinking opt-out flipped when it
      // actually didn't.
      if (!c.ok) throw new Error(`load consent failed: ${c.status}`)
      if (!p.ok) throw new Error(`load peer cards failed: ${p.status}`)
      setConsent(await c.json())
      const data = await p.json()
      setCards(data.peers || [])
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    load()
  }, [load])

  const flipConsent = useCallback(
    async (optOut: boolean) => {
      setActing(true)
      setErr(null)
      try {
        const r = await apiFetch("/api/v1/users/me/peer-consent", {
          method: "PUT",
          headers: { "Content-Type": "application/json", "X-Workspace-ID": workspaceId },
          body: JSON.stringify({ opted_out: optOut }),
        })
        if (!r.ok) throw new Error(`update consent failed: ${r.status}`)
        await load()
        toast.success(optOut ? "Opted out — existing peer cards purged" : "Opted back in")
      } catch (e) {
        setErr((e as Error).message)
      } finally {
        setActing(false)
      }
    },
    [load, workspaceId],
  )

  const deleteAll = useCallback(async () => {
    setActing(true)
    setErr(null)
    try {
      const r = await apiFetch("/api/v1/users/me/peer-cards", {
        method: "DELETE",
        headers: { "X-Workspace-ID": workspaceId },
      })
      if (!r.ok) throw new Error(`delete peer cards failed: ${r.status}`)
      await load()
      toast.success("Peer cards deleted")
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setActing(false)
      setConfirmDeleteOpen(false)
    }
  }, [load, workspaceId])

  if (loading) return <p className="text-sm text-muted-foreground">Loading privacy state...</p>
  if (err) {
    return (
      <div className="rounded-xl border border-destructive/30 bg-destructive/[0.02] p-4 text-sm text-destructive">
        {err}
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <SettingsCard
        title="Agent memory about you"
        description="Crewship agents may distil per-user profile notes from prior sessions (≤1500 bytes per agent). These notes shape how the agent addresses you — they are NOT shared with other operators."
      >
        <SettingsRow
          label={consent?.opted_out ? "Opted out" : "Opted in (default)"}
          description={
            consent?.opted_out
              ? `Opted out at ${consent.opted_out_at ? formatDateTime(consent.opted_out_at) : "unknown"}. Agents will not extract new peer cards about you in this workspace.`
              : "Agents may extract peer cards about you. You can opt out at any time. Opt-out is immediate: existing cards are purged as part of the same request, not on the next routine sweep."
          }
          border={false}
        >
          <Button
            size="sm"
            variant="outline"
            className="h-7 px-2.5 text-xs"
            onClick={() => flipConsent(!consent?.opted_out)}
            disabled={acting}
          >
            {consent?.opted_out ? "Opt back in" : "Opt out"}
          </Button>
        </SettingsRow>
      </SettingsCard>

      <SettingsCard
        title={`Peer cards on file (${cards.length})`}
        description="Everything currently stored about you across this workspace."
        actions={
          cards.length > 0 && (
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2.5 text-xs text-destructive hover:text-destructive hover:bg-destructive/10"
              onClick={() => setConfirmDeleteOpen(true)}
              disabled={acting}
            >
              Delete all
            </Button>
          )
        }
      >
        {cards.length === 0 ? (
          <SettingsEmpty>No peer cards on file.</SettingsEmpty>
        ) : (
          cards.map((c, idx) => (
            <SettingsRow
              key={c.id}
              label={c.agent_slug}
              description={c.content ? <span className="whitespace-pre-wrap">{c.content}</span> : undefined}
              border={idx < cards.length - 1}
              className="items-start"
            >
              <span className="text-[11px] text-muted-foreground shrink-0">
                {c.bytes} B · updated {formatDateTime(c.updated_at)}
              </span>
            </SettingsRow>
          ))
        )}
      </SettingsCard>

      <AlertDialog open={confirmDeleteOpen} onOpenChange={(o) => !acting && setConfirmDeleteOpen(o)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="text-sm">Delete all peer cards</AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              Delete every peer card about you across every agent in this workspace? This action
              cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs" disabled={acting}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 text-xs bg-destructive text-destructive-foreground hover:bg-destructive/90"
              disabled={acting}
              onClick={(e) => {
                // Keep the dialog open while the DELETE is in flight; a
                // second click can't fire a duplicate request.
                e.preventDefault()
                if (!acting) void deleteAll()
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
