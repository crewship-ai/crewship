"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { PanelLeft, PanelLeftClose, PanelLeftDashed, type LucideIcon } from "lucide-react"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { navSections, isHiddenForRole } from "@/lib/nav-sections"
import { useInboxUnreadCount } from "@/hooks/use-inbox"
import { useAccessMode, useRestrictedSurfaces, useTrustedWorkspaceId } from "@/hooks/use-access-mode"
import { restrictedPathAllowed } from "@/lib/restricted-surfaces"
import { useAbilities } from "@/hooks/use-abilities"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { WorkspaceSwitcher } from "@/components/layout/workspace-switcher"
import { SidebarVersion } from "@/components/layout/sidebar-version"
import { RAIL_ROW, RailGroupHead, RailLabel, RailTile, conceptOf } from "@/components/layout/rail"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuLabel, DropdownMenuRadioGroup, DropdownMenuRadioItem,
  DropdownMenuSeparator, DropdownMenuTrigger,
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
  // /inbox/count and /system/* are not on the restricted allowlist.
  const trustedWorkspaceId = useTrustedWorkspaceId()
  const trusted = useAccessMode() === "trusted"
  const inboxUnread = useInboxUnreadCount(trustedWorkspaceId)
  // A restricted session sees only the screens the server says it may open.
  const surfaces = useRestrictedSurfaces()

  return (
    <Sidebar variant="sidebar" collapsible="icon">
      <SidebarHeader className="rail-inset pb-1 pt-3">
        <WorkspaceSwitcher />
      </SidebarHeader>

      <SidebarContent className="rail-inset gap-0! overflow-x-hidden! pb-2">
        {navSections.map((section) => {
          const items = section.items.filter((item) => !isHiddenForRole(item, role, instanceAdmin) && (!surfaces || restrictedPathAllowed(item.href, surfaces)))
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
          <SidebarModeMenu />
        </SidebarMenu>
        <SidebarSeparator className="mx-0" />
        <SidebarMenu>
          {trusted && <SidebarVersion />}
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
type SidebarModeKey = "pinned" | "hover" | "collapsed"
const SIDEBAR_MODES: { key: SidebarModeKey; label: string; hint: string; icon: LucideIcon }[] = [
  { key: "pinned", label: "Expanded", hint: "Always open with names", icon: PanelLeft },
  { key: "hover", label: "Expand on hover", hint: "Icons, names under the pointer", icon: PanelLeftDashed },
  { key: "collapsed", label: "Collapsed", hint: "Icons only, never opens", icon: PanelLeftClose },
]

/**
 * The sidebar's three modes as one row with a menu, the same in the rail and
 * the open panel: a mode kept behind the open panel's "…" could not be
 * reached from Collapsed, the one mode that never opens. ⌘B pins and unpins,
 * returning to whichever rail mode was chosen.
 */
function SidebarModeMenu() {
  const { sidebarMode, setSidebarMode, togglePinned, setPeekOnHover } = useSidebar()
  const current = SIDEBAR_MODES.find((m) => m.key === sidebarMode) ?? SIDEBAR_MODES[1]
  const choose = (key: string) => {
    if (key === "pinned") {
      if (sidebarMode !== "pinned") togglePinned()
      return
    }
    setPeekOnHover(key === "hover")
    setSidebarMode(key as SidebarModeKey)
  }
  return (
    <SidebarMenuItem>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <SidebarMenuButton tooltip={`Sidebar: ${current.label}`} aria-label={`Sidebar: ${current.label}`} className={RAIL_ROW}>
            <RailTile icon={current.icon} className="rail-tile-static" />
            <RailLabel className="text-[12.5px] text-muted-foreground">{current.label}</RailLabel>
            <kbd className="rail-label mr-2 font-mono text-[10px] text-muted-foreground-soft">⌘B</kbd>
          </SidebarMenuButton>
        </DropdownMenuTrigger>
        <DropdownMenuContent side="right" align="end" sideOffset={8} className="w-60">
          <DropdownMenuLabel className="text-[11px] font-normal text-muted-foreground">Sidebar</DropdownMenuLabel>
          <DropdownMenuRadioGroup value={sidebarMode} onValueChange={choose}>
            {SIDEBAR_MODES.map(({ key, label, hint, icon: Icon }) => (
              <DropdownMenuRadioItem key={key} value={key} className="items-start gap-2">
                <Icon className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                <span className="grid leading-tight">
                  <span>{label}</span>
                  <span className="text-[11px] text-muted-foreground">{hint}</span>
                </span>
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
          <DropdownMenuSeparator />
          <p className="px-2 py-1 text-[11px] text-muted-foreground">⌘B pins and unpins</p>
        </DropdownMenuContent>
      </DropdownMenu>
    </SidebarMenuItem>
  )
}
