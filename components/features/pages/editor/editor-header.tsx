"use client"

import * as React from "react"
import { ArrowLeft, Box } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard, Pill, StatStrip, type StatItem } from "@/components/ui/detail"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { formatDateTime } from "@/lib/time"
import { cn } from "@/lib/utils"
import type { PageCapabilities } from "@/lib/pages/editor-contract"
import { toPageFolderRef, toPanelState, worstPanelState } from "@/hooks/use-pages"
import { pagePanelCount, toPageOwner, type WirePageDetail } from "@/hooks/use-page-grants"
import { PAGE_STATE_META } from "@/components/features/pages/page-state"
import type { PanelState } from "@/components/features/pages/panels/types"
import { FolderGlyph } from "@/components/features/pages/folder-glyph"

/**
 * The editor's header: the same card an issue opens with.
 *
 * The first draft of the editor (#2515) put a `Back to page` button, a plain
 * heading and one line of status in a bar, then a rail of sections beside a
 * centred form. Next to an issue's detail it read as a settings wizard from
 * another product. This is the issue header, one for one — the icon box, the
 * name as the first thing on the page, an identity line under it, the state
 * as pills, and the way out top-right where `Start work` sits on an issue.
 *
 * What viewers see meanwhile is still said here, under the button: the whole
 * screen has just changed under the person, and the first question is whether
 * anything they touch is already live.
 */
export interface PageEditorHeaderProps {
  slug: string
  page: WirePageDetail | null
  capabilities: PageCapabilities
  /** The heading the shell focuses on the way in and on the way back. */
  headingRef: React.RefObject<HTMLHeadingElement | null>
  /** What viewers see while this person edits, in one sentence. */
  audience: string
  onBack: () => void
}

/**
 * The Page's freshness, as one state: what the list route sends as `state`,
 * or — the detail route sends the panels and not the summary — the worst of
 * the panels' own states, the way the rail ranks them. Null only when the
 * record says nothing either way, which is not the same as "never produced".
 */
export function pageFreshness(page: WirePageDetail | null): PanelState | null {
  const summary = toPanelState(page?.state)
  if (summary) return summary
  if (!Array.isArray(page?.panels)) return null
  return worstPanelState(page.panels.map((raw) => toPanelState(raw.state)))
}

export function PageEditorHeader({ slug, page, capabilities, headingRef, audience, onBack }: PageEditorHeaderProps) {
  const PageIcon = CONCEPT_ICON.pages
  const owner = toPageOwner(page)
  const folder = toPageFolderRef(page?.folder)
  const panelCount = pagePanelCount(page)
  const state = pageFreshness(page)
  const stateMeta = state ? PAGE_STATE_META[state] : null
  const StateIcon = stateMeta?.icon
  const publication =
    capabilities.hasApplication && typeof page?.publication_version === "number" && page.publication_version > 0
      ? page.publication_version
      : null

  return (
    <DetailCard data-testid="page-editor-header">
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex min-w-0 items-start gap-3">
            <div
              className={cn(
                "flex h-10 w-10 shrink-0 items-center justify-center rounded-xl border border-border/60",
                "bg-surface-raised",
              )}
            >
              <PageIcon className="h-5 w-5 text-muted-foreground" aria-hidden />
            </div>
            <div className="min-w-0">
              {/* Focus lands here on the way in, on returning from the preview
                  and after a discard. The ring is on `:focus-visible`, not
                  `:focus`: browsers carry the input modality across a scripted
                  focus, so a keyboard user sees it and a mouse user does not. */}
              <h1
                ref={headingRef}
                tabIndex={-1}
                className="break-words rounded-sm text-lg font-semibold tracking-tight outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
              >
                {page?.name?.trim() || slug}
              </h1>
              <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 type-page-meta text-muted-foreground">
                <span className="font-mono">{slug}</span>
                {page?.folder !== undefined && (
                  <>
                    <span aria-hidden>·</span>
                    <span className="inline-flex min-w-0 items-center gap-1">
                      {folder ? (
                        <>
                          <FolderGlyph icon={folder.icon} color={folder.color} className={cn("h-3 w-3", !folder.color && "text-muted-foreground-soft")} />
                          <span className="truncate">{folder.name}</span>
                        </>
                      ) : (
                        <span className="text-muted-foreground-soft">Unfiled</span>
                      )}
                    </span>
                  </>
                )}
                {owner && (
                  <>
                    <span aria-hidden>·</span>
                    {/* §7.1 rule 1: owner_user_id XOR owner_crew_id, and which
                        one it is changes what "the owner" means — a crew-owned
                        Page survives the person leaving. So the kind is said,
                        never trimmed off. */}
                    <span className="inline-flex items-center gap-1.5 rounded-full border border-border/60 px-2 py-0.5">
                      <span className="text-muted-foreground-soft">{owner.kind}</span>
                      <span className="font-medium text-foreground/85">{owner.label}</span>
                    </span>
                  </>
                )}
              </div>
            </div>
          </div>
          <div className="flex shrink-0 flex-col items-end gap-1.5">
            <Button type="button" variant="outline" size="sm" className="coarse:min-h-11" onClick={onBack}>
              <ArrowLeft className="h-3.5 w-3.5" aria-hidden />
              Back to page
            </Button>
            <span className="type-meta text-muted-foreground">{audience}</span>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          {capabilities.hasApplicationDraft && (
            <Pill tone="purple">
              <Box className="h-3 w-3" aria-hidden />
              Custom application
            </Pill>
          )}
          {publication !== null && <Pill tone="blue">Publication {publication}</Pill>}
          {stateMeta && (
            <Pill tone={stateMeta.pill}>
              {StateIcon && <StateIcon className="h-3 w-3" aria-hidden />}
              {stateMeta.label}
            </Pill>
          )}
          <Pill tone="default">
            {panelCount} {panelCount === 1 ? "panel" : "panels"}
          </Pill>
        </div>
      </div>
    </DetailCard>
  )
}

const DASH = "—"

function when(iso: string | null | undefined): string {
  return iso ? formatDateTime(iso) : DASH
}

/**
 * The derived facts — created, spec changed, last data, address, panels — as
 * one strip behind a disclosure, the way an issue keeps its dates.
 *
 * They used to be a "General" table inside the Content form, between the
 * description and the Save button, with the owner printed as a raw user id.
 * None of them is something the form writes, so none of them belongs in it.
 */
export function PageDetailsStrip({ slug, page }: { slug: string; page: WirePageDetail | null }) {
  const panelCount = pagePanelCount(page)
  const items: StatItem[] = [
    { label: "Created", value: when(page?.created_at) },
    // §10 defines updated_at as the SPEC's mtime — when the arrangement last
    // changed, not when data last arrived. The next item is the data.
    { label: "Spec changed", value: when(page?.updated_at) },
    { label: "Last data", value: when(page?.last_produced_at) },
    { label: "Address", value: `/pages/${slug}`, mono: true },
    { label: "Panels", value: panelCount },
  ]
  return (
    <details data-slot="page-details">
      <summary className="cursor-pointer text-xs text-muted-foreground">Dates and page details</summary>
      <StatStrip items={items} className="mt-2" />
    </details>
  )
}
