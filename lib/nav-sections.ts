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
  /**
   * FUTURE renders the row disabled — the destination is announced, not built.
   * ADMIN hides the row below the console's ADMIN+ floor (#868/#893).
   */
  badge?: "FUTURE" | "ADMIN"
}

export interface NavSection {
  label: string
  items: NavItem[]
}

export const navSections: NavSection[] = [
  {
    label: "Plan",
    items: [
      { title: "Dashboard", href: "/", icon: CONCEPT_ICON.dashboard },
      { title: "Inbox", href: "/inbox", icon: CONCEPT_ICON.inbox },
      // Chat is under Plan, next to Inbox: both are "someone is waiting on a
      // reply". It points at the index, never at /chat/<slug> — a nav row
      // cannot know which agent, and the index is what answers that.
      //
      // The label is "Chat" (PRD O5 lists Chat / Talk / Conversations as open).
      // It is the word the product already uses for the surface — the toolbar
      // breadcrumb, the route and the panel component all say chat — so a
      // different noun in the rail would be the only place that disagrees.
      { title: "Chat", href: "/chat", icon: CONCEPT_ICON.sessions },
      { title: "Issues", href: "/issues", icon: CONCEPT_ICON.issues },
      { title: "Routines", href: "/routines", icon: CONCEPT_ICON.routines },
      // Plan, after Routines: a page is where a person goes to see the state
      // of their work, not a thing they build once (docs/prd/pages.md §9b.5).
      { title: "Pages", href: "/pages", icon: CONCEPT_ICON.pages },
    ],
  },
  {
    label: "Run",
    items: [
      { title: "Activity", href: "/activity", icon: CONCEPT_ICON.activity },
      { title: "Journal", href: "/journal", icon: CONCEPT_ICON.journal },
    ],
  },
  {
    label: "Build",
    items: [
      { title: "Crews", href: "/crews", icon: CONCEPT_ICON.crews },
      { title: "Skills", href: "/skills", icon: CONCEPT_ICON.skills },
      { title: "Credentials", href: "/credentials", icon: CONCEPT_ICON.credentials },
      { title: "Integrations", href: "/integrations", icon: CONCEPT_ICON.integrations },
    ],
  },
  {
    label: "System",
    items: [
      { title: "Marketplace", href: "/marketplace", icon: CONCEPT_ICON.marketplace, badge: "FUTURE" },
      { title: "Settings", href: "/settings", icon: CONCEPT_ICON.settings },
      { title: "Admin", href: "/admin", icon: CONCEPT_ICON.admin, badge: "ADMIN" },
    ],
  },
]

/** True when a role may not see this row at all. Shared by both surfaces. */
export function isHiddenForRole(item: NavItem, role: string | null | undefined): boolean {
  return item.badge === "ADMIN" && role !== "OWNER" && role !== "ADMIN"
}
