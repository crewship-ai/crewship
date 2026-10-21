"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { ChevronRight, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { visibleNavItems } from "@/lib/nav-sections"
import { useAppStore } from "@/lib/store"
import { useAbilities } from "@/hooks/use-abilities"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { useRestrictedSurfaces, useTrustedWorkspaceId } from "@/hooks/use-access-mode"
import { useInboxUnreadCount } from "@/hooks/use-inbox"
import { useLiveRunCount } from "@/hooks/use-live-run-count"
import { ConceptIcon } from "@/components/ui/concept-icon"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { conceptOf } from "@/components/layout/rail"
import { MobileWorkspaceSwitcher } from "./workspace-switcher"
import { UserMenu } from "./user-menu"

/**
 * The phone's More (v67 `pop-more`, chrome-states G11): one grid of tiles for
 * every destination this person may open - the same rows the sidebar offers,
 * filtered through `visibleNavItems` - then a row for the user, which opens
 * the user menu, and a row for what is running, which opens Activity.
 *
 * The workspace switcher keeps the header it has today; v67 puts it in the
 * phone top bar instead (CHR-08 / ODCH-G03 is an open decision).
 */
export function MobileMoreSheet() {
  const pathname = usePathname()
  const open = useAppStore((s) => s.mobileNavOpen)
  const setOpen = useAppStore((s) => s.setMobileNavOpen)
  const { role } = useAbilities()
  const instanceAdmin = useIsInstanceAdmin()
  const surfaces = useRestrictedSurfaces()
  // /inbox/count and the run feeds are not on the restricted allowlist.
  const workspaceId = useTrustedWorkspaceId()
  const items = visibleNavItems({ role, instanceAdmin, surfaces })
  const close = () => setOpen(false)

  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetContent side="bottom" showCloseButton={false} className="more-sheet rounded-t-xl max-h-[85dvh] p-0">
        <div className="mx-auto mb-1 mt-3 h-1.5 w-12 rounded-full bg-border" />
        <SheetHeader className="border-b px-4 py-2">
          <div className="flex items-center justify-between">
            <SheetTitle className="sr-only">Navigation</SheetTitle>
            <MobileWorkspaceSwitcher onNavigate={close} />
            <button onClick={close} aria-label="Close navigation" className="flex h-8 w-8 items-center justify-center rounded-md hover:bg-accent">
              <X className="h-4 w-4" />
            </button>
          </div>
        </SheetHeader>
        <div className="flex-1 overflow-y-auto">
          <ul aria-label="Navigation" className="more-grid">
            {items.map((item) => {
              const active = pathname === item.href || (item.href !== "/" && pathname.startsWith(item.href))
              return (
                <li key={item.href}>
                  <Link
                    href={item.href}
                    onClick={close}
                    aria-current={active ? "page" : undefined}
                    className={cn("more-it", active && "more-it-on")}
                  >
                    <ConceptIcon concept={conceptOf(item.href)} icon={item.icon} variant="chip" size="lg" />
                    <span className="max-w-full truncate">{item.title}</span>
                    {item.href === "/inbox" && <UnreadDot workspaceId={workspaceId} />}
                  </Link>
                </li>
              )
            })}
          </ul>
          <div className="more-rows">
            <UserMenu placement="row" />
            {workspaceId && <RunningRow workspaceId={workspaceId} onNavigate={close} />}
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}

/** The Inbox tile's count, as a leaf so the query runs only while the sheet is open. */
function UnreadDot({ workspaceId }: { workspaceId: string | null }) {
  const unread = useInboxUnreadCount(workspaceId)
  if (!unread) return null
  return (
    <span className="more-count" aria-label={`${unread} unread`}>
      {unread > 99 ? "99+" : unread}
    </span>
  )
}

/** "3 agents running" -> Activity. Absent while nothing runs. */
function RunningRow({ workspaceId, onNavigate }: { workspaceId: string; onNavigate: () => void }) {
  const live = useLiveRunCount(workspaceId)
  if (live < 1) return null
  return (
    <Link href="/activity" onClick={onNavigate} className="more-row">
      <span className="more-live" aria-hidden />
      <span className="min-w-0 flex-1 truncate">{live} {live === 1 ? "agent" : "agents"} running</span>
      <ChevronRight className="size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
    </Link>
  )
}
