"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { Menu } from "lucide-react"

import { cn } from "@/lib/utils"
import { phoneTabs } from "@/lib/nav-sections"
import { useInboxUnreadCount } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAppStore } from "@/lib/store"

/**
 * The phone's primary navigation.
 *
 * Before this, every destination on a phone was three taps deep behind a
 * hamburger — including Inbox, which is the one surface with a live count and
 * the one people open the app to check. A tab bar is not decoration here; it
 * is the difference between glancing at the queue and excavating it.
 *
 * It renders in the layout's flex column rather than `fixed`, so it occupies
 * real space and the scroll container above it needs no bottom padding to
 * compensate. The one thing that does overlap it is `SaveFooter`, which pins
 * itself to the bottom edge below `sm`; that footer offsets itself by
 * `--mobile-tab-bar-h` (app/globals.css) so the two stack instead of fighting.
 */
export function MobileTabBar() {
  const pathname = usePathname()
  const { workspaceId } = useWorkspace()
  const setMobileNavOpen = useAppStore((s) => s.setMobileNavOpen)
  const tabs = phoneTabs()

  return (
    <nav
      aria-label="Primary"
      className={cn(
        "flex shrink-0 items-stretch border-t border-border bg-background",
        // The token already includes the home-indicator inset; padding by the
        // same inset then leaves a 3.5rem content box, because box-sizing is
        // border-box.
        "h-[var(--mobile-tab-bar-h)] pb-[env(safe-area-inset-bottom)]",
      )}
    >
      {tabs.map((tab) => {
        const isActive =
          pathname === tab.href || (tab.href !== "/" && pathname.startsWith(tab.href))
        return (
          <Link
            key={tab.href}
            href={tab.href}
            aria-current={isActive ? "page" : undefined}
            className={cn(
              "relative flex flex-1 flex-col items-center justify-center gap-0.5 text-micro transition-colors",
              isActive ? "text-primary" : "text-muted-foreground",
            )}
          >
            <tab.icon className="h-5 w-5" />
            <span className="truncate px-1">{tab.title}</span>
            {tab.href === "/inbox" && <TabUnreadDot workspaceId={workspaceId} />}
          </Link>
        )
      })}

      <button
        type="button"
        aria-label="More"
        onClick={() => setMobileNavOpen(true)}
        className="flex flex-1 flex-col items-center justify-center gap-0.5 text-micro text-muted-foreground transition-colors"
      >
        <Menu className="h-5 w-5" />
        <span>More</span>
      </button>
    </nav>
  )
}

/**
 * A dot rather than a number: the tab is ~78px wide and already carries an
 * icon and a label, and the exact count is one tap away in the sheet. What a
 * glance needs to answer is "is anything waiting", not "how many".
 */
function TabUnreadDot({ workspaceId }: { workspaceId: string | null }) {
  const unread = useInboxUnreadCount(workspaceId)
  if (!unread) return null
  return (
    <span
      aria-label={`${unread} unread`}
      className="absolute top-1.5 right-[calc(50%-1.25rem)] h-2 w-2 rounded-full bg-primary"
    />
  )
}
