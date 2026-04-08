"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import {
  LayoutDashboard,
  Bot,
  Key,
  Plug,
  Zap,
  Settings,
  Network,
  Workflow,
  CircleDot,
  Activity,
  Shield,
  Store,
  ShieldCheck,
  PanelLeftClose,
  Pin,
  PinOff,
  MousePointer2,
  ChevronDown,
  Ship,
} from "lucide-react"
import { useAbilities } from "@/hooks/use-abilities"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
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

const navSections = [
  {
    label: "Work",
    items: [
      { title: "Dashboard", href: "/", icon: LayoutDashboard },
      { title: "Issues", href: "/issues", icon: CircleDot },
      { title: "Orchestration", href: "/orchestration", icon: Workflow },
      { title: "Fleet", href: "/fleet", icon: Ship },
      { title: "Crews", href: "/crews", icon: Network },
      { title: "Agents", href: "/agents", icon: Bot },
    ],
  },
  {
    label: "Configure",
    items: [
      { title: "Skills", href: "/skills", icon: Zap },
      { title: "Marketplace", href: "/marketplace", icon: Store, badge: "FUTURE" as const },
      { title: "Credentials", href: "/credentials", icon: Key },
      { title: "Integrations", href: "/integrations", icon: Plug },
    ],
  },
  {
    label: "Monitor",
    items: [
      { title: "Runs", href: "/runs", icon: Activity },
      { title: "Audit Log", href: "/audit", icon: Shield },
    ],
  },
  {
    label: "System",
    items: [
      { title: "Settings", href: "/settings", icon: Settings },
      { title: "Admin", href: "/admin", icon: ShieldCheck, badge: "OWNER" as const },
    ],
  },
]

export function AppSidebar() {
  const pathname = usePathname()
  const { role } = useAbilities()
  const { state, sidebarMode, setSidebarMode } = useSidebar()

  return (
    <Sidebar variant="sidebar" collapsible="icon">
      {/* Workspace switcher (replaces Crewship logo) */}
      <SidebarHeader className="p-2">
        <SidebarMenu>
          <SidebarMenuItem>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <SidebarMenuButton size="lg" tooltip="Unify Technology">
                  <div className="flex h-6 w-6 items-center justify-center rounded-md bg-primary text-[9px] font-bold text-primary-foreground shrink-0">
                    U
                  </div>
                  <div className="grid flex-1 text-left text-sm leading-tight group-data-[collapsible=icon]:hidden">
                    <span className="truncate font-semibold text-[13px]">Unify Technology</span>
                    <span className="truncate text-[10px] text-muted-foreground">3 members</span>
                  </div>
                  <ChevronDown className="h-3 w-3 text-muted-foreground shrink-0 group-data-[collapsible=icon]:hidden" />
                </SidebarMenuButton>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" side="bottom" className="w-72">
                <DropdownMenuLabel className="text-micro uppercase tracking-wider text-muted-foreground font-medium">
                  Workspaces
                </DropdownMenuLabel>
                <DropdownMenuItem className="flex items-center gap-3 py-2 bg-primary/5">
                  <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary text-micro font-bold text-primary-foreground shrink-0">
                    U
                  </div>
                  <div className="min-w-0">
                    <div className="text-xs font-medium">Unify Technology</div>
                    <div className="text-micro text-muted-foreground">3 members</div>
                  </div>
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem className="text-xs">Create workspace</DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        {navSections.map((section) => (
          <SidebarGroup key={section.label} className="px-2 py-1 group-data-[collapsible=icon]:px-1 group-data-[collapsible=icon]:py-0.5">
            <SidebarGroupLabel>{section.label}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {section.items
                  .filter((item) => {
                    if (item.badge === "OWNER" && role !== "OWNER") return false
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
