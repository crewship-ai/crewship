"use client"

import { useEffect, useState, type ReactNode } from "react"
import Link from "next/link"
import { Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { CrewIcon } from "@/components/ui/crew-icon"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { DetailCard } from "@/components/ui/detail"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { isRoutineTestFixture } from "@/lib/routine-filters"
import { useAbilities } from "@/hooks/use-abilities"
import { roleAtLeast } from "@/lib/routine-governance"
import type { RoutineDetail } from "./routines-detail-panel"

export type RoutineIdentity = Pick<RoutineDetail, "slug" | "name" | "description" | "icon" | "color" | "head_version" | "manifest">

/** One identity card, shared by the recipe and every entry into its run history. */
export function RoutineIdentityHeader({ routine, workspaceId, onChanged, onEdit, actions, children }: {
  routine: RoutineIdentity; workspaceId: string; onChanged?: () => void; onEdit?: () => void; actions?: ReactNode; children?: ReactNode
}) {
  const { role } = useAbilities()
  const [appearance, setAppearance] = useState(() => ({ icon: resolveRoutineIcon(routine), color: resolveRoutineColor(routine) }))
  const [saving, setSaving] = useState(false)
  useEffect(() => setAppearance({ icon: resolveRoutineIcon(routine), color: resolveRoutineColor(routine) }), [routine.slug, routine.icon, routine.color]) // eslint-disable-line react-hooks/exhaustive-deps
  const save = async (next: Partial<typeof appearance>) => {
    if (saving) return
    const previous = appearance
    setSaving(true); setAppearance({ ...appearance, ...next })
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(routine.slug)}/appearance`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(next) })
      if (!res.ok) throw new Error("save")
      onChanged?.()
    } catch { setAppearance(previous); toast.error("Could not save the icon") }
    finally { setSaving(false) }
  }
  const agent = routine.manifest?.agents?.[0]
  return <DetailCard><div className="space-y-3">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="flex min-w-0 items-start gap-3">
        {roleAtLeast(role, "MANAGER") ? <div aria-busy={saving} className={saving ? "pointer-events-none opacity-70" : ""}><CrewIconPopover {...appearance} size="lg" onIconChange={icon => void save({ icon })} onColorChange={color => void save({ color })} /></div> : <CrewIcon {...appearance} size="lg" />}
        <div className="min-w-0"><h1 className="break-words text-lg font-semibold tracking-tight">{routine.name || routine.slug}</h1><div className="mt-1 flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
          {routine.head_version != null && <span>Current recipe · v{routine.head_version}</span>}
          {agent && <Link href="/crews" className="inline-flex items-center gap-1.5 rounded-full border border-border/60 py-0.5 pl-0.5 pr-2 hover:text-foreground"><AgentAvatar seed={agent} className="h-4 w-4" alt="" />{agent}</Link>}
        </div></div>
      </div>
      <div className="flex flex-wrap items-center gap-1.5">{roleAtLeast(role, "MANAGER") && (onEdit ? <Button variant="outline" size="sm" onClick={onEdit}><Pencil className="mr-1.5 h-3.5 w-3.5" />Edit</Button> : <Button variant="outline" size="sm" asChild><Link href={`/routines?${new URLSearchParams({ slug: routine.slug, view: "edit" })}`}><Pencil className="mr-1.5 h-3.5 w-3.5" />Edit</Link></Button>)}{actions}</div>
    </div>
    {routine.description && <p className="max-w-[80ch] text-[13px] leading-relaxed text-foreground/85">{routine.description}</p>}
    {isRoutineTestFixture(routine.slug) && <p className="rounded-lg border border-warn/20 bg-warn/10 px-3 py-2 text-xs text-warn">Test recipe · release verification, not a client example. Its inputs may be technical test parameters.</p>}
    {children && <div className="flex flex-wrap items-center gap-1.5">{children}</div>}
  </div></DetailCard>
}
