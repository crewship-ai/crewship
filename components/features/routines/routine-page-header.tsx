"use client"

// The routine page's header (Harbor 2 RT-PAGE): the routine's glyph, its
// name, which crew and agent it runs as, and its state in plain words —
// "Live", "Not live yet", "Changes not live". Version and draft numbers are
// Full detail. Below it the draft bar (changes waiting to go live) and the tab
// row with "Runs and history" and the Simple / Full detail switch.

import * as React from "react"
import Link from "next/link"
import { Activity, CheckCircle2, ChevronRight, Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { CrewIcon } from "@/components/ui/crew-icon"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { useWorkspaceAgentDirectory } from "@/hooks/use-workspace-agent-directory"
import { useSessionSafe } from "@/hooks/use-auth"
import { relTime } from "@/lib/time"
import { cn } from "@/lib/utils"
import { isRoutineTestFixture } from "@/lib/routine-filters"
import {
  TAB_LABEL,
  routineActivityHref,
  routineStateBadges,
  visibleTabs,
  type DetailDepth,
  type RoutineTab,
} from "@/lib/routine-page"
import { RoutineAgentLink } from "./routine-agent-link"
import { draftAuthorLabel } from "./routine-identity-header"
import type { RoutineDetail } from "./routines-detail-panel"

export function RoutinePageHeader({
  routine,
  workspaceId,
  crewName,
  depth,
  appearance,
  canEditAppearance,
  onAppearance,
  onVersions,
  pills,
}: {
  routine: RoutineDetail
  workspaceId: string
  crewName?: string
  depth: DetailDepth
  appearance: { icon: string; color: string }
  canEditAppearance: boolean
  onAppearance: (next: Partial<{ icon: string; color: string }>) => void
  /** Live and Changes not live open the version history. */
  onVersions: () => void
  /** Lifecycle and last-run pills, computed by the panel. */
  pills?: React.ReactNode
}) {
  const { agents } = useWorkspaceAgentDirectory(workspaceId)
  const { live, changes } = routineStateBadges(
    { headVersion: routine.head_version, draft: routine.draft, draftOnly: routine.draft_only },
    depth,
  )
  const agent = routine.manifest?.agents?.[0]
  const published = live.tone === "success"
  return (
    <header className="flex flex-col gap-3" data-testid="routine-page-header">
      <div className="flex min-w-0 items-start gap-3">
        {canEditAppearance ? (
          <CrewIconPopover {...appearance} size="lg" ariaLabel="Change icon and colour" onIconChange={(icon) => onAppearance({ icon })} onColorChange={(color) => onAppearance({ color })} />
        ) : (
          <CrewIcon {...appearance} size="lg" />
        )}
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <h2 className="break-words text-xl font-semibold tracking-tight">{routine.name || routine.slug}</h2>
          <p className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-muted-foreground" data-testid="routine-meta-line">
            {crewName && <span>{crewName} crew</span>}
            {crewName && agent && <span aria-hidden>·</span>}
            {agent && (
              <span className="inline-flex items-center gap-1.5">
                runs as <RoutineAgentLink slug={agent} agent={agents?.find((a) => a.slug === agent)} />
              </span>
            )}
            {depth === "full" && (
              <>
                <span aria-hidden>·</span>
                <span className="font-mono">{routine.slug}</span>
              </>
            )}
          </p>
          <div className="flex flex-wrap items-center gap-1.5" data-testid="routine-state-line">
            {published ? (
              <button type="button" onClick={onVersions} title="Version history" className="rounded-full focus-visible:outline focus-visible:outline-primary">
                <StatusPill size="md" tone="success" icon={<CheckCircle2 className="h-3 w-3" aria-hidden />} label={live.label} />
              </button>
            ) : (
              <StatusPill size="md" tone="muted" label={live.label} />
            )}
            {changes && (
              <button type="button" onClick={onVersions} title="Changes saved as a draft" className="rounded-full focus-visible:outline focus-visible:outline-primary" data-testid="routine-draft-pill">
                <StatusPill size="md" tone="purple" icon={<Pencil className="h-3 w-3" aria-hidden />} label={changes.label} />
              </button>
            )}
            {pills}
          </div>
        </div>
      </div>
      {routine.description && <p className="max-w-prose text-sm leading-relaxed text-foreground/85">{routine.description}</p>}
      {isRoutineTestFixture(routine.slug) && (
        <p className="rounded-lg border border-warn/20 bg-warn/10 px-3 py-2 text-xs text-warn">
          Test recipe · release verification, not a client example. Its inputs may be technical test parameters.
        </p>
      )}
    </header>
  )
}

/** "N changes are not live yet" — Discard · Test changes · Publish changes. */
export function RoutineDraftBar({
  routine,
  canEdit,
  testing,
  onDiscard,
  onTest,
  onPublish,
}: {
  routine: RoutineDetail
  canEdit: boolean
  testing?: boolean
  onDiscard: () => void
  onTest: () => void
  onPublish: () => void
}) {
  const { data: session } = useSessionSafe()
  const draft = routine.draft
  if (!draft) return null
  const draftOnly = !!routine.draft_only || !(routine.head_version && routine.head_version > 0)
  const who = draftAuthorLabel(draft.updated_by, session?.user?.id)
  return (
    <section
      aria-label={draftOnly ? "Not live yet" : "Changes not live"}
      data-testid="routine-draft-bar"
      className="flex flex-col gap-3 rounded-lg border border-purple/25 bg-purple/[0.06] px-4 py-3 sm:flex-row sm:items-center"
    >
      <Pencil className="hidden h-4 w-4 shrink-0 text-purple sm:block" aria-hidden />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{draftOnly ? "This routine is not live yet" : "Changes are not live yet"}</p>
        <p className="text-xs text-muted-foreground">
          {draftOnly
            ? `Saved as a draft by ${who} ${relTime(draft.updated_at)}. Nothing can run until it is published. Test it, then publish it.`
            : `Edited by ${who} ${relTime(draft.updated_at)}; runs still use the live version. Test the changes, then publish them.`}
        </p>
      </div>
      {canEdit && (
        <div className="flex shrink-0 flex-wrap items-center gap-1.5">
          <Button type="button" variant="ghost" size="sm" onClick={onDiscard} className="coarse:h-11">
            Discard
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={onTest} disabled={testing} className="coarse:h-11">
            {testing ? "Testing…" : draftOnly ? "Test draft" : "Test changes"}
          </Button>
          <Button type="button" size="sm" onClick={onPublish} className="coarse:h-11" data-testid="routine-publish-button">
            {draftOnly ? "Publish" : "Publish changes"}
          </Button>
        </div>
      )}
    </section>
  )
}

/** Overview · Steps · Triggers N · (Versions) · Settings, Runs and history, Simple | Full detail. */
export function RoutineTabRow({
  slug,
  tab,
  depth,
  triggers,
  onTab,
  onDepth,
}: {
  slug: string
  tab: RoutineTab
  depth: DetailDepth
  triggers?: number
  onTab: (tab: RoutineTab) => void
  onDepth: (depth: DetailDepth) => void
}) {
  const tabs = visibleTabs(depth, tab)
  return (
    <div className="flex flex-col gap-2 border-b border-hairline sm:flex-row sm:items-end">
      {/* On a phone the switch and the Activity link sit above the tabs. */}
      <div className="order-2 -mb-px flex min-w-0 flex-1 overflow-x-auto [scrollbar-width:none] sm:order-1" role="tablist" aria-label="Routine">
        {tabs.map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            onClick={() => onTab(t)}
            className={cn(
              "flex h-9 shrink-0 items-center gap-1.5 border-b-2 px-3 text-sm transition-colors coarse:h-11",
              tab === t ? "border-primary-hover font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {TAB_LABEL[t]}
            {t === "triggers" && triggers != null && <span className="text-xs text-muted-foreground">{triggers}</span>}
          </button>
        ))}
      </div>
      <div className="order-1 flex shrink-0 items-center justify-between gap-2 pb-1.5 sm:order-2">
        <Button asChild variant="ghost" size="sm" className="gap-1.5 text-xs coarse:h-11">
          <Link href={routineActivityHref(slug)} title="Every run and change of this routine, in Activity">
            <Activity className="h-3.5 w-3.5" aria-hidden />
            Runs and history
            <ChevronRight className="h-3 w-3" aria-hidden />
          </Link>
        </Button>
        <div
          role="radiogroup"
          aria-label="Detail"
          title="Simple hides ids, retries, limits and code. Your choice is remembered."
          className="flex rounded-md border border-hairline p-0.5"
        >
          {(["simple", "full"] as const).map((d) => (
            <button
              key={d}
              type="button"
              role="radio"
              aria-checked={depth === d}
              onClick={() => onDepth(d)}
              className={cn(
                "h-7 rounded px-2.5 text-xs transition-colors coarse:h-10",
                depth === d ? "bg-foreground/[0.08] font-medium text-foreground" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {d === "simple" ? "Simple" : "Full detail"}
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
