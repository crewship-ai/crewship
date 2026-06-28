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

import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

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
      } catch (e) {
        setErr((e as Error).message)
      } finally {
        setActing(false)
      }
    },
    [load, workspaceId],
  )

  const deleteAll = useCallback(async () => {
    if (!confirm("Delete every peer card about you across every agent in this workspace?")) return
    setActing(true)
    setErr(null)
    try {
      const r = await apiFetch("/api/v1/users/me/peer-cards", {
        method: "DELETE",
        headers: { "X-Workspace-ID": workspaceId },
      })
      if (!r.ok) throw new Error(`delete peer cards failed: ${r.status}`)
      await load()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setActing(false)
    }
  }, [load, workspaceId])

  if (loading) return <p className="text-sm text-muted-foreground">Loading privacy state...</p>
  if (err) return <div className="rounded border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-300">{err}</div>

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <header>
          <h2 className="text-lg font-semibold">Agent memory about you</h2>
          <p className="text-sm text-muted-foreground">
            Crewship agents may distil per-user profile notes from prior sessions
            (≤1500 bytes per agent). These notes shape how the agent addresses
            you — they are NOT shared with other operators.
          </p>
        </header>

        <div className="rounded border border-white/10 p-4">
          <div className="flex items-center justify-between">
            <div>
              <p className="font-medium">
                {consent?.opted_out ? "Opted out" : "Opted in (default)"}
              </p>
              <p className="text-sm text-muted-foreground">
                {consent?.opted_out
                  ? `Opted out at ${consent.opted_out_at ?? "unknown"}. Agents will not extract new peer cards about you in this workspace.`
                  : "Agents may extract peer cards about you. You can opt out at any time."}
              </p>
            </div>
            <button
              onClick={() => flipConsent(!consent?.opted_out)}
              disabled={acting}
              className="rounded bg-emerald-500/20 px-3 py-1.5 text-sm text-emerald-300 hover:bg-emerald-500/30 disabled:opacity-50"
            >
              {consent?.opted_out ? "Opt back in" : "Opt out"}
            </button>
          </div>
          {!consent?.opted_out && (
            <p className="mt-3 text-xs text-muted-foreground">
              Opt-out is immediate: existing cards are purged as part of the same
              request, not on the next routine sweep.
            </p>
          )}
        </div>
      </section>

      <section className="space-y-3">
        <header className="flex items-center justify-between">
          <div>
            <h2 className="text-lg font-semibold">Peer cards on file ({cards.length})</h2>
            <p className="text-sm text-muted-foreground">
              Everything currently stored about you across this workspace.
            </p>
          </div>
          {cards.length > 0 && (
            <button
              onClick={deleteAll}
              disabled={acting}
              className="rounded bg-red-500/15 px-3 py-1.5 text-sm text-red-300 hover:bg-red-500/25 disabled:opacity-50"
            >
              Delete all
            </button>
          )}
        </header>
        {cards.length === 0 ? (
          <p className="text-sm text-muted-foreground">No peer cards on file.</p>
        ) : (
          <ul className="space-y-2">
            {cards.map((c) => (
              <li key={c.id} className="rounded border border-white/10 p-3">
                <div className="flex items-center justify-between">
                  <span className="font-medium">{c.agent_slug}</span>
                  <span className="text-xs text-muted-foreground">
                    {c.bytes} B · updated {c.updated_at}
                  </span>
                </div>
                {c.content && (
                  <pre className="mt-2 whitespace-pre-wrap text-sm bg-zinc-900/40 p-2 rounded">
                    {c.content}
                  </pre>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
