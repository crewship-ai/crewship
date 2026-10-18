"use client"

import { usePathname } from "next/navigation"
import { useEffect, useState } from "react"
import Link from "next/link"
import { ChevronRight, PanelLeft, Search } from "lucide-react"

import { areaOf } from "@/lib/nav-sections"
import { Button } from "@/components/ui/button"
import { useAccessMode } from "@/hooks/use-access-mode"
import { useIsMobile } from "@/hooks/use-mobile"
import { CommandPalette } from "@/components/command-palette"
import { InboxBell } from "@/components/features/inbox/inbox-bell"
import { ActivityBell } from "@/components/features/activity/activity-bell"
import { cn } from "@/lib/utils"
import { useAppStore } from "@/lib/store"

import { useIsTabletPanel } from "./secondary-panel"
import { MobileMoreSheet } from "./mobile-more-sheet"

// The top bar is the product's identity strip, not a page label — it says
// "Crewship" and nothing else. There used to be a route -> title map here
// ("/crews" -> "Crews & Agents", "/skills" -> "Skills", ...), which printed
// the page name directly above the SubBar that already carries it, stacked
// one row apart: "Crews & Agents" over "Crews & Agents · 3 crews". Only the
// mapped routes did that; every other page (inbox, issues, routines,
// integrations, admin) fell through to the "Crewship" default and read
// correctly, which is the behaviour the map is removed in favour of.
//
// "/" is the one route that had no SubBar underneath, so it loses its only
// on-screen name here. The fix for that belongs on the dashboard page —
// give it the SubBar every other page has — not in a map of exceptions.
//
// Deep pages keep their breadcrumb (agent detail, chat) — that is a click-path
// back out of a detail view, not a restatement of the SubBar.
//
// Settings used to be a third exception: a "Settings / <tab>" breadcrumb fed
// by a settingsTab mirror in the zustand store. It is not a detail view — its
// section nav never leaves the page — so the identity moved into the Settings
// sub-bar (title + section, same shape as Admin) and the mirror is gone.




// There used to be an agent-detail breadcrumb here — "Agents / <crew> /
// <agent>", keyed on a /^\/crews\/agents\/([^/]+)/ match of the pathname and
// fed by a GET /api/v1/agents/<id> to resolve the names. All three of its
// destinations were routes the selection-driven /crews redesign deleted:
// /crews/agents (no page.tsx, no crews/agents.html in the export) and
// /crews/<crewId> (same). The branch could not fire from a working navigation
// either, because nothing routes to /crews/agents/<id> any more — it rendered
// only when the Go static handler fell a bad URL through to the SPA root,
// which put an agent breadcrumb above the dashboard and fired a fetch for it.
//
// The agent detail surface is now /crews?agent=<slug> on the /crews canvas,
// which carries its own sub-bar, and the one deep page that still needs a way
// back — chat — has the breadcrumb below pointing exactly there.
export function AppToolbar() {
  const pathname = usePathname()
  // Inbox and runs are not on the restricted allowlist: their bells are not
  // rendered unless the session is trusted. (The connection, fleet and build
  // status are inside the Activity bell's panel.)
  const trusted = useAccessMode() === "trusted"
  const isMobile = useIsMobile()
  const setPageBarSlot = useAppStore((st) => st.setPageBarSlot)
  const secondaryPanel = useAppStore((st) => st.secondaryPanel)
  const setSecondaryPanelOpen = useAppStore((st) => st.setSecondaryPanelOpen)
  const secondaryPanelOpen = useAppStore((st) => st.secondaryPanelOpen)
  const tabletPanel = useIsTabletPanel() && !isMobile
  // The palette's open state lives in the app store so the sidebar's search
  // field can open it; a local fallback keeps the toolbar working where a
  // test or an embed provides a store without it.
  const [localCmdk, setLocalCmdk] = useState(false)
  const storeCmdk = useAppStore((st) => st.commandPaletteOpen)
  const storeSetCmdk = useAppStore((st) => st.setCommandPaletteOpen)
  const paletteQuery = useAppStore((st) => st.commandPaletteQuery)
  // Typed as always present, but a partial store (tests, embeds) may lack it.
  const hasStorePalette = typeof (storeSetCmdk as unknown) === "function"
  const cmdkOpen = hasStorePalette ? Boolean(storeCmdk) : localCmdk
  const setCmdkOpen = hasStorePalette ? storeSetCmdk : setLocalCmdk
  const breadcrumbs = useAppStore((s) => s.breadcrumbs)

  // ⌘K opens the global palette, on every route, and it is the ONLY listener
  // for that key in the product.
  //
  // It used to share it. The chat surface's own palette bound `mod+k` too
  // (composer/slash-palette.tsx), neither listener stopped the other, and both
  // are on `document` — where `stopPropagation` cannot order two listeners on
  // the same node in the same phase into a winner. So one press inside a
  // conversation opened two stacked dialogs.
  //
  // The key stayed here because this toolbar PRINTS it: the search button a few
  // lines down carries "⌘K" in a <kbd>, on screen on every route including
  // chat. A binding that changes meaning depending on where focus happens to be
  // would make that label wrong exactly where the cursor usually is. The chat
  // palette moved to ⌘/ and gained a button of its own; see the note on its
  // useHotkeys call.
  //
  // components/features/chat/__tests__/chat-hotkey-ownership.test.tsx is what
  // keeps this a single owner — it fails if pressing ⌘K in a conversation
  // reaches anything else.
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault()
        setCmdkOpen((prev) => !prev)
      }
    }
    document.addEventListener("keydown", onKeyDown)
    return () => document.removeEventListener("keydown", onKeyDown)
  }, [setCmdkOpen])


  const chatMatch = pathname.match(/^\/chat\/([^/]+)/)
  const isChatPage = Boolean(chatMatch)
  const chatAgentSlug = chatMatch?.[1] ? decodeURIComponent(chatMatch[1]) : null

  const area = areaOf(pathname)

  function renderBreadcrumbs() {
    // Chat page: a path back out of the conversation — Crews / <crew> /
    // <agent> / Chat, every step a place. The chat page publishes the crew
    // and the agent by NAME through the store once its roster resolves; the
    // slug from the URL is only the fallback while that is in flight, and it
    // is never shown for the onboarding Guide, whose slug is an internal
    // identifier (docs/ux/audit-conversations.md P1-7).
    if (isChatPage && chatAgentSlug) {
      const path = breadcrumbs.length > 0
        ? breadcrumbs
        : [{ label: "Crews", href: "/crews" }, ...(chatAgentSlug.startsWith("_") ? [] : [{ label: chatAgentSlug, href: `/crews?agent=${encodeURIComponent(chatAgentSlug)}` }])]
      return (
        <>
          {path.map((item, i) => (
            <span key={`${item.label}-${i}`} className="flex min-w-0 items-center gap-1.5">
              {item.href ? (
                <Link href={item.href} className="truncate text-sm text-muted-foreground transition-colors hover:text-foreground">
                  {item.label}
                </Link>
              ) : (
                <span className="truncate text-sm text-muted-foreground">{item.label}</span>
              )}
              <span className="shrink-0 text-sm text-muted-foreground-soft">/</span>
            </span>
          ))}
          <span className="text-sm font-semibold">Chat</span>
        </>
      )
    }

    return (
      <div className="flex items-center gap-1.5 min-w-0">
        {area ? (
          <>
            <span className="area-tile" aria-hidden><area.icon /></span>
            {breadcrumbs.length > 0 ? (
              <Link href={area.href} className="truncate text-sm text-muted-foreground transition-colors hover:text-foreground">{area.title}</Link>
            ) : (
              <span className="truncate text-sm font-semibold">{area.title}</span>
            )}
          </>
        ) : (
          <span className="text-sm font-semibold truncate">Crewship</span>
        )}
        {breadcrumbs.length > 0 && breadcrumbs.map((item, i) => (
          <div key={i} className="flex items-center gap-1.5 min-w-0">
            <ChevronRight className="size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
            {item.href ? (
              <Link href={item.href} className="max-w-[160px] truncate text-xs text-muted-foreground transition-colors hover:text-foreground/90">
                {item.label}
              </Link>
            ) : item.onClick ? (
              <button
                type="button"
                onClick={item.onClick}
                className="text-xs text-muted-foreground hover:text-foreground/90 transition-colors truncate max-w-[160px]"
              >
                {item.label}
              </button>
            ) : (
              <span className="text-xs text-foreground/80 truncate max-w-[160px]">{item.label}</span>
            )}
          </div>
        ))}
      </div>
    )
  }

  return (
    <header className="flex h-[var(--app-header-h)] pt-[env(safe-area-inset-top)] shrink-0 items-center justify-between bg-background px-3 sm:pl-4 sm:pr-3 border-b border-border">
      {/* Left: the area and its breadcrumbs, then the page's own header —
          title, live description and actions — which SubBar renders into
          this slot (Harbor 2: one 48 px bar, not a bar plus a sub-bar). */}
      <div className="flex min-w-0 flex-1 items-center gap-1.5 overflow-hidden">
        {renderBreadcrumbs()}
        {/* On a phone the slot holds the page's primary action (icon-only) and
            its explorer toggle, between the title and the search (v67). */}
        <div ref={setPageBarSlot} data-slot="page-bar" data-area-title={area?.title ?? ""} className="flex min-w-0 flex-1 items-center gap-2" />
      </div>

      {/* Right: Status indicators + search + notifications */}
      <div className="flex items-center gap-1 sm:gap-1.5 shrink-0">
        {/* Search lives in the sidebar on desktop and tablet (Harbor 2); the
            phone has no sidebar, so it keeps an icon here. ⌘K is still owned
            by the listener above. */}
        {/* Phone: the page's secondary panel opens as a sheet (Harbor 2 M3).
            Tablet: it floats over the content and this button toggles it
            (v67 panelBtn, COM-05). */}
        {/* A tablet's list panel (Inbox, Settings, Admin) is docked, so it has
            no button (v67 panelBtn is for filter panels only). */}
        {(isMobile || (tabletPanel && secondaryPanel?.kind !== "list")) && secondaryPanel && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={secondaryPanel.label}
            aria-expanded={tabletPanel ? secondaryPanelOpen : undefined}
            data-testid="secondary-panel-button"
            onClick={() => setSecondaryPanelOpen(tabletPanel ? !secondaryPanelOpen : true)}
          >
            <PanelLeft className="h-4 w-4" />
          </Button>
        )}

        {/* Mobile: search icon only */}
        <Button variant="ghost" size="icon-sm" className="md:hidden coarse:h-[44px] coarse:w-[44px]" aria-label="Search" onClick={() => setCmdkOpen(true)}>
          <Search className="h-4 w-4" />
        </Button>

        {/* Two surfaces, split by who is waiting: Activity = what the
          * machines are doing (runs, live and just-finished, nothing asked
          * of you); Inbox = what a human is being asked for (waitpoints,
          * escalations, failed runs, replies).
          *
          * There was a third bell here. It read an entity-scoped
          * `notifications` table that nothing in the product ever wrote to —
          * `CreateNotification` had zero callers and there was no create
          * route — so it was permanently empty by construction. It was not
          * an unfinished wire: the pipeline the product actually runs is
          * event -> inbox item -> notifyroute.Router -> channels + journal,
          * with the Inbox as the origin and Activity as the record, and that
          * table sits outside it. Three panels also cost more than they
          * bought: "machine vs human" is a line a user can hold, "Activity
          * vs Inbox vs Notifications" is not. The backend behind that bell —
          * handler, routes, CLI group and the table itself — followed in
          * #1751.
          *
          * System-wide messages (new version, degraded runtime, lost
          * realtime) are banners — UpdateBanner / RuntimeBanner /
          * RealtimeStatusBanner — because a notice nobody can afford to miss
          * does not belong behind a closed dropdown. */}
        {trusted && (
          <div className="flex items-center gap-0.5">
            {/* The strip reads in two halves: the page's actions and search,
                then the two bells. The rule marks where one ends. On a phone
                it is the activity bell only: Inbox is a tab, and the account
                is a row in the More sheet. */}
            <span className="mx-1 hidden h-5 w-px bg-foreground/[0.1] md:block" aria-hidden />
            <ActivityBell />
            <div className="hidden md:flex">
              <InboxBell />
            </div>
          </div>
        )}
      </div>

      {/* Mobile: the More sheet (v67 pop-more) */}
      {isMobile && <MobileMoreSheet />}

      <CommandPalette open={cmdkOpen} onOpenChange={setCmdkOpen} initialQuery={paletteQuery || undefined} />
    </header>
  )
}
