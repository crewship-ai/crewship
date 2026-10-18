"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { PanelLeft, PanelLeftClose, PanelLeftDashed, Search, type LucideIcon } from "lucide-react"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { navSections, visibleItems } from "@/lib/nav-sections"
import { useInboxUnreadCount } from "@/hooks/use-inbox"
import { useRestrictedSurfaces, useTrustedWorkspaceId } from "@/hooks/use-access-mode"
import { useAbilities } from "@/hooks/use-abilities"
import { useAuthAvailable, useIsInstanceAdmin } from "@/hooks/use-auth"
import { WorkspaceSwitcher } from "@/components/layout/workspace-switcher"
import { RAIL_ROW, RailCount, RailGroupHead, RailLabel, RailTile, conceptOf } from "@/components/layout/rail"
import { UserMenu } from "@/components/layout/user-menu"
import { useAppStore } from "@/lib/store"
import type { NavItem } from "@/lib/nav-sections"
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
  // Live unread count for the Inbox row — shared with the top-bar bell so
  // they stay in lockstep without two pollers.
  // /inbox/count and /system/* are not on the restricted allowlist.
  const trustedWorkspaceId = useTrustedWorkspaceId()
  const inboxUnread = useInboxUnreadCount(trustedWorkspaceId)
  // A restricted session sees only the screens the server says it may open.
  const surfaces = useRestrictedSurfaces()
  const authAvailable = useAuthAvailable()
  const visible = (items: NavItem[]) => visibleItems(items, { role, instanceAdmin, surfaces })
  const row = (item: NavItem) => {
    const isActive = pathname === item.href || (item.href !== "/" && pathname.startsWith(item.href))
    const unread = item.href === "/inbox" ? inboxUnread : 0
    return (
      <SidebarMenuItem key={item.href}>
        <SidebarMenuButton asChild isActive={isActive} tooltip={item.title} className={RAIL_ROW}>
          <Link href={item.href} aria-label={unread ? `${item.title}, ${unread > 99 ? "99+" : unread} unread` : undefined}>
            <RailTile icon={item.icon} concept={conceptOf(item.href)} active={isActive} badge={unread} />
            <RailLabel>{item.title}</RailLabel>
            <RailCount value={unread} />
          </Link>
        </SidebarMenuButton>
      </SidebarMenuItem>
    )
  }
  const main = navSections.filter((s) => s.placement !== "footer")
  const footer = navSections.filter((s) => s.placement === "footer").flatMap((s) => visible(s.items))

  return (
    <Sidebar variant="inset" collapsible="icon">
      <SidebarHeader className="rail-inset gap-1.5 pb-1 pt-3">
        <WorkspaceSwitcher />
        <SidebarSearch />
      </SidebarHeader>

      <SidebarContent className="rail-inset gap-0! overflow-x-hidden! pb-2">
        {main.map((section, i) => {
          const items = visible(section.items)
          if (items.length === 0) return null
          return (
            <SidebarGroup key={section.label || `group-${i}`} className="p-0!" aria-label={section.label || "Main"}>
              <RailGroupHead label={section.label} />
              <SidebarMenu className="gap-px">{items.map(row)}</SidebarMenu>
            </SidebarGroup>
          )
        })}
      </SidebarContent>

      {/* Sidebar mode, Settings and Admin, then — below a rule — who you are. */}
      <SidebarFooter className="rail-inset gap-px border-t border-sidebar-border pb-2 pt-2">
        <SidebarMenu className="gap-px">
          <SidebarModeMenu />
          {footer.map(row)}
        </SidebarMenu>
        {authAvailable && <UserMenu placement="sidebar" />}
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}

/** The search field under the workspace switcher. It opens the ⌘K palette;
 *  the key itself is owned by AppToolbar. */
function SidebarSearch() {
  const open = useAppStore((s) => s.setCommandPaletteOpen)
  return (
    <button type="button" className="rail-search" onClick={() => open(true)} aria-label="Search" aria-keyshortcuts="Meta+K">
      <Search aria-hidden />
      <span className="rail-label min-w-0 flex-1 truncate text-left">Search</span>
      <kbd className="rail-label font-mono text-micro">⌘K</kbd>
    </button>
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
  { key: "pinned", label: "Pinned open", hint: "Always open with names", icon: PanelLeft },
  { key: "hover", label: "Rail · expand on hover", hint: "Icons, names under the pointer", icon: PanelLeftDashed },
  { key: "collapsed", label: "Rail", hint: "Icons only, never opens", icon: PanelLeftClose },
]

/**
 * The sidebar's three modes as one row with a menu, the same in the rail and
 * the open panel: a mode kept behind the open panel's "…" could not be
 * reached from Collapsed, the one mode that never opens. ⌘B pins and unpins,
 * returning to whichever rail mode was chosen.
 */
function SidebarModeMenu() {
  const { sidebarMode, setSidebarMode, togglePinned, setPeekOnHover, state } = useSidebar()
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
            <RailTile icon={current.icon} />
            <RailLabel>{current.label}</RailLabel>
            <kbd className="rail-label ml-auto font-mono text-micro text-muted-foreground-soft">⌘B</kbd>
          </SidebarMenuButton>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          side={state === "expanded" ? "top" : "right"}
          align={state === "expanded" ? "start" : "end"}
          sideOffset={state === "expanded" ? 6 : 8}
          className="w-[280px]"
        >
          <DropdownMenuLabel className="text-micro font-semibold text-muted-foreground">Sidebar</DropdownMenuLabel>
          <DropdownMenuRadioGroup value={sidebarMode} onValueChange={choose}>
            {SIDEBAR_MODES.map(({ key, label, hint, icon: Icon }) => (
              <DropdownMenuRadioItem key={key} value={key} className="items-center gap-2.5">
                <span data-slot="mode-tile" className="grid size-6 shrink-0 place-items-center rounded-md bg-muted text-muted-foreground">
                  <Icon className="size-3.5" />
                </span>
                <span className="grid min-w-0 flex-1 leading-tight">
                  <b className="truncate font-semibold">{label}</b>
                  <span className="truncate text-micro text-muted-foreground">{hint}</span>
                </span>
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
          <DropdownMenuSeparator />
          <p className="flex items-center gap-1.5 px-2 py-1 text-micro text-muted-foreground">
            <kbd className="rounded border border-border bg-card px-1 font-mono text-micro text-muted-foreground-soft">⌘B</kbd>
            pins and unpins
          </p>
        </DropdownMenuContent>
      </DropdownMenu>
    </SidebarMenuItem>
  )
}
