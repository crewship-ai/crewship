"use client"

import { usePathname } from "next/navigation"
import { useSession, signOut } from "next-auth/react"
import { Search, Bell, BookOpen, ChevronDown, User, HelpCircle, Github, LogOut } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Badge } from "@/components/ui/badge"
import { useCrewshipdStatus } from "@/hooks/use-crewshipd-status"
import { useWorkspace } from "@/hooks/use-workspace"

const pageConfig: Record<string, { title: string; breadcrumb?: string; pills?: { label: string; variant: "default" | "secondary" | "outline" | "destructive" }[] }> = {
  "/": { title: "Dashboard", pills: [{ label: "0 running", variant: "secondary" }] },
  "/agents": { title: "Agents", pills: [{ label: "0 agents", variant: "secondary" }] },
  "/crews": { title: "Crews" },
  "/credentials": { title: "Credentials" },
  "/skills": { title: "Skills" },
  "/audit": { title: "Audit Log" },
  "/settings": { title: "Settings" },
}

function getInitials(name: string): string {
  if (!name.trim()) return "?"
  return name
    .split(" ")
    .map((n) => n[0])
    .join("")
    .slice(0, 2)
    .toUpperCase()
}

export function AppToolbar() {
  const pathname = usePathname()
  const config = pageConfig[pathname] ?? { title: "Crewship" }
  const { workspaceId } = useWorkspace()
  const { status: daemonStatus } = useCrewshipdStatus(workspaceId)
  const { data: session } = useSession()

  const userName = session?.user?.name ?? "User"
  const userEmail = session?.user?.email ?? ""
  const userInitials = getInitials(userName)

  return (
    <header className="flex h-12 shrink-0 items-center justify-between bg-white dark:bg-background px-3 sm:px-4">
      {/* Left: Org switcher + breadcrumb + pills */}
      <div className="flex items-center gap-1.5 min-w-0 overflow-hidden">
        {/* Org switcher */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button className="flex items-center gap-1.5 rounded-md px-1.5 py-1 hover:bg-accent transition-colors shrink-0">
              <div className="flex h-5 w-5 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground">U</div>
              <span className="text-sm font-medium hidden sm:inline">Unify Technology</span>
              <ChevronDown className="h-3 w-3 text-muted-foreground" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-72">
            <DropdownMenuLabel className="text-[10px] uppercase tracking-wider text-muted-foreground font-medium">Workspaces</DropdownMenuLabel>
            <DropdownMenuItem className="flex items-center gap-3 py-2 bg-primary/5">
              <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary text-[10px] font-bold text-primary-foreground shrink-0">U</div>
              <div className="min-w-0">
                <div className="text-xs font-medium">Unify Technology</div>
                <div className="text-[10px] text-muted-foreground">3 members</div>
              </div>
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="text-xs">Create workspace</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        <span className="text-muted-foreground/40 text-sm shrink-0">/</span>

        {/* Page title breadcrumb */}
        {config.breadcrumb ? (
          <>
            <span className="text-sm text-muted-foreground truncate">{config.title}</span>
            <span className="text-muted-foreground/40 text-sm shrink-0">/</span>
            <span className="text-sm font-semibold truncate">{config.breadcrumb}</span>
          </>
        ) : (
          <span className="text-sm font-semibold truncate">{config.title}</span>
        )}

        {/* Status pills */}
        {config.pills && config.pills.length > 0 && (
          <div className="hidden md:flex items-center gap-1.5 ml-1">
            {config.pills.map((pill) => (
              <Badge key={pill.label} variant={pill.variant} className="text-[10px] px-2 py-0.5">
                {pill.label}
              </Badge>
            ))}
          </div>
        )}
      </div>

      {/* Right: crewshipd + search + help + notifications + user */}
      <div className="flex items-center gap-1 sm:gap-1.5 shrink-0">
        {/* crewshipd status */}
        {daemonStatus === "connected" ? (
          <div className="hidden lg:flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-emerald-50 dark:bg-emerald-950/30 border border-emerald-200 dark:border-emerald-800 mr-1">
            <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
            <span className="text-[10px] font-medium text-emerald-700 dark:text-emerald-400">crewshipd</span>
          </div>
        ) : daemonStatus === "checking" ? (
          <div className="hidden lg:flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-amber-50 dark:bg-amber-950/30 border border-amber-200 dark:border-amber-800 mr-1">
            <span className="h-1.5 w-1.5 rounded-full bg-amber-500 animate-pulse" />
            <span className="text-[10px] font-medium text-amber-700 dark:text-amber-400">crewshipd</span>
          </div>
        ) : (
          <div className="hidden lg:flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-800 mr-1">
            <span className="h-1.5 w-1.5 rounded-full bg-red-500" />
            <span className="text-[10px] font-medium text-red-700 dark:text-red-400">crewshipd</span>
          </div>
        )}

        {/* Search */}
        <Button variant="outline" size="sm" className="h-8 gap-2 rounded-full border-border bg-transparent text-muted-foreground hover:text-foreground px-3" aria-label="Search">
          <Search className="h-3.5 w-3.5" />
          <span className="text-xs hidden sm:inline">Search...</span>
          <kbd className="pointer-events-none hidden h-5 select-none items-center gap-0.5 rounded border bg-muted px-1.5 font-mono text-[10px] font-medium sm:flex">
            <span className="text-xs">&#8984;</span>K
          </kbd>
        </Button>

        {/* Help */}
        <Button variant="ghost" size="icon" className="h-8 w-8 hidden sm:inline-flex" aria-label="Help">
          <BookOpen className="h-4 w-4" />
        </Button>

        {/* Notifications */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" className="h-8 w-8 relative" aria-label="Notifications">
              <Bell className="h-4 w-4" />
              <span className="absolute -top-0.5 -right-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-destructive text-[9px] font-bold text-destructive-foreground ring-2 ring-background" aria-hidden="true">
                3
              </span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-80">
            <DropdownMenuLabel className="flex items-center justify-between">
              <span>Notifications</span>
              <button className="text-[11px] text-primary font-medium">Mark all read</button>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="flex-col items-start gap-1 py-3">
              <div className="text-xs font-medium">No new notifications</div>
              <div className="text-[11px] text-muted-foreground">You&apos;re all caught up.</div>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        {/* User */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button className="flex items-center gap-2 rounded-md px-1.5 py-1 hover:bg-accent transition-colors" aria-label="User menu">
              <div className="flex h-7 w-7 items-center justify-center rounded-full bg-primary text-[10px] font-semibold text-primary-foreground">{userInitials}</div>
              <span className="text-xs font-medium hidden sm:inline">{userName.split(" ")[0]}</span>
              <ChevronDown className="h-3 w-3 text-muted-foreground hidden sm:block" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-64">
            <div className="px-2 py-3">
              <div className="flex items-center gap-3">
                <div className="flex h-10 w-10 items-center justify-center rounded-full bg-primary text-sm font-semibold text-primary-foreground">{userInitials}</div>
                <div>
                  <div className="text-sm font-medium">{userName}</div>
                  <div className="text-xs text-muted-foreground">{userEmail}</div>
                </div>
              </div>
              <div className="flex items-center gap-1.5 mt-2">
                <Badge variant="outline" className="text-[10px] px-1.5 py-0.5">Owner</Badge>
                <span className="text-[10px] text-muted-foreground">Unify Technology</span>
              </div>
            </div>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="gap-3 text-xs">
              <User className="h-4 w-4 text-muted-foreground" />
              Profile & Settings
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
              <Github className="h-4 w-4 text-muted-foreground" />
              GitHub
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="gap-3 text-xs text-destructive" onClick={() => signOut({ callbackUrl: "/login" })}>
              <LogOut className="h-4 w-4" />
              Log out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  )
}
