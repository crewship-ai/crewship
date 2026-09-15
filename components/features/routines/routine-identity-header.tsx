"use client"

import { useEffect, useState, type ReactNode, type Ref } from "react"
import Link from "next/link"
import { Pencil, Upload } from "lucide-react"
import { Button } from "@/components/ui/button"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { CrewIcon } from "@/components/ui/crew-icon"
import { useWorkspaceAgentDirectory } from "@/hooks/use-workspace-agent-directory"
import { useSessionSafe } from "@/hooks/use-auth"
import { RoutineAgentLink } from "./routine-agent-link"
import { DetailCard, Pill } from "@/components/ui/detail"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { isRoutineTestFixture } from "@/lib/routine-filters"
import { useAbilities } from "@/hooks/use-abilities"
import { roleAtLeast } from "@/lib/routine-governance"
import { relTime } from "@/lib/time"
import type { RoutineDetail } from "./routines-detail-panel"

export type RoutineIdentity = Pick<RoutineDetail, "slug" | "name" | "description" | "icon" | "color" | "head_version" | "manifest"> &
  Partial<Pick<RoutineDetail, "draft">>

/** "you" for the viewer's own id, the raw id otherwise — the server does no lookup. */
export function draftAuthorLabel(updatedBy: string | undefined, viewerId: string | undefined): string {
  if (!updatedBy) return "unknown"
  return viewerId && updatedBy === viewerId ? "you" : updatedBy
}

/**
 * One identity card, shared by the routine page and every entry into its run
 * history.
 *
 * The state line answers the two authoring questions in one sentence:
 * "is it published or only saved?" and "what will Run start?" — `Published v3
 * · Draft r2 · you · 12 min ago · Run uses v3`. The draft comes from the
 * detail's `draft` field; without one the line is the published version alone.
 */
export function RoutineIdentityHeader({
  routine,
  workspaceId,
  onChanged,
  onEdit,
  onPublish,
  editButtonRef,
  primary,
  actions,
  runUses = false,
  children,
}: {
  routine: RoutineIdentity
  workspaceId: string
  onChanged?: () => void
  onEdit?: () => void
  /** Opens the publish review; rendered only when the routine has a draft. */
  onPublish?: () => void
  editButtonRef?: Ref<HTMLButtonElement>
  /** The primary verb (Run), first in the action row. */
  primary?: ReactNode
  /** Everything after Edit — the ⋯ menu. */
  actions?: ReactNode
  /** Say what Run starts ("Run uses v3"); off on a run page, which states its own version. */
  runUses?: boolean
  children?: ReactNode
}) {
  const { role } = useAbilities()
  const { data: session } = useSessionSafe()
  const { agents } = useWorkspaceAgentDirectory(workspaceId)
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
  const manager = roleAtLeast(role, "MANAGER")
  const published = routine.head_version != null && routine.head_version > 0
  const draft = routine.draft
  return <DetailCard><div className="space-y-3">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="flex min-w-0 items-start gap-3">
        {manager ? <div aria-busy={saving} className={saving ? "pointer-events-none opacity-70" : ""}><CrewIconPopover {...appearance} size="lg" onIconChange={icon => void save({ icon })} onColorChange={color => void save({ color })} /></div> : <CrewIcon {...appearance} size="lg" />}
        <div className="min-w-0"><h1 className="break-words text-lg font-semibold tracking-tight">{routine.name || routine.slug}</h1><div data-testid="routine-state-line" className="mt-1 flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
          {published ? <Pill tone="success">Published v{routine.head_version}</Pill> : <Pill tone="default">Not published</Pill>}
          {draft && <Pill tone="purple" data-testid="routine-draft-pill">Draft r{draft.revision} · {draftAuthorLabel(draft.updated_by, session?.user?.id)} · {relTime(draft.updated_at)}</Pill>}
          {runUses && (published
            ? <span>Run uses <b className="font-medium text-foreground/85">v{routine.head_version}</b></span>
            : <span>Run uses <b className="font-medium text-foreground/85">nothing yet</b> — publish first</span>)}
          {agent && <RoutineAgentLink slug={agent} agent={agents?.find(a => a.slug === agent)} workspaceId={workspaceId} />}
        </div></div>
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        {primary}
        {manager && draft && onPublish && <Button variant="outline" size="sm" onClick={onPublish} data-testid="routine-publish-button"><Upload className="mr-1.5 h-3.5 w-3.5" />Publish draft r{draft.revision}</Button>}
        {manager && (onEdit ? <Button ref={editButtonRef} variant="outline" size="sm" onClick={onEdit}><Pencil className="mr-1.5 h-3.5 w-3.5" />Edit</Button> : <Button variant="outline" size="sm" asChild><Link href={`/routines?${new URLSearchParams({ slug: routine.slug, view: "edit" })}`}><Pencil className="mr-1.5 h-3.5 w-3.5" />Edit</Link></Button>)}
        {actions}
      </div>
    </div>
    {routine.description && <p className="max-w-[80ch] text-[13px] leading-relaxed text-foreground/85">{routine.description}</p>}
    {isRoutineTestFixture(routine.slug) && <p className="rounded-lg border border-warn/20 bg-warn/10 px-3 py-2 text-xs text-warn">Test recipe · release verification, not a client example. Its inputs may be technical test parameters.</p>}
    {children && <div className="flex flex-wrap items-center gap-1.5">{children}</div>}
  </div></DetailCard>
}
