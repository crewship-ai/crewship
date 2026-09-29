"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { MoreHorizontal, PanelLeftClose, PanelLeftOpen } from "lucide-react"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { navSections, isHiddenForRole } from "@/lib/nav-sections"
import { useInboxUnreadCount } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { WorkspaceSwitcher } from "@/components/layout/workspace-switcher"
import { SidebarVersion } from "@/components/layout/sidebar-version"
import { RAIL_ROW, RailGroupHead, RailLabel, RailTile, conceptOf } from "@/components/layout/rail"
import {
  DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
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
  // Live unread count for the Inbox tile — shared with the top-bar bell so
  // they stay in lockstep without two pollers.
  const { workspaceId } = useWorkspace()
  const inboxUnread = useInboxUnreadCount(workspaceId)

  return (
    <Sidebar variant="sidebar" collapsible="icon">
      <SidebarHeader className="rail-inset pb-1 pt-3">
        <WorkspaceSwitcher />
      </SidebarHeader>

      <SidebarContent className="rail-inset gap-0! overflow-x-hidden! pb-2">
        {navSections.map((section) => {
          const items = section.items.filter((item) => !isHiddenForRole(item, role, instanceAdmin))
          if (items.length === 0) return null
          return (
            <SidebarGroup key={section.label} className="p-0!" aria-label={section.label}>
              <RailGroupHead label={section.label} />
              <SidebarMenu className="gap-1">
                {items.map((item) => {
                  const isActive = pathname === item.href || (item.href !== "/" && pathname.startsWith(item.href))
                  // FUTURE is announced, not built — the row reads as a
                  // destination but must not navigate anywhere.
                  if (item.badge === "FUTURE") {
                    return (
                      <SidebarMenuItem key={item.href} className="opacity-50">
                        <SidebarMenuButton disabled isActive={false} tooltip={`${item.title} · soon`} className={RAIL_ROW}>
                          <RailTile icon={item.icon} concept={conceptOf(item.href)} className="rail-tile-static" />
                          <RailLabel>{item.title}</RailLabel>
                          <span className="rail-label mr-2 rounded-full bg-muted px-1.5 font-mono text-[9.5px] text-muted-foreground">SOON</span>
                        </SidebarMenuButton>
                      </SidebarMenuItem>
                    )
                  }
                  const unread = item.href === "/inbox" ? inboxUnread : 0
                  return (
                    <SidebarMenuItem key={item.href}>
                      <SidebarMenuButton asChild isActive={isActive} tooltip={item.title} className={RAIL_ROW}>
                        <Link href={item.href} aria-label={unread ? `${item.title}, ${unread > 99 ? "99+" : unread} unread` : undefined}>
                          <RailTile icon={item.icon} concept={conceptOf(item.href)} active={isActive} badge={unread} />
                          <RailLabel>{item.title}</RailLabel>
                        </Link>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  )
                })}
              </SidebarMenu>
            </SidebarGroup>
          )
        })}
      </SidebarContent>

      {/* Pin / collapse, then — below a rule — which Crewship this is. */}
      <SidebarFooter className="rail-inset gap-1 pb-3 pt-2">
        <SidebarMenu>
          <PinToggle />
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

/**
 * The rail's one control: pin the panel open, or collapse it to the rail
 * (⌘B). Whether the rail expands on hover is a preference in its menu, not a
 * third state to cycle through — the old button walked hover → pinned →
 * collapsed and nobody could say which one a click would land on.
 */
function PinToggle() {
  const { sidebarMode, togglePinned, peekOnHover, setPeekOnHover } = useSidebar()
  const pinned = sidebarMode === "pinned"
  const label = pinned ? "Collapse" : "Pin open"
  return (
    <SidebarMenuItem className="flex items-center">
      <SidebarMenuButton onClick={togglePinned} tooltip={`${label} · ⌘B`} aria-label={`${label} the sidebar`} className={RAIL_ROW}>
        <RailTile icon={pinned ? PanelLeftClose : PanelLeftOpen} className="rail-tile-static" />
        <RailLabel className="text-[12.5px] text-muted-foreground">{label}</RailLabel>
        <kbd className="rail-label mr-8 font-mono text-[10px] text-muted-foreground-soft">⌘B</kbd>
      </SidebarMenuButton>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" aria-label="Sidebar options"
            className="rail-label absolute right-0 grid h-7 w-7 place-items-center rounded-md text-muted-foreground hover:bg-sidebar-accent hover:text-foreground group-data-[collapsible=icon]:pointer-events-none">
            <MoreHorizontal className="h-3.5 w-3.5" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent side="top" align="end" className="w-52">
          <DropdownMenuCheckboxItem checked={peekOnHover} onCheckedChange={(v) => setPeekOnHover(v === true)}>
            Expand on hover
          </DropdownMenuCheckboxItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </SidebarMenuItem>
  )
}
