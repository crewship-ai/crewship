"use client"

import { Button } from "@/components/ui/button"
import { validNativeVersion } from "@/lib/mooring/native-draft"

export interface NativeCraftLeaseView {
  holder: { actor_id: string; display_name: string; kind: "human" | "agent" } | null
  can_stop_agent?: boolean
  mine: boolean
  generation: string | null
  can_write: boolean
  acquired_at: string | null
  handoff_requested_by: Array<{ request_id: string; display_name: string; kind: "human" | "agent" }>
}
export type NativeCraftLeaseAction = "acquire" | "release" | "request-handoff" | "handoff"
type ActionBody = { generation?: string; request_id?: string }

/** Advisory controls only. Every action is still authorized and fenced by the
 * server; the status never contains the private admission bearer. */
export function NativeCraftLeaseBar({ view, mayEdit, pending = false, onAction, onStopAgent }: { view: NativeCraftLeaseView; mayEdit: boolean; pending?: boolean; onStopAgent?(): void; onAction(action: NativeCraftLeaseAction, body: ActionBody): void }) {
  const generation = validNativeVersion(view.generation) && view.generation !== "0" ? view.generation : undefined
  const mine = mayEdit && view.mine && view.can_write && generation !== undefined
  const name = view.holder?.display_name.trim() || (view.holder?.kind === "agent" ? "Agent" : "Někdo")
  const since = view.acquired_at && Number.isFinite(Date.parse(view.acquired_at)) ? new Date(view.acquired_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }) : null
  return <div role="toolbar" aria-label="Úpravy dokumentu" aria-busy={pending} className="flex flex-wrap items-center gap-2 border-b px-3 py-2 text-sm">
    <span>{view.holder ? `Upravuje ${name}` : "Volné"}{since ? ` · od ${since}` : ""}</span>
    {!mayEdit && <span className="text-muted-foreground">Pouze čtení</span>}
    {mayEdit && !view.holder && <Button size="sm" disabled={pending} onClick={() => onAction("acquire", {})}>Převzít úpravy</Button>}
    {mayEdit && view.holder && !view.mine && <Button size="sm" variant="outline" disabled={pending} onClick={() => onAction("request-handoff", {})}>Požádat o předání</Button>}
    {mayEdit && view.holder?.kind === "agent" && view.can_stop_agent === true && onStopAgent && <Button size="sm" variant="outline" disabled={pending} onClick={onStopAgent}>Zastavit agenta</Button>}
    {mine && <>
      <Button size="sm" variant="outline" disabled={pending} onClick={() => onAction("release", { generation })}>Uvolnit úpravy</Button>
      {view.handoff_requested_by.map(request => <Button key={request.request_id} size="sm" disabled={pending} onClick={() => onAction("handoff", { generation, request_id: request.request_id })}>Předat uživateli {request.display_name.trim() || (request.kind === "agent" ? "Agent" : "Uživatel")}</Button>)}
    </>}
    {mayEdit && view.mine && !view.can_write && <span className="text-muted-foreground">Pouze čtení · místní koncept zachován</span>}
  </div>
}
