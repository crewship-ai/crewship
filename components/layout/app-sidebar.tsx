"use client"

import type * as React from "react"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { PanelLeftClose, Pin, MousePointer2 } from "lucide-react"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { accentFor } from "@/lib/concept-accents"
import { navSections, isHiddenForRole } from "@/lib/nav-sections"
import { useInboxUnreadCount } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { WorkspaceSwitcher } from "@/components/layout/workspace-switcher"
import { SidebarVersion } from "@/components/layout/sidebar-version"
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
  SidebarSeparator,
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
 * Re-exported for the tests and callers that have always imported it from
 * here. The definition lives in lib/nav-sections, which the phone sheet reads
 * too — see the comment there for why there is exactly one of these now.
 */
export { navSections }

export function AppSidebar() {
  const pathname = usePathname()
  const { role } = useAbilities()
  const instanceAdmin = useIsInstanceAdmin()
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
              <SidebarMenu className={RAIL_MENU}>
                {section.items
                  .filter((item) => !isHiddenForRole(item, role, instanceAdmin))
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
                            className={RAIL_BUTTON}
                          >
                            <RailTile icon={item.icon} href={item.href} active={false} />
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
                          className={RAIL_BUTTON}
                        >
                          <Link href={item.href}>
                            <RailTile icon={item.icon} href={item.href} active={isActive} />
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

      {/* The sidebar mode toggle, then — below a rule, at the very bottom —
          which Crewship this is. */}
      <SidebarFooter className="gap-1.5 p-2">
        <SidebarMenu className={RAIL_MENU}>
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
        <SidebarSeparator className="mx-0" />
        <SidebarMenu>
          <SidebarVersion />
        </SidebarMenu>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}

/** In icon mode the tile IS the button: no padding, no second background, and
 *  no gap — the collapsed label keeps zero width but a flex gap still pushed
 *  the tile half a gap (3.7px) left of centre. */
const RAIL_BUTTON =
  "group-data-[collapsible=icon]:w-9! group-data-[collapsible=icon]:h-9! group-data-[collapsible=icon]:gap-0! group-data-[collapsible=icon]:p-0! group-data-[collapsible=icon]:bg-transparent! group-data-[collapsible=icon]:hover:bg-transparent!"

/** Icon mode centres each tile in the rail; the list is otherwise left-aligned. */
const RAIL_MENU = "group-data-[collapsible=icon]:items-center"

/** The concept a destination stands for, so its tile wears that colour. */
function conceptOf(href: string): string {
  if (href === "/") return "dashboard"
  const first = href.slice(1).split("/")[0]
  return first === "chat" ? "sessions" : first
}

/**
 * A destination's icon in a Harbor tile, the same one a nested page's collapsed
 * panel shows (DrillPage): the page you are on is tinted in its concept's
 * colour, the rest stay neutral and take the tint on hover, with the tile's
 * lift and tilt. globals.css `.rail-tile`.
 */
function RailTile({ icon: Icon, href, active }: { icon: React.ElementType; href: string; active: boolean }) {
  return (
    <span
      data-slot="rail-tile"
      data-active={active ? "true" : undefined}
      className="rail-tile"
      style={{ "--ic": accentFor(conceptOf(href)).tint } as React.CSSProperties}
      aria-hidden
    >
      <Icon />
    </span>
  )
}
