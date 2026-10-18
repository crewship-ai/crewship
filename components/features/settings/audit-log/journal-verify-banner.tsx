"use client"

import { useState } from "react"
import { ShieldAlert, ShieldCheck, ShieldQuestion } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"

/**
 * Settings › Audit log: "Journal chain verified · Verify now".
 *
 * The journal is a hash chain; GET /api/v1/admin/journal/verify (ADMIN+, the
 * same check as `crewship journal verify`) walks it and reports the first
 * entry that does not match. It walks every entry, so it runs when asked —
 * not on every visit to the page.
 */

interface VerifyResult {
  ok: boolean
  count?: number
  broken_seq?: number
  reason?: string
}

type State =
  | { kind: "idle" }
  | { kind: "running" }
  | { kind: "ok"; count: number; at: Date }
  | { kind: "broken"; seq?: number; reason?: string; at: Date }
  | { kind: "error"; message: string }

export function JournalVerifyBanner({ workspaceId }: { workspaceId: string }) {
  const [state, setState] = useState<State>({ kind: "idle" })

  async function verify() {
    setState({ kind: "running" })
    try {
      const res = await apiFetch(`/api/v1/admin/journal/verify?workspace_id=${encodeURIComponent(workspaceId)}`)
      const body = (await res.json().catch(() => null)) as (VerifyResult & { error?: string }) | null
      if (!res.ok || !body) {
        setState({ kind: "error", message: typeof body?.error === "string" ? body.error : `Request failed (${res.status})` })
        return
      }
      setState(body.ok
        ? { kind: "ok", count: body.count ?? 0, at: new Date() }
        : { kind: "broken", seq: body.broken_seq, reason: body.reason, at: new Date() })
    } catch {
      setState({ kind: "error", message: "The server could not be reached." })
    }
  }

  // The wireframe's 24-hour clock.
  const time = (d: Date) => d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false })
  const warn = state.kind === "broken" || state.kind === "error"
  const Icon = state.kind === "ok" ? ShieldCheck : warn ? ShieldAlert : ShieldQuestion
  const how = <>Every workspace event is mirrored into a hash-chained journal (HMAC-SHA256). <span className="font-mono">crewship journal verify</span></>

  return (
    <div
      data-slot="journal-verify"
      data-state={state.kind}
      role={state.kind === "broken" ? "alert" : "status"}
      className={cn("page-banner border border-border", warn ? "page-banner-warn" : "page-banner-info")}
    >
      <span data-slot="journal-verify-tile" aria-hidden className={cn("grid size-7 shrink-0 place-items-center rounded-lg", warn ? "bg-card text-chip-warn-fg" : "icon-tile")}>
        <Icon className="size-4" />
      </span>
      <span className="min-w-0 flex-1">
        {state.kind === "idle" && <><b className="block font-semibold">Journal chain</b><small className="block text-label text-muted-foreground">{how}</small></>}
        {state.kind === "running" && <><b className="block font-semibold">Verifying the journal chain…</b><small className="block text-label text-muted-foreground">Every entry is checked.</small></>}
        {state.kind === "ok" && <><b className="block font-semibold">Journal chain verified {time(state.at)}</b><small className="block text-label text-muted-foreground">{state.count.toLocaleString("en-US")} entries, none changed. {how}</small></>}
        {state.kind === "broken" && <><b className="font-semibold">Journal chain broken{state.seq ? ` at entry ${state.seq}` : ""}</b>{state.reason ? <> · {state.reason}</> : null}<span className="block text-label">Checked {time(state.at)}. Entries after it are still checked; see <span className="font-mono">crewship journal verify</span> for every break.</span></>}
        {state.kind === "error" && <><b className="font-semibold">Couldn’t verify the journal chain</b> · {state.message}</>}
      </span>
      <Button size="sm" variant="outline" onClick={() => void verify()} disabled={state.kind === "running"}>
        {state.kind === "running" && <Spinner className="h-3 w-3" />}
        Verify now
      </Button>
    </div>
  )
}
