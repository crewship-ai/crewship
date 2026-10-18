"use client"

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"

import { Brain } from "lucide-react"

import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { entityHref } from "@/lib/entity-links"

import { CrCard, CrLine } from "./cr-card"

interface Waiting { inboxId: string; proposalId: string; rules: number; entries: number }
type Load = { state: "loading" } | { state: "error" } | { state: "ready"; waiting: Waiting[] }

/** Proposals looked up per page; more than this are still in the Inbox. */
const MAX_LOOKUPS = 20

async function readJson(res: Response): Promise<Record<string, unknown>> {
  const body: unknown = await res.json().catch(() => null)
  return body && typeof body === "object" ? (body as Record<string, unknown>) : {}
}

/**
 * Memory proposals of one crew that wait for a decision (v67 "Waiting for
 * review"). The server has no list of proposals: they surface as inbox items of
 * kind memory_consolidation carrying payload.proposal_id, and only when the
 * consolidator runs with CREWSHIP_CONSOLIDATE_HITL=1. So this lists those inbox
 * items, asks GET /consolidate/proposed/{id}/explain which crew each belongs to
 * and whether it is still pending, and decides with the existing approve and
 * reject calls. Nothing waiting means no card at all, not an empty one.
 */
export function CrewMemoryReview({ workspaceId, crewId, canDecide }: {
  workspaceId: string
  crewId: string
  /** The server lets only OWNER and ADMIN decide a proposal. */
  canDecide: boolean
}) {
  const ws = encodeURIComponent(workspaceId)
  const [load, setLoad] = useState<Load>({ state: "loading" })

  useEffect(() => {
    let cancelled = false
    setLoad({ state: "loading" })
    ;(async () => {
      const res = await apiFetch(`/api/v1/inbox?workspace_id=${ws}&kind=memory_consolidation&state=active&limit=50`)
      if (!res.ok) throw new Error(String(res.status))
      const rows = (await readJson(res)).rows
      const items = (Array.isArray(rows) ? rows : [])
        .map((r) => r as { id?: string; state?: string; payload?: { proposal_id?: unknown } })
        .filter((r) => typeof r.id === "string" && r.state !== "resolved" && typeof r.payload?.proposal_id === "string")
        .slice(0, MAX_LOOKUPS)
      const found = await Promise.all(items.map(async (r): Promise<Waiting | null> => {
        const proposalId = r.payload!.proposal_id as string
        const e = await apiFetch(`/api/v1/consolidate/proposed/${encodeURIComponent(proposalId)}/explain?workspace_id=${ws}`)
        if (!e.ok) return null
        const x = await readJson(e)
        if (x.crew_id !== crewId || x.status !== "pending") return null
        return { inboxId: r.id as string, proposalId, rules: Number(x.rules_count) || 0, entries: Number(x.entries_scanned) || 0 }
      }))
      if (!cancelled) setLoad({ state: "ready", waiting: found.filter((w): w is Waiting => w !== null) })
    })().catch(() => { if (!cancelled) setLoad({ state: "error" }) })
    return () => { cancelled = true }
  }, [ws, crewId])

  const drop = useCallback((proposalId: string) => {
    setLoad((l) => (l.state === "ready" ? { state: "ready", waiting: l.waiting.filter((w) => w.proposalId !== proposalId) } : l))
  }, [])

  if (load.state === "loading") return null
  if (load.state === "error") {
    return (
      <CrCard title="Waiting for review" concept="memory">
        <p role="alert" className="border-t border-border px-4 py-3 text-body text-chip-danger-fg">Memory proposals could not be loaded.</p>
      </CrCard>
    )
  }
  if (load.waiting.length === 0) return null
  return (
    <CrCard title={`Waiting for review · ${load.waiting.length}`} concept="memory" icon={Brain} data-testid="crew-memory-review">
      {load.waiting.map((w) => (
        <ProposalRow key={w.proposalId} w={w} workspaceId={workspaceId} canDecide={canDecide} onDecided={drop} />
      ))}
    </CrCard>
  )
}

function ProposalRow({ w, workspaceId, canDecide, onDecided }: {
  w: Waiting
  workspaceId: string
  canDecide: boolean
  onDecided: (proposalId: string) => void
}) {
  const ws = encodeURIComponent(workspaceId)
  const [busy, setBusy] = useState<"approve" | "reject" | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [open, setOpen] = useState(false)
  const [diff, setDiff] = useState<{ text: string } | { error: string } | null>(null)

  const toggle = async () => {
    const next = !open
    setOpen(next)
    if (!next || diff) return
    try {
      const res = await apiFetch(`/api/v1/consolidate/proposed/${encodeURIComponent(w.proposalId)}/diff?workspace_id=${ws}`)
      if (!res.ok) throw await toApiError(res, `The changes could not be loaded (${res.status}).`)
      const body = await readJson(res)
      setDiff({ text: typeof body.diff === "string" && body.diff ? body.diff : "No changes to show." })
    } catch (e) {
      setDiff({ error: e instanceof Error ? e.message : "The changes could not be loaded." })
    }
  }

  const decide = async (action: "approve" | "reject") => {
    if (busy) return
    setBusy(action)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/consolidate/proposed/${encodeURIComponent(w.proposalId)}/${action}?workspace_id=${ws}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
      })
      if (!res.ok) throw await toApiError(res, `The proposal could not be ${action === "approve" ? "accepted" : "rejected"} (${res.status}).`)
      // The decision is made. The inbox row is the same ask, so it is closed
      // the way the Inbox closes it; if that fails the decision still stands.
      await apiFetch(`/api/v1/inbox/${encodeURIComponent(w.inboxId)}?workspace_id=${ws}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ state: "resolved", resolved_action: action === "approve" ? "approved" : "rejected" }),
      }).catch(() => undefined)
      onDecided(w.proposalId)
    } catch (e) {
      setError(e instanceof Error ? e.message : "The decision failed.")
    } finally {
      setBusy(null)
    }
  }

  return (
    <div data-proposal={w.proposalId}>
      <CrLine
        title={`${w.rules} ${w.rules === 1 ? "rule" : "rules"} learned from recent runs`}
        sub={`Consolidator · from ${w.entries} ${w.entries === 1 ? "entry" : "entries"} · becomes crew memory once accepted`}
        right={
          <>
            <Button variant="ghost" size="sm" aria-expanded={open} onClick={() => void toggle()}>View changes</Button>
            {canDecide ? (
              <>
                <Button variant="ghost" size="sm" disabled={busy !== null} onClick={() => void decide("reject")}>{busy === "reject" ? "Rejecting…" : "Reject"}</Button>
                <Button variant="outline" size="sm" disabled={busy !== null} onClick={() => void decide("approve")}>{busy === "approve" ? "Accepting…" : "Accept"}</Button>
              </>
            ) : (
              <Button asChild variant="outline" size="sm"><Link href={entityHref({ kind: "inbox", itemId: w.inboxId, itemKind: "memory_consolidation" })}>Open in Inbox</Link></Button>
            )}
          </>
        }
      />
      {!canDecide && <p className="px-4 pb-2.5 text-label text-muted-foreground">Owners and admins decide memory proposals.</p>}
      {error && <p role="status" className="px-4 pb-2.5 text-label text-chip-danger-fg">{error}</p>}
      {open && (
        <div className="px-4 pb-3">
          {diff === null ? <p className="text-label text-muted-foreground">Loading…</p>
            : "error" in diff ? <p role="status" className="text-label text-chip-danger-fg">{diff.error}</p>
              : <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted px-3 py-2 font-mono text-label text-foreground">{diff.text}</pre>}
        </div>
      )}
    </div>
  )
}
