"use client"

import Link from "next/link"
import Image from "next/image"
import { usePathname } from "next/navigation"
import {
  LayoutDashboard,
  Bot,
  Key,
  Zap,
  Settings,
  Network,
  Workflow,
  Activity,
  Shield,
  Store,
  ShieldCheck,
  ChevronsLeft,
  ChevronsRight,
} from "lucide-react"
import { useAbilities } from "@/hooks/use-abilities"
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
  useSidebar,
} from "@/components/ui/sidebar"

const navSections = [
  {
    label: "Work",
    items: [
      { title: "Dashboard", href: "/", icon: LayoutDashboard },
      { title: "Orchestration", href: "/orchestration", icon: Workflow, badge: "FUTURE" as const },
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
  const { toggleSidebar } = useSidebar()

  return (
    <Sidebar variant="sidebar" collapsible="icon">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild>
              <Link href="/">
                <Image
                  src="/logo.svg"
                  alt="Crewship"
                  width={28}
                  height={28}
                  className="shrink-0"
                />
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-semibold">Crewship</span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        {navSections.map((section) => (
          <SidebarGroup key={section.label}>
            <SidebarGroupLabel>{section.label}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {section.items
                  .filter((item) => {
                    if (item.badge === "OWNER" && role !== "OWNER") return false
                    return true
                  })
                  .map((item) => (
                    <SidebarMenuItem key={item.href}>
                      <SidebarMenuButton
                        asChild={item.badge !== "FUTURE"}
                        disabled={item.badge === "FUTURE"}
                        isActive={
                          pathname === item.href ||
                          (item.href !== "/" && pathname.startsWith(item.href))
                        }
                      >
                        {item.badge === "FUTURE" ? (
                          <span className="flex items-center gap-2 opacity-50">
                            <item.icon />
                            <span>{item.title}</span>
                          </span>
                        ) : (
                          <Link href={item.href}>
                            <item.icon />
                            <span>{item.title}</span>
                          </Link>
                        )}
                      </SidebarMenuButton>
                      {item.badge === "FUTURE" && (
                        <SidebarMenuBadge className="text-[9px] bg-muted text-muted-foreground px-1.5">
                          FUTURE
                        </SidebarMenuBadge>
                      )}
                    </SidebarMenuItem>
                  ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        ))}
      </SidebarContent>

      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton onClick={toggleSidebar} aria-label="Toggle sidebar" className="justify-center group-data-[collapsible=icon]:justify-center">
              <ChevronsLeft className="group-data-[collapsible=icon]:hidden" />
              <span className="group-data-[collapsible=icon]:hidden">Collapse</span>
              <ChevronsRight className="hidden group-data-[collapsible=icon]:block" />
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
    </Sidebar>
  )
}
