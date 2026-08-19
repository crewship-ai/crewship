"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { PanelLeftClose, Pin, MousePointer2 } from "lucide-react"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { useInboxUnreadCount } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { WorkspaceSwitcher } from "@/components/layout/workspace-switcher"
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuBadge,
  SidebarFooter,
  SidebarRail,
  useSidebar,
} from "@/components/ui/sidebar"

/**
 * The rail's icons ARE the product's icons — every other surface used to pick
 * again from memory, so the same concept wore a different face per screen.
 * They now come from lib/concept-icons, and NAV_ICONS is re-exported so that
 * map can assert the two never drift (lib/__tests__/concept-icons.test.ts).
 */
export const NAV_ICONS = CONCEPT_ICON

/**
 * Exported so a test can ask what the navigation contains without standing up
 * the whole sidebar tree. There are two of these arrays — this one and
 * `mobileNavSections` in app-toolbar — and they are kept in step by hand, which
 * is how chat came to be absent from both.
 */
export const navSections = [
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
      { title: "Marketplace", href: "/marketplace", icon: CONCEPT_ICON.marketplace, badge: "FUTURE" as const },
      { title: "Settings", href: "/settings", icon: CONCEPT_ICON.settings },
      { title: "Admin", href: "/admin", icon: CONCEPT_ICON.admin, badge: "ADMIN" as const },
    ],
  },
]

export function AppSidebar() {
  const pathname = usePathname()
  const { role } = useAbilities()
  const { sidebarMode, setSidebarMode } = useSidebar()
  // Live unread count for the Inbox row badge — shared with the
  // top-bar bell so they stay in lockstep without two pollers.
  const { workspaceId } = useWorkspace()
  const inboxUnread = useInboxUnreadCount(workspaceId)

  return (
    <Sidebar variant="sidebar" collapsible="icon">
      <SidebarHeader className="p-2">
        <WorkspaceSwitcher />
      </SidebarHeader>

      <SidebarContent>
        {navSections.map((section) => (
          <SidebarGroup key={section.label} className="px-2 py-1 group-data-[collapsible=icon]:px-1 group-data-[collapsible=icon]:py-0.5">
            <SidebarGroupLabel>{section.label}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {section.items
                  .filter((item) => {
                    // Admin console floor is ADMIN+ (#868/#893) — keep the nav
                    // entry in lockstep so an ADMIN sees the console they can drive.
                    if (item.badge === "ADMIN" && role !== "OWNER" && role !== "ADMIN") return false
                    return true
                  })
                  .map((item) => {
                    const isActive =
                      pathname === item.href ||
                      (item.href !== "/" && pathname.startsWith(item.href))

                    if (item.badge === "FUTURE") {
                      return (
                        <SidebarMenuItem key={item.href} className="group-data-[collapsible=icon]:hidden">
                          <SidebarMenuButton
                            disabled
                            isActive={false}
                            tooltip={item.title}
                            size="sm"
                          >
                            <item.icon />
                            <span>{item.title}</span>
                          </SidebarMenuButton>
                          <SidebarMenuBadge className="text-micro bg-muted text-muted-foreground px-1.5">
                            FUTURE
                          </SidebarMenuBadge>
                        </SidebarMenuItem>
                      )
                    }

                    const showInboxBadge = item.href === "/inbox" && inboxUnread > 0

                    return (
                      <SidebarMenuItem key={item.href}>
                        <SidebarMenuButton
                          asChild
                          isActive={isActive}
                          tooltip={item.title}
                          size="sm"
                        >
                          <Link href={item.href}>
                            <item.icon />
                            <span>{item.title}</span>
                          </Link>
                        </SidebarMenuButton>
                        {showInboxBadge && (
                          <SidebarMenuBadge
                            className="bg-info/15 text-info px-1.5 text-[10px] font-semibold tabular-nums"
                            aria-label={`${inboxUnread > 99 ? "99+" : inboxUnread} unread inbox items`}
                          >
                            {inboxUnread > 99 ? "99+" : inboxUnread}
                          </SidebarMenuBadge>
                        )}
                      </SidebarMenuItem>
                    )
                  })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        ))}
      </SidebarContent>

      {/* Sidebar mode toggle */}
      <SidebarFooter className="p-2">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              onClick={() => {
                // Cycle: hover → pinned → collapsed → hover
                const next = sidebarMode === "hover" ? "pinned" : sidebarMode === "pinned" ? "collapsed" : "hover"
                setSidebarMode(next)
              }}
              aria-label={`Sidebar: ${sidebarMode}`}
              tooltip={
                sidebarMode === "hover" ? "Hover mode — click to pin"
                  : sidebarMode === "pinned" ? "Pinned — click to collapse"
                  : "Collapsed — click for hover mode"
              }
              size="sm"
            >
              {sidebarMode === "hover" ? (
                <>
                  <MousePointer2 />
                  <span>Hover</span>
                </>
              ) : sidebarMode === "pinned" ? (
                <>
                  <Pin />
                  <span>Pinned</span>
                </>
              ) : (
                <>
                  <PanelLeftClose />
                  <span>Collapsed</span>
                </>
              )}
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
