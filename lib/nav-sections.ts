import { roleAtLeast } from "@/lib/routine-governance"
import { restrictedHome, restrictedPathAllowed, type RestrictedSurface } from "@/lib/restricted-surfaces"
import type { LucideIcon } from "lucide-react"

import { CONCEPT_ICON } from "@/lib/concept-icons"

/**
 * The application's navigation, defined once.
 *
 * There used to be two of these — `navSections` in the desktop rail and
 * `mobileNavSections` in the toolbar's phone sheet — kept in step by hand. The
 * comment above each one admitted as much and named the incident where Chat
 * went missing from both. The mechanism outlived the fix: by #2483 the phone
 * sheet had lost Inbox, Issues, Routines, Pages, Activity, Journal and
 * Integrations, and the desktop rail had never noticed.
 *
 * Both surfaces now render this array. A destination cannot exist on one
 * breakpoint and not the other, because there is only one place to add it.
 */
export interface NavItem {
  title: string
  href: string
  icon: LucideIcon
  /** ADMIN hides the row from anyone who is not an instance administrator. */
  badge?: "ADMIN"
  /**
   * The lowest workspace role that sees the row. The Journal is the raw
   * technical log; Activity is the readable record for everyone (#2979).
   */
  minRole?: "MANAGER" | "ADMIN"
}

export interface NavSection {
  /** Group heading; empty for the ungrouped places you start from. */
  label: string
  items: NavItem[]
  /** "footer" rows sit at the bottom of the sidebar (Settings, Admin). The
   *  phone sheet lists every section, footer included. */
  placement?: "footer"
}

/**
 * Harbor 2 (#3350): the places you start from have no heading (Dashboard,
 * Inbox, Chat, Activity); then **Work** (what gets done) and **Build** (who
 * does it and with what). Settings and Admin live in the sidebar footer with
 * the sidebar mode and the user menu. Marketplace — announced, never built —
 * is gone. The Journal is part of Activity (F-02): its admin-only views are
 * gated there and old /journal links redirect. "Vault" is the label of Credentials (same route); "Pages" keeps its
 * name until the Artifacts delivery renames it.
 */
export const navSections: NavSection[] = [
  {
    label: "",
    items: [
      { title: "Dashboard", href: "/", icon: CONCEPT_ICON.dashboard },
      { title: "Inbox", href: "/inbox", icon: CONCEPT_ICON.inbox },
      // Points at the index, never at /chat/<slug> — a nav row cannot know
      // which agent, and the index is what answers that.
      { title: "Chat", href: "/chat", icon: CONCEPT_ICON.sessions },
      { title: "Activity", href: "/activity", icon: CONCEPT_ICON.activity },
    ],
  },
  {
    label: "Work",
    items: [
      { title: "Issues", href: "/issues", icon: CONCEPT_ICON.issues },
      { title: "Routines", href: "/routines", icon: CONCEPT_ICON.routines },
      { title: "Pages", href: "/pages", icon: CONCEPT_ICON.pages },
      // F-01: the item arrives with the Calendar page itself (ODCH-12), last
      // in Work as SHL-01 orders it.
      { title: "Calendar", href: "/calendar", icon: CONCEPT_ICON.calendar },
    ],
  },
  {
    label: "Build",
    items: [
      { title: "Crews", href: "/crews", icon: CONCEPT_ICON.crews },
      { title: "Skills", href: "/skills", icon: CONCEPT_ICON.skills },
      { title: "Vault", href: "/credentials", icon: CONCEPT_ICON.credentials },
      { title: "Integrations", href: "/integrations", icon: CONCEPT_ICON.integrations },
    ],
  },
  {
    label: "",
    placement: "footer",
    items: [
      { title: "Settings", href: "/settings", icon: CONCEPT_ICON.settings },
      { title: "Admin", href: "/admin", icon: CONCEPT_ICON.admin, badge: "ADMIN" },
    ],
  },
]

/**
 * True when this person may not see the row at all. Shared by both surfaces.
 * Admin is the instance console: it shows for an instance administrator, and a
 * workspace role (OWNER or ADMIN of the current workspace) does not decide it.
 */
export function isHiddenForRole(item: NavItem, role: string | null | undefined, instanceAdmin?: boolean | null): boolean {
  if (item.badge === "ADMIN" && instanceAdmin !== true) return true
  // While the role is loading it is null, and the row stays hidden: showing it
  // and then taking it away is worse than letting it appear.
  return item.minRole != null && !roleAtLeast(role, item.minRole)
}

/** Who is looking: the inputs every navigation surface filters by. */
export interface NavViewer {
  /** Workspace role; null while it loads. */
  role: string | null | undefined
  instanceAdmin?: boolean | null
  /** The screens a restricted (Guest) session may open; null for a full one. */
  surfaces: readonly RestrictedSurface[] | null
}

/**
 * Every row this person sees, in navigation order, footer rows (Settings,
 * Admin) last. The sidebar, the phone More sheet and the tab bar all filter
 * through here, so the role x destination matrix has one definition and one
 * test (lib/__tests__/nav-role-matrix.test.ts).
 */
export function visibleNavItems(viewer: NavViewer, sections: NavSection[] = navSections): NavItem[] {
  return sections.flatMap((s) => visibleItems(s.items, viewer))
}

/** One section's rows for this person. */
export function visibleItems(items: NavItem[], { role, instanceAdmin, surfaces }: NavViewer): NavItem[] {
  return items.filter((item) => !isHiddenForRole(item, role, instanceAdmin) && (!surfaces || restrictedPathAllowed(item.href, surfaces)))
}

/**
 * The phone tab bar for this person: a Guest keeps only granted tabs. A
 * restricted session has no Dashboard, so Home leads to its own home
 * (`restrictedHome`, SHL-07 / DSH-02) instead of vanishing; a tab that would
 * point at the same place as Home is dropped.
 */
export function visiblePhoneTabs({ surfaces }: Pick<NavViewer, "surfaces"> & Partial<NavViewer>): NavItem[] {
  if (!surfaces) return phoneTabs()
  const home = restrictedHome(surfaces)
  return phoneTabs().flatMap((tab) => {
    if (tab.href === "/") return home ? [{ ...tab, href: home }] : []
    return tab.href !== home && restrictedPathAllowed(tab.href, surfaces) ? [tab] : []
  })
}

/**
 * The phone tab bar's destinations (Harbor 2): Home, Inbox, Chat and Issues;
 * the bar's fifth slot, More, opens the full navigation in a sheet.
 *
 * Hrefs, not copies — the label and icon still come from `navSections`, so a
 * rename cannot leave the tab bar saying something the sidebar does not.
 */
export const PHONE_TAB_HREFS = ["/", "/inbox", "/chat", "/issues"] as const

export function phoneTabs(): NavItem[] {
  const byHref = new Map(navSections.flatMap((s) => s.items).map((i) => [i.href, i]))
  return PHONE_TAB_HREFS.map((href) => {
    const item = byHref.get(href)
    if (!item) throw new Error(`PHONE_TAB_HREFS names ${href}, which navSections does not carry`)
    // A tab is one short word: the Dashboard reads "Home" there.
    return href === "/" ? { ...item, title: "Home" } : item
  })
}

/** The destination a path belongs to — the area the top bar names. */
export function areaOf(pathname: string): NavItem | undefined {
  const items = navSections.flatMap((s) => s.items)
  return (
    items.find((i) => i.href !== "/" && (pathname === i.href || pathname.startsWith(`${i.href}/`))) ??
    (pathname === "/" ? items.find((i) => i.href === "/") : undefined)
  )
}
