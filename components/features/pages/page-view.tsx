"use client"

/**
 * One page — the panel grid (PRD §9).
 *
 * | Concern | Choice                                                      |
 * |---------|-------------------------------------------------------------|
 * | Layout  | CSS Grid, `col-span-n` from the spec's `span`                |
 * | Reflow  | `@container` queries on the panel's own box, not the viewport|
 * | Mobile  | single column below the tablet breakpoint                    |
 * | Dispatch| `PanelRenderer` — validate, look up the closed enum, render   |
 *
 * Cost: 0 KB. No chart library, no grid library, no new dependency.
 *
 * Two details that are easy to get wrong and are deliberate here:
 *
 *  · **The span classes are static strings.** `col-span-${n}` is invisible to
 *    Tailwind's scanner and would ship a grid with no spans at all.
 *
 *  · **The single-column case is `grid-cols-1` plus `md:col-span-n`, never a
 *    bare `col-span-n`.** In a one-track grid, `grid-column: span 8` does not
 *    clamp — it creates seven implicit columns and the page scrolls sideways
 *    on a phone, which is the opposite of "mobile and tablet first".
 *
 * ## Tabs (epic #1935)
 *
 * A page may carry a bar of tabs under the breadcrumb, one screen at a time
 * instead of one long scroll. The bar, the grouping rule and everything that
 * follows from tabs HIDING panels live in `page-tabs.tsx`; what this file owes
 * the feature is one property, and it is the important one: **the header's
 * freshness summary is computed over every panel on the page, never over the
 * visible tab.** It must not move when the tab does.
 *
 * Each cell is a named `@container/panel`, which is what the panels' own
 * `@md/panel:` rules resolve against: a panel in a `span: 4` cell collapses
 * its table to a card list while the `span: 12` panel beside it keeps the
 * table, at one viewport width. That is the whole point of §9's container
 * queries.
 *
 * ## Liveness (epic #1935)
 *
 * A pulse HERE means one thing: *this panel's data just changed*. It is not a
 * heartbeat — a panel that pulses permanently looks identical whether the last
 * payload landed a second or an hour ago, and it is the kind of motion a
 * reader stops seeing within a day. Silence therefore carries information too:
 * a producer that dies simply stops flashing, and its panel greys itself out
 * on its own once the server's verdict crosses the SLA.
 *
 * Three refusals keep the flash from claiming more than the freshness verdict
 * does (§4 is the whole reason this product exists):
 *
 *  · Only a `fresh` panel flashes. A `stale`, `failed` or never-produced panel
 *    is silent no matter what arrives — motion must never imply a liveness the
 *    verdict disagrees with.
 *  · Only a CHANGED payload flashes. The same bytes arriving again is not news;
 *    it is a producer repeating itself, and re-flashing would turn the cue into
 *    the heartbeat it exists not to be.
 *  · An EMPTY payload never flashes. A panel drawing the em dash has received
 *    nothing worth celebrating (§9b.4).
 *
 * The first payload a panel is ever seen with is a baseline, not an arrival —
 * otherwise every panel flashes on load and the cue means "you opened a page".
 *
 * Cost: 0 KB. `motion` is 45 KB gzip for what is one CSS animation, and §9's
 * rendering budget is ~0 KB of new weight (animation is CSS-only there in as
 * many words). The keyframes are declared BELOW rather than in
 * `app/globals.css`, so the whole cue — the rule, its duration and its
 * reduced-motion fallback — reads in one place.
 */

import * as React from "react"
import { ChevronLeft, ChevronRight } from "lucide-react"

import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"
import { PanelRenderer } from "@/components/features/pages/panels"
import {
  PanelActionsProvider,
  useHiddenPanels,
  type PageAction,
} from "@/components/features/pages/panels/panel-actions"
import { PAGE_STATE_META } from "@/components/features/pages/page-state"
import { LiveIndicator, type PageLiveness } from "@/components/features/pages/live-indicator"
import {
  PageTabGroup,
  PageTabs,
  PageTabsStyles,
  usePageTabState,
} from "@/components/features/pages/page-tabs"
import { derivePageTabs, type PageTabView } from "@/hooks/use-pages"
import type { PageView as PageRecord, PanelView } from "@/hooks/use-pages"

/**
 * `span` → grid class. A literal map, because Tailwind reads source text: a
 * template literal here produces a page whose every panel is full width.
 */
const SPAN_CLASS: Record<number, string> = {
  1: "md:col-span-1",
  2: "md:col-span-2",
  3: "md:col-span-3",
  4: "md:col-span-4",
  5: "md:col-span-5",
  6: "md:col-span-6",
  7: "md:col-span-7",
  8: "md:col-span-8",
  9: "md:col-span-9",
  10: "md:col-span-10",
  11: "md:col-span-11",
  12: "md:col-span-12",
}

export function spanClass(span: number | undefined): string {
  const n = Math.min(12, Math.max(1, Math.trunc(span ?? 12) || 12))
  return SPAN_CLASS[n]
}

// ── "Data just arrived" (epic #1935) ───────────────────────────────────────

/** How long a panel stays highlighted after its payload changed. */
export const PANEL_ARRIVAL_MS = 1200

/**
 * The cue, in full.
 *
 * A single-shot `box-shadow` ramp: up fast enough to catch the eye at the edge
 * of vision, down slowly enough that it reads as a decay rather than a blink.
 * It paints on the CELL, not inside the card, so the panel components own
 * nothing of this and a panel rendered anywhere else (the public page, the
 * dashboard strip) has no route by which it could flash.
 *
 * The reduced-motion block is the `app/globals.css` idiom for `.agent-active-*`
 * — `animation: none`, and the meaning survives without it. Here that means the
 * highlight still APPEARS for the same window and then goes, as a steady ring
 * with no ramp: no movement, no fade, same information. The DOM state
 * (`data-panel-arrival`) is set identically either way, so nothing about *what*
 * is being said depends on the media query; only how it is drawn.
 *
 * Declared here rather than in `app/globals.css` deliberately: this rule is
 * meaningless outside this component, and the file it would otherwise live in
 * is being edited on another branch.
 */
export const PANEL_ARRIVAL_CSS = `
@keyframes crewship-panel-arrival {
  0%   { box-shadow: 0 0 0 0 rgba(30, 123, 254, 0); }
  8%   { box-shadow: 0 0 0 2px rgba(30, 123, 254, 0.45), 0 0 16px 2px rgba(30, 123, 254, 0.20); }
  100% { box-shadow: 0 0 0 2px rgba(30, 123, 254, 0), 0 0 16px 2px rgba(30, 123, 254, 0); }
}
[data-panel-arrival="flash"] {
  animation: crewship-panel-arrival ${PANEL_ARRIVAL_MS}ms ease-out 1;
}
@media (prefers-reduced-motion: reduce) {
  [data-panel-arrival="flash"] {
    animation: none;
    box-shadow: 0 0 0 2px rgba(30, 123, 254, 0.35);
  }
}
`

/**
 * React 19 hoists a `<style>` carrying `href` + `precedence` into the head and
 * dedupes it by href, so the grid can declare its own rule without a global
 * stylesheet edit and without shipping it once per panel.
 */
function PanelArrivalStyles() {
  return (
    <style href="crewship-panel-arrival" precedence="medium">
      {PANEL_ARRIVAL_CSS}
    </style>
  )
}

/** True when the payload is something rather than the absence of something. */
function hasPayload(payload: unknown): boolean {
  if (payload === null || payload === undefined) return false
  if (typeof payload === "string") return payload.trim() !== ""
  if (Array.isArray(payload)) return payload.length > 0
  if (typeof payload === "object") return Object.keys(payload as object).length > 0
  return true
}

/**
 * What "the data changed" means, as one comparable string — or null for a
 * panel that is not allowed to flash at all.
 *
 * Keyed on the PAYLOAD and nothing else. `produced_at` moves every time a
 * producer runs, whether or not it found anything new, so a signature carrying
 * it would flash on a 30-second cron pushing identical numbers — which is a
 * heartbeat, and a heartbeat is what this is not.
 */
export function panelArrivalSignature(panel: PanelView): string | null {
  // The verdict is the authority. `stale` and `failed` are silent by
  // construction, and so is a state this build could not read (normalised to
  // `never_produced` — never optimistically to `fresh`).
  if (panel.snapshot.state !== "fresh") return null
  if (!hasPayload(panel.snapshot.payload)) return null
  try {
    return JSON.stringify(panel.snapshot.payload) ?? null
  } catch {
    // A payload that cannot be serialised cannot be compared, and a cue we
    // cannot justify is one we do not show.
    return null
  }
}

/**
 * Flash once when `signature` changes to a new, non-null value.
 *
 * The first signature observed is a baseline: a panel does not flash because
 * it mounted. A null signature (ineligible panel) clears any flash in flight
 * and is remembered, so the transition back into `fresh` with genuinely new
 * data is what flashes — not the recovery itself.
 *
 * Known limit: a second payload landing INSIDE the window extends the
 * highlight rather than restarting the ramp, because the attribute never
 * leaves and a CSS animation only replays when its selector re-matches. At the
 * cadence a panel is pushed at (§4 SLAs are seconds-to-minutes) that is one
 * highlight for two arrivals a fraction of a second apart, which is what a
 * reader would perceive anyway — and the alternative, blinking the attribute
 * off for a frame, would flicker.
 */
export function useArrivalFlash(signature: string | null, durationMs = PANEL_ARRIVAL_MS): boolean {
  const [flashing, setFlashing] = React.useState(false)
  // `undefined` is "nothing observed yet", which is distinct from a panel
  // observed to be ineligible (`null`).
  const seen = React.useRef<string | null | undefined>(undefined)

  React.useEffect(() => {
    const previous = seen.current
    seen.current = signature
    if (previous === undefined) return
    if (signature === null) {
      setFlashing(false)
      return
    }
    if (previous === signature) return
    setFlashing(true)
    const timer = setTimeout(() => setFlashing(false), durationMs)
    return () => clearTimeout(timer)
  }, [signature, durationMs])

  return flashing
}

export interface PageViewProps {
  page: PageRecord | null
  slug: string
  loading: boolean
  error: string | null
  notFound: boolean
  onBack: () => void
  /** Injected clock — absolute ages are computed, so a test can pin `now`. */
  now?: Date
  /**
   * Only for a caller that already holds it. Left unset, the action bar reads
   * the active workspace from `useWorkspace()` — which is loaded by the time
   * this surface mounts, and which is the source the rest of the dashboard
   * uses. Passing it avoids that lookup where it is already known.
   */
  workspaceId?: string | null
  /**
   * Whether this page is actually subscribed and receiving (`usePage().live`).
   *
   * Defaults to `offline` rather than to `live`: a caller that does not pass it
   * has no realtime provider to speak of, and the indicator's whole job is to
   * refuse to claim liveness it cannot demonstrate.
   */
  live?: PageLiveness
}

export function PageView({
  page,
  slug,
  loading,
  error,
  notFound,
  onBack,
  now,
  workspaceId,
  live = "offline",
}: PageViewProps) {
  // The bar is derived from the panels the SERVER sent, sealed placeholders
  // included, so it has the same shape for every viewer (§2.3). It lives out
  // here rather than in PageBody because the owner asked for it under the
  // breadcrumb row and outside the scroll area — a bar that scrolls away is a
  // bar you have to go back up to use.
  const tabs = React.useMemo<PageTabView[]>(() => derivePageTabs(page?.panels ?? []), [page])
  const [activeTab, selectTab] = usePageTabState(tabs)

  // The bar and the groups are two halves of one control and have to agree on
  // the ids that pair them (`pageTabIds`). They are also far apart in this
  // tree — the bar sits outside the scroll area, the groups are down inside
  // PanelGrid — so the scope is minted HERE, at the one component that renders
  // both, and threaded down.
  //
  // Deriving the ids from the tab slug alone would be shorter and would make
  // "one PageView per document" a rule this component does not state and
  // nothing enforces: a second one — a preview beside the page, a future embed
  // that reuses this rather than `public-page-view.tsx`, a test — would emit
  // every id twice, and each reference would then resolve to whichever half
  // rendered first, silently pairing one view's tabs with the other's panels.
  // `useId` is the cheap way not to owe that rule; it is also SSR-stable,
  // which a module-level counter is not.
  const tabIdScope = React.useId()

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Breadcrumb back-bar — inside the content area, matching /routines and
          /issues, so it does not compete with the global sub-bar above. */}
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card/40 px-4 py-2 print:hidden">
        <button
          type="button"
          onClick={onBack}
          className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          Back to pages
        </button>
        <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
        <span className="truncate text-xs font-medium text-foreground/85" title={page?.name ?? slug}>
          {page?.name ?? slug}
        </span>
        <span className="type-page-stamp ml-1 truncate text-muted-foreground">{slug}</span>
        {/* The liveness indicator sits in the header, once — not on every
            panel. "Is the pipe open" is a property of the page; "did MY data
            just change" is a property of a panel, and each is said in exactly
            one place. */}
        <div className="ml-auto flex shrink-0 items-center gap-3">
          {page?.ownerLabel && (
            <span className="type-page-meta shrink-0 text-muted-foreground-soft">
              {page.ownerLabel}
            </span>
          )}
          <LiveIndicator liveness={live} />
        </div>
      </div>

      {/* The bar renders only when the groups it points at will render too.
          `tabs` is derived from the LAST page the query held, and TanStack
          keeps that data when a refetch fails (`usePage` sets `error` and
          `notFound` from `query.error` while `query.data` stands) — so
          `error` and `notFound` are both reachable with a page still in hand.
          `PageBody` returns early in either case and mounts no group at all,
          which would leave every `aria-controls` here resolving to nothing:
          the dangling reference `pageTabIds` exists to prevent, and an
          `aria-valid-attr-value` failure. A bar whose tabs reveal nothing is
          also the wrong thing to draw above an error. */}
      {tabs.length > 0 && !error && !notFound && (
        <PageTabs tabs={tabs} activeId={activeTab} onSelect={selectTab} idScope={tabIdScope} />
      )}

      <div className="flex-1 overflow-auto">
        <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
          <PageBody
            page={page}
            slug={slug}
            loading={loading}
            error={error}
            notFound={notFound}
            now={now}
            workspaceId={workspaceId}
            tabs={tabs}
            activeTab={activeTab}
            tabIdScope={tabIdScope}
          />
        </div>
      </div>
    </div>
  )
}

function PageBody({
  page,
  slug,
  loading,
  error,
  notFound,
  now,
  workspaceId,
  tabs,
  activeTab,
  tabIdScope,
}: Omit<PageViewProps, "onBack"> & {
  tabs: PageTabView[]
  activeTab: string
  tabIdScope: string
}) {
  if (notFound) {
    return (
      <EmptyState
        icon={CONCEPT_ICON.pages}
        title={`No page is addressed "${slug}"`}
        description="It may have been deleted, renamed, or it belongs to a crew you are not in. Pick a page from the list on the left, or run crewship page list to see what exists."
      />
    )
  }

  if (error) {
    return (
      <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
        {error}
      </div>
    )
  }

  if (loading && !page) {
    return (
      <div className="grid grid-cols-1 gap-4 md:grid-cols-12">
        <Skeleton className="h-[180px] rounded-xl md:col-span-6" />
        <Skeleton className="h-[180px] rounded-xl md:col-span-6" />
        <Skeleton className="h-[220px] rounded-xl md:col-span-12" />
      </div>
    )
  }

  if (!page) return null

  const panels = page.panels ?? []
  if (panels.length === 0) {
    return (
      <EmptyState
        icon={CONCEPT_ICON.pages}
        title="This page declares no panels"
        description={`A page with no panel has nothing to render and nothing to push to. Add a panel to the spec and save it with crewship page update ${slug} --file page.yaml.`}
      />
    )
  }

  // The freshness summary is computed over the whole PAGE — every panel the
  // server sent, on every tab — and never over the tab in view. That is the
  // rule that keeps tabs from undoing §4: a page that reads FRESH while a
  // hidden tab is failing is the silent-old-numbers failure with one extra
  // click in front of it. `page.state` and `panels.length` come off the record
  // as the server sent it, so switching tabs cannot move either, and there is a
  // test that says so in those terms.
  const meta = page.state ? PAGE_STATE_META[page.state] : null

  return (
    <>
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="truncate text-lg font-semibold tracking-tight">{page.name}</h1>
          <p className="text-xs text-muted-foreground">
            {panels.length} {panels.length === 1 ? "panel" : "panels"}
            {page.description ? ` · ${page.description}` : ""}
          </p>
        </div>
        {meta && (
          <span
            className={cn(
              "type-page-label shrink-0 whitespace-nowrap",
              meta.tone,
            )}
          >
            {meta.label}
          </span>
        )}
      </div>

      <PanelGrid
        panels={panels}
        slug={slug}
        now={now}
        workspaceId={workspaceId}
        tabs={tabs}
        activeTab={activeTab}
        tabIdScope={tabIdScope}
      />
    </>
  )
}

/**
 * The grid, and the only surface in the app that mounts `PanelActionsProvider`.
 *
 * The actions reach the buttons through that provider rather than through
 * `PanelSpec`, so a panel rendered anywhere else — the public page (§7.3.2
 * rule 1), the dashboard strip, a test — has no route by which an action could
 * become a button. Absence is the default; this component is the single
 * exception, on the one surface where the viewer is authenticated and the
 * server has already filtered the page per crew.
 *
 * The provider wraps ALL the tabs, not one grid per tab. Action ids are unique
 * within the PAGE and a `toggle` may name any panel on it
 * (`internal/pages/spec.go`), so a provider per tab would leave a button that
 * validated at save time doing nothing at click time — the worst shape a
 * control can have.
 */
function PanelGrid({
  panels,
  slug,
  now,
  workspaceId,
  tabs,
  activeTab,
  tabIdScope,
}: {
  panels: NonNullable<PageRecord["panels"]>
  slug: string
  now?: Date
  workspaceId?: string | null
  tabs: PageTabView[]
  activeTab: string
  tabIdScope: string
}) {
  const actions = React.useMemo(() => {
    const map = new Map<string, readonly PageAction[]>()
    for (const panel of panels) {
      if (panel.actions.length > 0) map.set(panel.spec.id, panel.actions)
    }
    return map
  }, [panels])

  return (
    <PanelActionsProvider slug={slug} actions={actions} workspaceId={workspaceId}>
      {tabs.length === 0 ? (
        // No panel on this page declares a tab: no bar, no groups, and the
        // grid is exactly the markup it was before tabs existed.
        <PanelGridCells panels={panels} now={now} />
      ) : (
        <div className="flex flex-col gap-4">
          <PageTabsStyles />
          {tabs.map((tab) => (
            // Every group stays mounted and the inactive ones are hidden, which
            // is what lets print reveal them all (§10b.8) — and what keeps a
            // panel's "the data just changed" baseline from resetting every
            // time somebody clicks back to its tab.
            <PageTabGroup
              key={tab.id}
              tab={tab}
              active={tab.id === activeTab}
              idScope={tabIdScope}
            >
              <PanelGridCells panels={tab.panels} now={now} />
            </PageTabGroup>
          ))}
        </div>
      )}
    </PanelActionsProvider>
  )
}

/**
 * The cells, inside the provider so they can read what a `toggle` hid.
 *
 * `kind: "toggle"` is local and shows or hides panel ids
 * (`internal/pages/spec.go` `PanelAction.Target`); this is where "hidden"
 * becomes true. It is view state and nothing else — no request was made, no
 * spec changed, and a reload brings every panel back.
 */
function PanelGridCells({
  panels,
  now,
}: {
  panels: NonNullable<PageRecord["panels"]>
  now?: Date
}) {
  const hidden = useHiddenPanels()
  return (
    <div
      data-slot="panel-grid"
      className="grid grid-cols-1 gap-4 md:grid-cols-12 print:grid-cols-1"
    >
      <PanelArrivalStyles />
      {panels
        .filter((panel) => !hidden.has(panel.spec.id))
        .map((panel) => (
          // Keyed on the panel id, so a refetch replaces the payload inside a
          // cell that persists — which is what makes "changed since last time"
          // a question this component can answer at all.
          <PanelCell key={panel.spec.id} panel={panel} now={now} />
        ))}
    </div>
  )
}

/** One cell: the grid slot, the container context, and the arrival cue. */
function PanelCell({ panel, now }: { panel: PanelView; now?: Date }) {
  const signature = panelArrivalSignature(panel)
  const flashing = useArrivalFlash(signature)
  return (
    <div
      data-slot="panel-cell"
      data-panel-span={panel.spec.span}
      // Set in both states rather than toggled on and off: the DOM always says
      // what it means, which is also what a reduced-motion reader is left with.
      data-panel-arrival={flashing ? "flash" : "idle"}
      // min-w-0 so a wide table inside a narrow cell scrolls in its own
      // box instead of stretching the track and shoving its neighbours
      // off the grid. rounded-xl matches the card the cue is drawn around —
      // a box-shadow follows the border radius of the box it is painted on,
      // and a square ring around a rounded card reads as a rendering bug.
      className={cn(
        // self-start, and this is load-bearing rather than cosmetic. A grid
        // cell stretches to the tallest panel in its row by default, while the
        // card inside keeps its natural height — so the arrival ring, painted
        // on the CELL, was drawn around the card plus whatever empty space the
        // row's tallest neighbour created below it. A cue that says "this panel
        // just received data" must not outline space that received nothing.
        // Hugging the content also leaves the visible layout exactly as it was:
        // the cards were never stretched, only the invisible cell was.
        "@container/panel min-w-0 self-start rounded-xl print:break-inside-avoid",
        spanClass(panel.spec.span),
      )}
    >
      <PanelRenderer panel={panel.spec} data={panel.snapshot} now={now} />
    </div>
  )
}
