"use client"

/**
 * The Vault's bulk actions: Delete (owners and admins) and Change tier.
 *
 * Both act on the rows the person can SEE and has ticked — the page passes the
 * visible selection, never a tick made under an earlier filter. Delete says
 * what breaks before it runs: the routines that reference the secrets and the
 * agents that hold them. A failed delete keeps the dialog open and the failed
 * rows selected.
 */

import * as React from "react"
import { ShieldCheck } from "lucide-react"

import { ConfirmDialog, type Consequence } from "@/components/ui/confirm-dialog"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"
import { CREDENTIAL_TIERS } from "@/lib/credentials/tiers"
import { toast } from "@/lib/toast"

export interface BulkTarget {
  id: string
  name: string
  agent_names?: string[] | null
}

interface Dependents {
  routines: { slug: string; name: string }[]
  failed: number
}

/** PATCH or DELETE each id; 404 on delete counts as done (someone else got there first). */
async function eachId(ids: string[], run: (id: string) => Promise<Response>, okOn404 = false): Promise<string[]> {
  const results = await Promise.allSettled(ids.map(run))
  return ids.filter((_, i) => {
    const r = results[i]
    if (r.status === "rejected") return true
    if (okOn404 && r.value.status === 404) return false
    return !r.value.ok
  })
}

export function BulkDeleteDialog({
  open,
  onOpenChange,
  workspaceId,
  targets,
  onDone,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  targets: BulkTarget[]
  /** Called after the attempt with the ids that could not be deleted. */
  onDone: (failedIds: string[]) => void
}) {
  const [deps, setDeps] = React.useState<Dependents | null>(null)
  const key = targets.map((t) => t.id).join(",")

  React.useEffect(() => {
    if (!open || targets.length === 0) return
    let cancelled = false
    setDeps(null)
    const ws = encodeURIComponent(workspaceId)
    Promise.all(
      targets.map((t) =>
        apiFetch(`/api/v1/credentials/${encodeURIComponent(t.id)}/dependents?workspace_id=${ws}`)
          .then(async (r) => {
            if (!r.ok) throw new Error()
            const body = (await r.json()) as { routines?: { slug: string; name: string }[] }
            if (!Array.isArray(body?.routines)) throw new Error()
            return body.routines
          })
          .catch(() => null),
      ),
    ).then((lists) => {
      if (cancelled) return
      const bySlug = new Map<string, { slug: string; name: string }>()
      let failed = 0
      for (const l of lists) {
        if (!l) { failed++; continue }
        for (const r of l) bySlug.set(r.slug, r)
      }
      setDeps({ routines: [...bySlug.values()], failed })
    })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- keyed on the ids
  }, [open, key, workspaceId])

  const n = targets.length
  const agents = Array.from(new Set(targets.flatMap((t) => t.agent_names ?? [])))
  const list = (names: string[]) => (names.length > 4 ? `${names.slice(0, 4).join(", ")} and ${names.length - 4} more` : names.join(", "))

  const consequences: Consequence[] = [
    { tone: "lost", text: `${n === 1 ? targets[0].name : `${n} secrets`} ${n === 1 ? "is" : "are"} deleted for good. This cannot be undone.` },
  ]
  if (agents.length > 0) {
    consequences.push({ tone: "warn", text: `${agents.length} agent${agents.length === 1 ? "" : "s"} lose access at the next run: ${list(agents)}.` })
  }
  if (!deps) consequences.push({ tone: "warn", text: "Checking which routines use them…" })
  else {
    if (deps.routines.length > 0) {
      consequences.push({ tone: "warn", text: `${deps.routines.length} routine${deps.routines.length === 1 ? "" : "s"} reference ${n === 1 ? "it" : "them"} and may fail: ${list(deps.routines.map((r) => r.name || r.slug))}.` })
    } else if (deps.failed === 0) {
      consequences.push({ tone: "kept", text: "No visible routine references them." })
    }
    if (deps.failed > 0) consequences.push({ tone: "warn", text: `Routine use could not be checked for ${deps.failed} of them.` })
  }

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={n === 1 ? `Delete ${targets[0]?.name ?? "secret"}?` : `Delete ${n} secrets?`}
      consequences={consequences}
      confirmLabel={n === 1 ? "Delete" : `Delete ${n}`}
      destructive
      onConfirm={async () => {
        const ws = encodeURIComponent(workspaceId)
        const failed = await eachId(
          targets.map((t) => t.id),
          (id) => apiFetch(`/api/v1/credentials/${encodeURIComponent(id)}?workspace_id=${ws}`, { method: "DELETE" }),
          true,
        )
        onDone(failed)
        const done = n - failed.length
        if (failed.length === 0) {
          toast.success(`${done} secret${done === 1 ? "" : "s"} deleted`)
          return
        }
        toast.error(`${done} deleted, ${failed.length} could not be deleted`, {
          description: "The ones that failed stay selected so you can try again.",
        })
        // Throwing keeps the dialog open on the rows that are left.
        throw new Error("partial")
      }}
    />
  )
}

export function BulkTierDialog({
  open,
  onOpenChange,
  workspaceId,
  targets,
  onDone,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  targets: BulkTarget[]
  onDone: (failedIds: string[]) => void
}) {
  const [tier, setTier] = React.useState<string>("2")
  const [busy, setBusy] = React.useState(false)
  React.useEffect(() => { if (open) setTier("2") }, [open])
  const meta = CREDENTIAL_TIERS.find((t) => String(t.level) === tier)

  const save = async () => {
    if (busy) return
    setBusy(true)
    try {
      const ws = encodeURIComponent(workspaceId)
      const failed = await eachId(targets.map((t) => t.id), (id) =>
        apiFetch(`/api/v1/credentials/${encodeURIComponent(id)}?workspace_id=${ws}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ security_level: Number(tier) }),
        }),
      )
      onDone(failed)
      const done = targets.length - failed.length
      if (failed.length === 0) {
        toast.success(`Tier changed to L${tier} for ${done} secret${done === 1 ? "" : "s"}`)
        onOpenChange(false)
      } else {
        toast.error(`Tier changed for ${done}, ${failed.length} could not be changed`, { description: "The ones that failed stay selected." })
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface open={open} onOpenChange={(o) => { if (!busy) onOpenChange(o) }} size="sm" onSubmit={save}>
      <CreateSurfaceHeader icon={ShieldCheck} concept="credentials" title="Change tier"
        description={`For ${targets.length} selected secret${targets.length === 1 ? "" : "s"}. Keeper applies it from the next read.`}
        onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody>
        <CreateSurfaceChoice
          ariaLabel="Keeper tier"
          value={tier}
          onChange={setTier}
          options={CREDENTIAL_TIERS.map((t) => ({ value: String(t.level), label: t.short, hint: t.consequence }))}
        />
        {meta && <p className="mt-2 text-label text-muted-foreground">{meta.consequence}</p>}
      </CreateSurfaceBody>
      <CreateSurfaceFooter onCancel={() => onOpenChange(false)} primaryLabel={`Set L${tier}`} onPrimary={save} busy={busy} guardCancel={false} />
    </CreateSurface>
  )
}
