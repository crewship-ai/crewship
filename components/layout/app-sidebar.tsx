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
  Activity,
  Shield,
  Store,
  ShieldCheck,
  PanelLeftClose,
  PanelLeftOpen,
  ChevronDown,
  LogOut,
  User,
  HelpCircle,
  BookOpen,
  GitBranch,
} from "lucide-react"
import { useAbilities } from "@/hooks/use-abilities"
import { useAuth } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Badge } from "@/components/ui/badge"
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
      { title: "Orchestration", href: "/orchestration", icon: Workflow },
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

function getInitials(name: string): string {
  if (!name.trim()) return "?"
  return name
    .split(" ")
    .map((n) => n[0])
    .join("")
    .slice(0, 2)
    .toUpperCase()
}

export function AppSidebar() {
  const pathname = usePathname()
  const { role } = useAbilities()
  const { toggleSidebar, state } = useSidebar()
  const { session, signOut } = useAuth()
  const { role: wsRole } = useWorkspace()

  const userName = session?.user?.name ?? "User"
  const userEmail = session?.user?.email ?? ""
  const userInitials = getInitials(userName)

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
          <SidebarGroup key={section.label} className="px-2 py-1">
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

      {/* User menu + collapse toggle */}
      <SidebarFooter className="p-2">
        <SidebarMenu>
          {/* User dropdown */}
          <SidebarMenuItem>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <SidebarMenuButton size="lg" tooltip={userName}>
                  <div className="flex h-6 w-6 items-center justify-center rounded-full bg-primary text-[9px] font-semibold text-primary-foreground shrink-0">
                    {userInitials}
                  </div>
                  <div className="grid flex-1 text-left text-sm leading-tight group-data-[collapsible=icon]:hidden">
                    <span className="truncate font-medium text-[12px]">{userName}</span>
                    <span className="truncate text-[10px] text-muted-foreground">{userEmail}</span>
                  </div>
                  <ChevronDown className="h-3 w-3 text-muted-foreground shrink-0 group-data-[collapsible=icon]:hidden" />
                </SidebarMenuButton>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" side="top" className="w-64">
                <div className="px-2 py-3">
                  <div className="flex items-center gap-3">
                    <div className="flex h-10 w-10 items-center justify-center rounded-full bg-primary text-sm font-semibold text-primary-foreground">
                      {userInitials}
                    </div>
                    <div>
                      <div className="text-sm font-medium">{userName}</div>
                      <div className="text-xs text-muted-foreground">{userEmail}</div>
                    </div>
                  </div>
                  {wsRole && (
                    <div className="flex items-center gap-1.5 mt-2">
                      <Badge variant="outline" className="text-micro px-1.5 py-0.5">{wsRole}</Badge>
                      <span className="text-micro text-muted-foreground">Unify Technology</span>
                    </div>
                  )}
                </div>
                <DropdownMenuSeparator />
                <DropdownMenuItem asChild className="gap-3 text-xs">
                  <Link href="/settings">
                    <User className="h-4 w-4 text-muted-foreground" />
                    Profile & Settings
                  </Link>
                </DropdownMenuItem>
                <DropdownMenuItem className="gap-3 text-xs">
                  <HelpCircle className="h-4 w-4 text-muted-foreground" />
                  Help & Support
                </DropdownMenuItem>
                <DropdownMenuItem className="gap-3 text-xs">
                  <BookOpen className="h-4 w-4 text-muted-foreground" />
                  Documentation
                </DropdownMenuItem>
                <DropdownMenuItem className="gap-3 text-xs">
                  <GitBranch className="h-4 w-4 text-muted-foreground" />
                  GitHub
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  className="gap-3 text-xs text-destructive"
                  onClick={() => {
                    signOut().then(() => {
                      window.location.href = "/login"
                    })
                  }}
                >
                  <LogOut className="h-4 w-4" />
                  Log out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </SidebarMenuItem>

          {/* Collapse toggle */}
          <SidebarMenuItem>
            <SidebarMenuButton
              onClick={toggleSidebar}
              aria-label="Toggle sidebar"
              tooltip={state === "expanded" ? "Collapse" : "Expand"}
              size="sm"
            >
              {state === "expanded" ? (
                <>
                  <PanelLeftClose />
                  <span>Collapse</span>
                </>
              ) : (
                <PanelLeftOpen />
              )}
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
