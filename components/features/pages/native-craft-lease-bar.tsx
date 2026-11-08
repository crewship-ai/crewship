"use client"

import * as React from "react"
import { Button } from "@/components/ui/button"
import { validNativeVersion } from "@/lib/mooring/native-draft"

export interface NativeCraftLeaseView {
  holder: { actor_id: string; display_name: string; kind: "human" | "agent" } | null
  can_edit?: boolean
  /** Only the Page owner shares a version; Edit alone never offers it. */
  can_share?: boolean
  can_stop_agent?: boolean
  mine: boolean
  generation: string | null
  can_write: boolean
  acquired_at: string | null
  /** When an unrenewed lease lapses (TTL 15 s, renewed only while its editor is connected). */
  expires_at?: string | null
  handoff_requested_by: Array<{ request_id: string; display_name: string; kind: "human" | "agent" }>
}
export type NativeCraftLeaseAction = "acquire" | "release" | "request-handoff" | "handoff"
type ActionBody = { generation?: string; request_id?: string }

/** Advisory controls only. Every action is still authorized and fenced by the
 * server; the status never contains the private admission bearer. */
export function NativeCraftLeaseBar({ view, mayEdit, pending = false, requested = false, onAction, onStopAgent, self, variant = "bar" }: { /** "popover": inside a status popover (the Artifacts frame), not a row above the editor. */ variant?: "bar" | "popover"; view: NativeCraftLeaseView; mayEdit: boolean; pending?: boolean; /** Server-confirmed request for this holder and generation. */ requested?: boolean; onStopAgent?(): void; onAction(action: NativeCraftLeaseAction, body: ActionBody): void; /** The signed-in person's user id. */ self?: string }) {
  // The same person in another editor session: typically the tab this one
  // replaced by a reload. Its lease is renewed only while that editor is
  // connected, so a closed tab frees it within the TTL; say so instead of
  // offering to "ask" oneself.
  const otherSession = Boolean(self) && view.holder?.kind === "human" && !view.mine && (view.holder.actor_id === `user:${self}` || view.holder.actor_id === self)
  const releaseIn = useSecondsUntil(otherSession ? view.expires_at ?? null : null)
  const generation = validNativeVersion(view.generation) && view.generation !== "0" ? view.generation : undefined
  const mine = mayEdit && view.mine && view.can_write && generation !== undefined
  const name = view.holder?.display_name.trim() || (view.holder?.kind === "agent" ? "Agent" : "Someone")
  const since = view.acquired_at && Number.isFinite(Date.parse(view.acquired_at)) ? new Date(view.acquired_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }) : null
  return <div role="toolbar" aria-label="Document editing" aria-busy={pending} className={variant === "popover" ? "flex w-full flex-wrap items-center gap-1.5 text-sm" : "flex flex-wrap items-center gap-2 border-b px-3 py-2 text-sm"}>
    <span className={variant === "popover" ? "w-full font-medium" : undefined}>{otherSession ? "You are editing in another tab or window" : view.holder ? `${name} is editing` : "Free"}{since ? ` · since ${since}` : ""}</span>
    {otherSession && <span role="status" className="text-muted-foreground">{releaseIn !== null && releaseIn > 0 ? `If you closed or reloaded it, editing is released within ${releaseIn} s.` : "Waiting for the other tab to release editing…"}</span>}
    {!mayEdit && <span className="text-muted-foreground">Read-only</span>}
    {mayEdit && !view.holder && <Button size="sm" disabled={pending} onClick={() => onAction("acquire", {})}>Take over editing</Button>}
    {mayEdit && view.holder && !view.mine && <Button size="sm" variant="outline" disabled={pending || requested} onClick={() => onAction("request-handoff", {})}>{requested ? "Request sent" : otherSession ? "Ask the other tab to hand over" : "Request edit"}</Button>}
    {mayEdit && view.holder && !view.mine && <span data-slot="handoff-request-status" aria-live="polite" aria-atomic="true" role={requested ? "status" : undefined} className="text-muted-foreground">{requested ? view.holder.kind === "agent" ? "Request recorded. An agent does not hand over editing by itself." : "Waiting for the handover." : ""}</span>}
    {mayEdit && view.holder?.kind === "agent" && view.can_stop_agent === true && onStopAgent && <Button size="sm" variant="outline" disabled={pending} onClick={onStopAgent}>Stop agent</Button>}
    {mine && <>
      <Button size="sm" variant="outline" disabled={pending} onClick={() => onAction("release", { generation })}>Hand back</Button>
      {view.handoff_requested_by.map(request => <Button key={request.request_id} size="sm" disabled={pending} onClick={() => onAction("handoff", { generation, request_id: request.request_id })}>Hand over to {request.display_name.trim() || (request.kind === "agent" ? "Agent" : "User")}</Button>)}
    </>}
    {mayEdit && view.mine && !view.can_write && <span className="text-muted-foreground">Read-only · local draft kept</span>}
  </div>
}

/** Whole seconds until `iso` (ticking once a second), or null without a valid time. */
function useSecondsUntil(iso: string | null): number | null {
  const at = iso ? Date.parse(iso) : NaN
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    if (!Number.isFinite(at)) return
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [at])
  return Number.isFinite(at) ? Math.max(0, Math.ceil((at - now) / 1000)) : null
}
