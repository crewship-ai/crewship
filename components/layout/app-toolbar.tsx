"use client"

import { usePathname } from "next/navigation"
import { useEffect, useState } from "react"
import Link from "next/link"
import { useAuth } from "@/hooks/use-auth"
import {
  Activity, BookOpen, ChevronDown, GitBranch, HelpCircle, Key, LayoutDashboard,
  LogOut, Menu, Network, Search, Settings, Shield, ShieldCheck, Store, User, X, Zap,
} from "lucide-react"

import { CONCEPT_ICON } from "@/lib/concept-icons"
import { useRealtime } from "@/hooks/use-realtime"
import { UserAvatar } from "@/components/ui/user-avatar"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,

  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Badge } from "@/components/ui/badge"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { useEngineStatus } from "@/hooks/use-engine-status"
import { useCrewsStatus } from "@/hooks/use-crews-status"
import { useProvisioningStatus } from "@/hooks/use-provisioning-status"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { useAbilities } from "@/hooks/use-abilities"
import { CommandPalette } from "@/components/command-palette"
import { InboxBell } from "@/components/features/inbox/inbox-bell"
import { ActivityBell } from "@/components/features/activity/activity-bell"
import { useAppStore } from "@/lib/store"

import { ProvisioningBadge } from "./app-toolbar-provisioning"
import { SystemStatusPill } from "./status-pill"

// External destinations for the user menu. Kept here (not env-driven) because
// they are stable public properties; the docs site is the Mintlify source of
// truth and Help & Support routes to the GitHub issue tracker.
const DOCS_URL = "https://docs.crewship.ai"
const GITHUB_URL = "https://github.com/crewship-ai/crewship"
const SUPPORT_URL = "https://github.com/crewship-ai/crewship/issues"

/**
 * Exported for the same reason as `navSections` in app-sidebar: the two nav
 * definitions are hand-kept twins, and a test that reads both is what stops a
 * surface from existing on the desktop rail and nowhere on a phone.
 */
export const mobileNavSections = [
  {
    label: "Work",
    items: [
      { title: "Dashboard", href: "/", icon: LayoutDashboard },
      // Same entry as the desktop rail, same icon (CONCEPT_ICON.sessions).
      // The mobile sheet otherwise picks its icons locally from lucide, but
      // an icon that changes between breakpoints is the drift lib/concept-icons
      // exists to prevent.
      { title: "Chat", href: "/chat", icon: CONCEPT_ICON.sessions },
      { title: "Crews & Agents", href: "/crews", icon: Network },
    ],
  },
  {
    label: "Configure",
    items: [
      { title: "Skills", href: "/skills", icon: Zap },
      { title: "Marketplace", href: "/marketplace", icon: Store, disabled: true },
      { title: "Credentials", href: "/credentials", icon: Key },
    ],
  },
  {
    label: "Monitor",
    items: [
      { title: "Runs", href: "/journal?tab=runs", icon: Activity },
      { title: "Audit Log", href: "/settings?tab=audit", icon: Shield },
    ],
  },
  {
    label: "System",
    items: [
      { title: "Settings", href: "/settings", icon: Settings },
      { title: "Admin", href: "/admin", icon: ShieldCheck, adminOnly: true },
    ],
  },
]


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
  const { workspaceId } = useWorkspace()
  const { status: engineStatus } = useEngineStatus(workspaceId)
  const crewsStatus = useCrewsStatus(workspaceId)
  const provisioning = useProvisioningStatus(workspaceId)
  const { session, signOut } = useAuth()
  const { status: wsStatus } = useRealtime()
  const isMobile = useIsMobile()
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [cmdkOpen, setCmdkOpen] = useState(false)
  const { role } = useAbilities()
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
  }, [])

  const userName = session?.user?.name ?? "User"
  const userEmail = session?.user?.email ?? ""
  const userAvatar = session?.user?.avatar_url ?? ""

  const chatMatch = pathname.match(/^\/chat\/([^/]+)/)
  const isChatPage = Boolean(chatMatch)
  const chatAgentSlug = chatMatch?.[1] ? decodeURIComponent(chatMatch[1]) : null

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
        <span className="text-sm font-semibold truncate">Crewship</span>
        {breadcrumbs.length > 0 && breadcrumbs.map((item, i) => (
          <div key={i} className="flex items-center gap-1.5 min-w-0">
            <span className="text-muted-foreground/30 text-xs">/</span>
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
    <header className="flex h-12 shrink-0 items-center justify-between bg-card px-3 sm:px-4 border-b border-white/[0.1]">
      {/* Left: breadcrumb only */}
      <div className="flex items-center gap-1.5 min-w-0 overflow-hidden">
        {renderBreadcrumbs()}
      </div>

      {/* Right: Status indicators + search + notifications */}
      <div className="flex items-center gap-1 sm:gap-1.5 shrink-0">
        {/* One status pill: connection on the left, fleet on the right.
          *
          * These were two — "Online" and "Crews idle" — and the second was
          * losing an argument it should not have been in. Its busy state
          * repeated the Activity panel's live count without the routine name,
          * elapsed time, cost or Cancel that panel carries; its quiet state
          * asserted "idle" from `agents.status`, a column that flips for the
          * six seconds an agent takes to answer, so the word was wrong about
          * as often as it was right.
          *
          * Merged rather than merely reworded, because two pills could state a
          * contradiction side by side: "Offline" next to "7 agents", where the
          * second is last-known and nobody can currently know it. One pill
          * drops the fleet half while the link is down. See status-pill.tsx.
          *
          * The provisioning badge stays separate and stays to the LEFT, so the
          * status pill never reflows when a build appears. */}
        <div className="hidden lg:flex items-center gap-1.5 mr-1">
          <ProvisioningBadge provisioning={provisioning} workspaceId={workspaceId} />
          <SystemStatusPill engineStatus={engineStatus} wsStatus={wsStatus} crews={crewsStatus} />
        </div>

        {/* Desktop: search button. Same pill geometry as the System / Crews
            status pills it sits beside, and the same .type-meta register as
            everything else in the strip — it had been the one control here
            wearing a hardcoded text-xs label and a text-xs ⌘ inside a
            text-[10px] key cap. */}
        <Button variant="outline" size="sm" className="hidden md:flex h-8 gap-2 rounded-full border-border bg-transparent text-muted-foreground hover:text-foreground px-3" aria-label="Search" onClick={() => setCmdkOpen(true)}>
          <Search className="h-3.5 w-3.5" />
          <span className="type-meta hidden sm:inline">Search...</span>
          <kbd className="pointer-events-none hidden h-4 select-none items-center gap-0.5 rounded border border-white/[0.08] bg-white/[0.03] px-1 font-mono text-[10px] leading-none sm:flex">
            &#8984;K
          </kbd>
        </Button>

        {/* Mobile: search icon only */}
        <Button variant="ghost" size="icon-sm" className="md:hidden" aria-label="Search" onClick={() => setCmdkOpen(true)}>
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
        <div className="hidden md:flex items-center gap-0.5">
          <ActivityBell />
          <InboxBell />
        </div>

        {/* Desktop: user menu */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button className="hidden md:flex items-center gap-2 rounded-md px-1.5 py-1 hover:bg-accent transition-colors" aria-label="User menu">
              <UserAvatar name={userName} email={userEmail} src={userAvatar} className="h-7 w-7" textClassName="text-micro" />
              <span className="text-xs font-medium hidden sm:inline">{userName.split(" ")[0]}</span>
              <ChevronDown className="h-3 w-3 text-muted-foreground hidden sm:block" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-64">
            <div className="px-2 py-3">
              <div className="flex items-center gap-3">
                <UserAvatar name={userName} email={userEmail} src={userAvatar} className="h-10 w-10" textClassName="text-sm" />
                <div>
                  <div className="text-sm font-medium">{userName}</div>
                  <div className="text-xs text-muted-foreground">{userEmail}</div>
                </div>
              </div>
              <div className="flex items-center gap-1.5 mt-2">
                <Badge variant="outline" className="text-micro px-1.5 py-0.5">Owner</Badge>
                <span className="text-micro text-muted-foreground">Unify Technology</span>
              </div>
            </div>
            <DropdownMenuSeparator />
            <DropdownMenuItem asChild className="gap-3 text-xs">
              <Link href="/settings">
                <User className="h-4 w-4 text-muted-foreground" />
                Profile & Settings
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem asChild className="gap-3 text-xs">
              <a href={SUPPORT_URL} target="_blank" rel="noopener noreferrer">
                <HelpCircle className="h-4 w-4 text-muted-foreground" />
                Help & Support
              </a>
            </DropdownMenuItem>
            <DropdownMenuItem asChild className="gap-3 text-xs">
              <a href={DOCS_URL} target="_blank" rel="noopener noreferrer">
                <BookOpen className="h-4 w-4 text-muted-foreground" />
                Documentation
              </a>
            </DropdownMenuItem>
            <DropdownMenuItem asChild className="gap-3 text-xs">
              <a href={GITHUB_URL} target="_blank" rel="noopener noreferrer">
                <GitBranch className="h-4 w-4 text-muted-foreground" />
                GitHub
              </a>
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="gap-3 text-xs text-destructive" onClick={() => { signOut().then(() => window.location.href = "/login") }}>
              <LogOut className="h-4 w-4" />
              Log out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        {/* Mobile: hamburger for main navigation */}
        <Button variant="ghost" size="icon" className="h-8 w-8 md:hidden" aria-label="Navigation" onClick={() => setMobileNavOpen(true)}>
          <Menu className="h-4 w-4" />
        </Button>
      </div>

      {/* Mobile: main navigation bottom sheet */}
      {isMobile && (
        <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
          <SheetContent side="bottom" showCloseButton={false} className="rounded-t-2xl max-h-[85vh] p-0">
            <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-1" />
            <SheetHeader className="px-4 py-2 border-b">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <div className="flex h-6 w-6 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground">U</div>
                  <SheetTitle className="text-sm">Unify Technology</SheetTitle>
                </div>
                <button onClick={() => setMobileNavOpen(false)} aria-label="Close navigation" className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
                  <X className="h-4 w-4" />
                </button>
              </div>
            </SheetHeader>
            <div className="flex-1 overflow-y-auto py-2">
              {mobileNavSections.map((section) => (
                <div key={section.label}>
                  <div className="px-3 py-1 text-micro uppercase tracking-wider font-semibold text-muted-foreground">{section.label}</div>
                  {section.items
                    // Admin console floor is ADMIN+ (#868/#893), matching the sidebar + backend.
                    .filter((item) => !("adminOnly" in item && item.adminOnly && role !== "OWNER" && role !== "ADMIN"))
                    .map((item) => {
                      const isActive = pathname === item.href || (item.href !== "/" && pathname.startsWith(item.href))
                      const disabled = "disabled" in item && item.disabled
                      return (
                        <Link
                          key={item.href}
                          href={disabled ? "#" : item.href}
                          onClick={() => !disabled && setMobileNavOpen(false)}
                          className={`w-full flex items-center gap-3 px-4 py-2.5 text-sm transition-colors ${
                            disabled
                              ? "text-muted-foreground-soft pointer-events-none"
                              : isActive
                                ? "bg-accent text-foreground font-medium"
                                : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
                          }`}
                        >
                          <item.icon className="h-4 w-4" />
                          {item.title}
                          {disabled && <span className="text-micro bg-muted px-1.5 rounded ml-auto">FUTURE</span>}
                        </Link>
                      )
                    })}
                </div>
              ))}
            </div>
            <div className="border-t p-4">
              <div className="flex items-center gap-3">
                <UserAvatar name={userName} email={userEmail} src={userAvatar} className="h-8 w-8" textClassName="text-micro" />
                <div className="flex-1 min-w-0">
                  <div className="text-xs font-medium">{userName}</div>
                  <div className="text-micro text-muted-foreground">{userEmail}</div>
                </div>
                <button
                  onClick={() => { signOut().then(() => window.location.href = "/login") }}
                  className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent text-muted-foreground"
                  aria-label="Log out"
                >
                  <LogOut className="h-4 w-4" />
                </button>
              </div>
            </div>
          </SheetContent>
        </Sheet>
      )}

      <CommandPalette open={cmdkOpen} onOpenChange={setCmdkOpen} />
    </header>
  )
}

/**
 * Toolbar provisioning badge — single source of UI truth for "an image
 * somewhere needs work".
 *
 * Sits at the LEFT edge of the status group so the fixed Online / Crews
 * pills don't reflow when it appears. Click opens a popover whose rows
 * are state-aware:
 *   - needs_provision → Build now button
 *   - running         → live step/total progress bar + last message
 *   - failed          → error + Retry button
 *
 * Crew name is a Link to the canvas (where the user can edit config or
 * see the full provisioning banner); action buttons live on the row
 * itself so the user never has to navigate away to act.
 */
