"use client"

/**
 * The tab bar under the breadcrumb — one page, several screens (PRD §9, §2.3,
 * §3, §4, §10b.8).
 *
 * A page is a fixed structure a reader returns to, and past about six panels it
 * stops being glanceable and becomes a scroll. Every ordinary website answered
 * this with a bar of tabs; this is that bar, in the place the owner asked for
 * it — under "Back to pages", above the grid.
 *
 * The format is one optional `tab:` on the panel and no second list
 * (internal/pages/tabs.go has the argument). What this file owns is everything
 * that follows from tabs being a thing that HIDES panels:
 *
 *  1. **Every tab carries the worst state of its own panels.** `failed` over
 *     `stale` over `never_produced` over `fresh`, drawn as a GLYPH and not as
 *     colour alone (§3). Without it a page's whole claim (§4: a panel that
 *     stops reporting says so) would hold only for the tab you happen to be
 *     looking at, and a critical panel could sit failing on the third tab while
 *     the page reads fine.
 *
 *  2. **The header's freshness summary is not this component's business.** It
 *     is computed over every panel on the page, in `page-view.tsx`, from the
 *     record the server sent — so it does not move when the tab does. There is
 *     a test that says exactly that.
 *
 *  3. **The bar is the same for every viewer.** A tab whose panels are all
 *     sealed still appears, with its placeholders under it. Dropping it would
 *     reflow the page per reader — the property §2.3 spends its length arguing
 *     for — and the tab's absence would itself disclose that everything on it
 *     belongs to a crew this reader is not in. A tab of only sealed panels
 *     carries NO state glyph, because the server sends no state for a panel the
 *     viewer may not see and the bar does not guess one.
 *
 *  4. **Print has no tabs** (§10b.8). Paper cannot be clicked, so `@media
 *     print` reveals every group in order under its tab name and hides the bar
 *     itself. The rule is declared here rather than in `app/globals.css`
 *     because it is meaningless outside this component — the same reasoning,
 *     and the same React 19 hoisted-`<style>` idiom, as the arrival cue in
 *     `page-view.tsx`.
 *
 *  5. **The selected tab is in the URL**, so a link opens on the right screen.
 *
 * Mobile: the bar scrolls horizontally rather than wrapping into a stack. Four
 * short words that scroll are a bar; four words on three lines are a menu that
 * has eaten the page.
 *
 * Cost: 0 KB. `ToolbarStrip` is the repo's canonical icon+label tab strip and
 * its own doc names this exact use ("in-page tab switching"), so there is no
 * third tab idiom here.
 */

import * as React from "react"
import { useSearchParams } from "next/navigation"

import { ToolbarStrip, type ToolbarTab } from "@/components/layout/toolbar-strip"
import { PAGE_STATE_META } from "@/components/features/pages/page-state"
import { cn } from "@/lib/utils"
import type { PageTabView } from "@/hooks/use-pages"

/** The query parameter a link carries. */
export const PAGE_TAB_PARAM = "tab"

// ── Which tab is showing ───────────────────────────────────────────────────

/**
 * Resolve `?tab=` against the page's tabs, falling back to the first one.
 *
 * Deriving rather than correcting in an effect: an unknown or absent tab shows
 * the first one on the very first render, and moving to another page — whose
 * tabs are different, or which has none — cannot leave a tab selected that this
 * page does not have.
 */
export function resolveTabId(tabs: readonly PageTabView[], requested: string | null): string {
  if (tabs.length === 0) return ""
  const hit = tabs.find((t) => t.id === requested)
  if (hit) return hit.id
  // A link written by hand is likelier to carry the visible name than the
  // slug, and answering it costs one comparison.
  const byName = tabs.find((t) => t.name.toLowerCase() === (requested ?? "").trim().toLowerCase())
  return byName ? byName.id : tabs[0].id
}

/**
 * The selected tab, in the URL.
 *
 * `useSearchParams` and not `window.location.search`, for the reason
 * `settings-layout.tsx` documents at length: during a client-side navigation
 * the App Router renders the new route before `window.location` has been
 * updated, so an initialiser reading the location object sees the OLD url and
 * every deep link lands on the first tab.
 *
 * Writing goes through `history.replaceState` rather than `router.replace`: a
 * tab is not a navigation, and pushing a route entry per click would make Back
 * walk the tabs instead of leaving the page. Local state is therefore
 * authoritative for clicks — `replaceState` does not notify `useSearchParams`
 * anyway — and the query string is followed only when it actually changes,
 * which is what a real navigation looks like.
 *
 * (`nuqs` is a dependency of this repo but has no import anywhere in it, and
 * adopting it here would mean mounting its adapter in the root layout — a
 * file this change does not own. This is the idiom the app actually uses.)
 */
export function usePageTabState(tabs: readonly PageTabView[]): [string, (id: string) => void] {
  const searchParams = useSearchParams()
  const search = searchParams.toString()
  const requested = searchParams.get(PAGE_TAB_PARAM)

  const [picked, setPicked] = React.useState<string | null>(requested)
  const appliedSearch = React.useRef(search)
  React.useEffect(() => {
    if (appliedSearch.current === search) return
    appliedSearch.current = search
    setPicked(new URLSearchParams(search).get(PAGE_TAB_PARAM))
  }, [search])

  const activeId = resolveTabId(tabs, picked)

  const select = React.useCallback((id: string) => {
    setPicked(id)
    if (typeof window === "undefined") return
    const url = new URL(window.location.href)
    url.searchParams.set(PAGE_TAB_PARAM, id)
    window.history.replaceState(null, "", url.toString())
  }, [])

  return [activeId, select]
}

// ── The bar ────────────────────────────────────────────────────────────────

/**
 * One tab's worst state, as a glyph.
 *
 * Icon plus tone, never tone alone (§3). The word is carried too — in the
 * button's accessible name, because a reader who cannot see the glyph is
 * exactly the reader who most needs to be told which tab is failing.
 */
function TabStateGlyph({ state }: { state: PageTabView["state"] }) {
  if (!state) return null
  const meta = PAGE_STATE_META[state]
  const Icon = meta.icon
  return <Icon className={cn("h-3.5 w-3.5 shrink-0", meta.tone)} aria-hidden="true" />
}

export interface PageTabsProps {
  tabs: readonly PageTabView[]
  activeId: string
  onSelect: (id: string) => void
}

export function PageTabs({ tabs, activeId, onSelect }: PageTabsProps) {
  const items: ToolbarTab[] = tabs.map((tab) => ({
    id: tab.id,
    label: tab.name,
    badge: <TabStateGlyph state={tab.state} />,
    // The state in words, so the bar says the same thing to a screen reader
    // that it says to an eye. A tab with no readable state (all its panels are
    // sealed) says only its name, which is all that is known about it.
    ariaLabel: tab.state ? `${tab.name} — ${PAGE_STATE_META[tab.state].label}` : tab.name,
  }))

  return (
    <ToolbarStrip
      data-slot="page-tabs"
      tabs={items}
      activeTab={activeId}
      onTabChange={onSelect}
      ariaLabel="Page tabs"
      // Not `compact`: it hides tab LABELS below the `sm` breakpoint, and a tab
      // here is nothing but its label — that would leave a phone showing a row
      // of identical freshness glyphs with no way to tell the tabs apart.
      // The bar scrolls sideways at the narrow breakpoint instead of wrapping
      // into a stack of rows. `bg-card/40` matches the breadcrumb row directly
      // above it, so the two read as one piece of chrome rather than as a
      // second toolbar.
      className="overflow-x-auto bg-card/40 print:hidden"
    />
  )
}

// ── The groups ─────────────────────────────────────────────────────────────

/**
 * What print does with a tab (§10b.8).
 *
 * Paper has no tabs, so every group is revealed, each under its own name, in
 * bar order — and the bar itself, which cannot be clicked on paper, is hidden.
 * `!important` because the on-screen state is the `hidden` attribute plus a
 * utility class, and this has to win over both.
 *
 * Declared here rather than in `app/globals.css`: the rule is meaningless
 * outside this component, and React 19 hoists a `<style href precedence>` into
 * the head and dedupes it by href, so it ships once however many groups render.
 */
export const PAGE_TABS_PRINT_CSS = `
@media print {
  [data-slot="page-tabs"] { display: none !important; }
  [data-slot="tab-group"] { display: block !important; }
  [data-slot="tab-group-name"] { display: block !important; }
}
`

export function PageTabsStyles() {
  return (
    <style href="crewship-page-tabs-print" precedence="medium">
      {PAGE_TABS_PRINT_CSS}
    </style>
  )
}

export interface PageTabGroupProps {
  tab: PageTabView
  active: boolean
  children: React.ReactNode
}

/**
 * One tab's panels.
 *
 * Every group stays in the DOM and the inactive ones carry the `hidden`
 * attribute — the standard tabpanel shape, and the only way print can render
 * what the screen is hiding. The name is drawn only on paper: on screen the bar
 * above already says which tab this is, and repeating it would spend a heading
 * on something the reader just clicked.
 */
export function PageTabGroup({ tab, active, children }: PageTabGroupProps) {
  return (
    <section
      data-slot="tab-group"
      data-tab={tab.id}
      role="tabpanel"
      aria-label={tab.name}
      hidden={!active}
      className={active ? undefined : "hidden"}
    >
      <h2
        data-slot="tab-group-name"
        className="hidden pb-2 text-sm font-semibold tracking-tight"
      >
        {tab.name}
      </h2>
      {children}
    </section>
  )
}
