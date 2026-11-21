"use client"

import * as React from "react"
import { Activity, ShieldCheck, TriangleAlert } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { readApiError } from "@/lib/api-error"
import { useShowMore } from "@/hooks/use-show-more"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { ShowMore } from "@/components/ui/show-more"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { ago } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { displayName, type Person, type Workspace } from "./people-model"
import { roleLabel } from "./people-parts"

/** One row of GET /api/v1/admin/instance/audit. */
export interface AuditRow {
  id: string
  user_id: string | null
  user_email: string | null
  action: string
  entity_type: string
  entity_id: string | null
  target_workspace_id: string | null
  metadata: string
  created_at: string
}

/** GET /api/v1/admin/instance/audit/verify */
export interface AuditVerify {
  ok: boolean
  count?: number
  broken_seq?: number
  reason?: string
  historical_status?: string
}

const AUDIT = "/api/v1/admin/instance/audit"

function meta(raw: string): Record<string, unknown> {
  try {
    const v = JSON.parse(raw)
    return v && typeof v === "object" ? (v as Record<string, unknown>) : {}
  } catch {
    return {}
  }
}

const str = (v: unknown) => (typeof v === "string" ? v : "")
const change = (v: unknown): { from: string; to: string } | null =>
  v && typeof v === "object" ? { from: str((v as { from?: unknown }).from), to: str((v as { to?: unknown }).to) } : null

/** The sentence for one entry, in the words the page uses. */
export function describeAudit(row: AuditRow, people: Person[], workspaces: Workspace[]): string {
  const m = meta(row.metadata)
  const who = (id: string | null | undefined) => {
    const p = id ? people.find((x) => x.id === id) : undefined
    return p ? displayName(p) : "someone"
  }
  const ws = (id: string | null | undefined) => workspaces.find((w) => w.id === id)?.name ?? "a workspace"
  const person = row.entity_type === "user" || row.entity_type === "workspace_member" ? who(row.entity_id) : "someone"
  const role = str(m.role) ? roleLabel(str(m.role)) : ""
  const prev = str(m.previous_role) ? roleLabel(str(m.previous_role)) : ""
  switch (row.action.replace(/^instance\./, "")) {
    case "user_created": return `Created the account of ${person}`
    case "member_added": return `${person} added to ${ws(row.target_workspace_id)}${role ? ` as ${role}` : ""}`
    case "member_role_changed": return `${person} changed${prev ? ` from ${prev}` : ""}${role ? ` to ${role}` : ""} in ${ws(row.target_workspace_id)}`
    case "member_removed": return `${person} removed from ${ws(row.target_workspace_id)}`
    case "setup_link_issued": return `Issued a setup link for ${person}`
    case "setup_link_revoked": return `Voided the setup link of ${person}`
    case "user_suspended": return `Suspended ${person}${str(m.reason) ? ` · “${str(m.reason)}”` : ""}`
    case "user_reactivated": return `Reactivated ${person}`
    case "admin_granted": return `Made ${person} an instance admin`
    case "admin_revoked": return `Removed ${person} as an instance admin`
    case "workspace_created": return `Created the workspace ${str(m.name) || ws(row.entity_id)}`
    case "workspace_deleted": return "Deleted the workspace"
    case "workspace_ownership_transferred": return `Ownership of ${ws(row.entity_id)} handed to ${who(str(m.new_owner_user_id))}`
    case "workspace_updated": {
      const name = change(m.name)
      const kind = change(m.kind)
      const parts = [
        name && `Renamed from “${name.from}” to “${name.to}”`,
        kind && `Changed from ${kind.from === "personal" ? "a personal account" : "a workspace"} to ${kind.to === "personal" ? "a personal account" : "a workspace"}`,
      ].filter(Boolean)
      return parts.join(" · ") || "Changed the workspace"
    }
    default: {
      const words = row.action.replace(/^instance\./, "").replace(/_/g, " ")
      return words.charAt(0).toUpperCase() + words.slice(1)
    }
  }
}

/**
 * "Admin actions on this account / workspace": what instance admins changed,
 * newest first, from the instance audit log, with a button that walks the
 * whole chain. The log is read per entity id: a person's id finds the actions
 * on the account and on their memberships, a workspace's id everything done
 * to it.
 */
export function AdminAuditCard({ entityId, title, description, people, workspaces }: {
  entityId: string
  title: string
  description: string
  people: Person[]
  workspaces: Workspace[]
}) {
  const [rows, setRows] = React.useState<AuditRow[] | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const [check, setCheck] = React.useState<{ state: "idle" | "running" | "done" | "failed"; result?: AuditVerify; error?: string }>({ state: "idle" })

  React.useEffect(() => {
    let live = true
    setRows(null)
    setError(null)
    setCheck({ state: "idle" })
    ;(async () => {
      try {
        const res = await apiFetch(`${AUDIT}?entity_id=${encodeURIComponent(entityId)}&limit=100`)
        if (!res.ok) { if (live) setError(await readApiError(res, "The admin actions could not be read")); return }
        const data = (await res.json()) as AuditRow[]
        // The server answers newest first; sorting again keeps it so.
        if (live) setRows([...data].sort((a, b) => b.created_at.localeCompare(a.created_at)))
      } catch {
        if (live) setError("The admin actions could not be read")
      }
    })()
    return () => { live = false }
  }, [entityId])

  const verify = async () => {
    setCheck({ state: "running" })
    try {
      const res = await apiFetch(`${AUDIT}/verify`)
      if (!res.ok) { setCheck({ state: "failed", error: await readApiError(res, "The log could not be checked") }); return }
      setCheck({ state: "done", result: (await res.json()) as AuditVerify })
    } catch {
      setCheck({ state: "failed", error: "The log could not be checked" })
    }
  }

  const page = useShowMore(rows ?? [], { step: 10, resetKey: entityId })
  const intact = check.state === "done" && check.result?.ok
  const broken = check.state === "done" && !check.result?.ok

  return (
    <SettingsCard icon={Activity} tint="var(--muted-foreground)" title={title} description={description}
      footer={
        <>
          <span className="inline-flex items-center gap-1.5" role="status">
            {broken ? <TriangleAlert className="size-3 text-destructive" aria-hidden /> : <ShieldCheck className="size-3" aria-hidden />}
            {check.state === "idle" && "Not checked in this session"}
            {check.state === "running" && "Checking…"}
            {intact && "Chain intact"}
            {broken && `Chain broken${check.result?.broken_seq ? ` at entry ${check.result.broken_seq}` : ""}${check.result?.reason ? ` · ${check.result.reason}` : ""}`}
            {check.state === "failed" && check.error}
          </span>
          <Button size="sm" variant="ghost" onClick={() => void verify()} disabled={check.state === "running"}>Check now</Button>
        </>
      }>
      {error ? (
        <p className="px-4 py-3 text-label text-destructive">{error}</p>
      ) : rows === null ? (
        <div className="grid gap-2 p-4"><Skeleton className="h-10 w-full" /><Skeleton className="h-10 w-full" /></div>
      ) : rows.length === 0 ? (
        <p className="px-4 py-3 text-label text-muted-foreground">No admin actions recorded.</p>
      ) : (
        <>
          {page.shown.map((r) => {
            const actor = people.find((p) => p.id === r.user_id)
            return (
              <SettingsRow key={r.id} label={describeAudit(r, people, workspaces)}
                description={`by ${actor ? displayName(actor) : r.user_email ?? "the system"}`}>
                <span className="text-label text-muted-foreground" title={new Date(r.created_at).toLocaleString()}>{ago(r.created_at)}</span>
              </SettingsRow>
            )
          })}
          <ShowMore shown={page.shown.length} total={page.total} nextCount={page.nextCount} paged={page.paged} onMore={page.showMore} noun="actions" />
        </>
      )}
    </SettingsCard>
  )
}
