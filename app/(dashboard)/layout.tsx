"use client"

import { useEffect, useRef } from "react"
import { usePathname } from "next/navigation"
import { useIsMobile } from "@/hooks/use-mobile"
import { useRouteScrollRestoration } from "@/hooks/use-route-scroll-restoration"
import { MobileTabBar } from "@/components/layout/mobile-tab-bar"
import { Spinner } from "@/components/ui/spinner"
import { useSession } from "@/hooks/use-auth"

import { AppSidebar } from "@/components/layout/app-sidebar"
import { AppToolbar } from "@/components/layout/app-toolbar"
import { RuntimeBanner } from "@/components/layout/runtime-banner"
import { UpdateBanner } from "@/components/layout/update-banner"
import { SidebarProvider, SidebarInset } from "@/components/ui/sidebar"
import { RealtimeProvider } from "@/hooks/use-realtime"
import { JournalLookupProvider } from "@/hooks/use-journal-lookup"
import { ActiveRoutineRunsProvider } from "@/hooks/use-active-routine-runs"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAccessMode, useAccessModeWatcher, useTrustedWorkspaceId } from "@/hooks/use-access-mode"
import { Button } from "@/components/ui/button"
import { RealtimeToasts } from "@/components/layout/realtime-toasts"
import { RealtimeStatusBanner } from "@/components/layout/realtime-status-banner"
import { NotificationSoundEvents } from "@/components/layout/notification-sound-events"

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const { status } = useSession()
  const { error: workspacesError, refresh: refreshWorkspaces } = useWorkspace()
  // Session-level, not per workspace: one restricted membership puts the
  // whole account behind the restricted allowlist (see use-access-mode.ts).
  const accessMode = useAccessMode()
  const trusted = accessMode === "trusted"
  // Null until the session is known to be trusted, so the workspace-scoped
  // shell providers below send nothing a restricted session would be refused.
  const trustedWorkspaceId = useTrustedWorkspaceId()
  useAccessModeWatcher()
  const isMobile = useIsMobile()
  const pathname = usePathname()
  const scrollRef = useRef<HTMLDivElement>(null)

  /**
   * The page content scrolls inside this div, not the document, so the browser
   * has nothing to manage on a route change — a new screen opened already
   * scrolled halfway down the last one, and back opened a list at the top
   * instead of where the person left it. Nothing about that is visible on a
   * desktop, where a route change usually fits the viewport anyway.
   */
  useRouteScrollRestoration(scrollRef, pathname)

  useEffect(() => {
    if (status === "unauthenticated") {
      // Immediate redirect — no spinner, no delay
      window.location.replace("/login")
    }
  }, [status])

  if (status === "loading" || status === "unauthenticated") {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <Spinner className="h-6 w-6 text-muted-foreground-soft" />
      </div>
    )
  }

  // Until the access mode is known, nothing below may mount: the shell and
  // every page start requests that a restricted session is refused. The
  // workspace list (allowlisted for everyone) is the only request in flight.
  if (accessMode === "loading") {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-3 bg-background">
        {workspacesError ? (
          <>
            <p role="alert" className="text-sm text-muted-foreground">Could not load your workspaces.</p>
            <Button variant="outline" size="sm" onClick={() => { void refreshWorkspaces() }}>Retry</Button>
          </>
        ) : (
          <Spinner className="h-6 w-6 text-muted-foreground-soft" />
        )}
      </div>
    )
  }

  return (
    <RealtimeProvider>
      {/* Workspace-scoped journal lookup (crew/agent/mission id → name,
          slug, color). One fetch shared by every journal surface — the
          journal page, crew-journal, and the crew/agent activity feeds —
          which resolve ids to display names client-side. Must sit inside
          RealtimeProvider (it invalidates on crew/agent realtime events). */}
      <JournalLookupProvider workspaceId={trustedWorkspaceId}>
      {/* One workspace-scoped "active routine runs" subscription shared
          by the toolbar live chip and the /routines live surfaces —
          must sit inside RealtimeProvider (it consumes WS events). */}
      <ActiveRoutineRunsProvider>
        <SidebarProvider>
          <AppSidebar />
          <SidebarInset>
            <AppToolbar />
            <RealtimeStatusBanner />
            {/* /system/runtime and /system/version are not on the
                restricted allowlist. */}
            {trusted && <RuntimeBanner />}
            {trusted && <UpdateBanner />}
            <div ref={scrollRef} className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden bg-background rounded-t-2xl">
              {children}
            </div>
            {/* A flex sibling, not a fixed bar: it takes real space, so the
                scroll container above it needs no compensating padding and
                nothing can end up hidden underneath it. */}
            {isMobile && <MobileTabBar />}
          </SidebarInset>
        </SidebarProvider>
        <RealtimeToasts />
        {trusted && <NotificationSoundEvents />}
      </ActiveRoutineRunsProvider>
      </JournalLookupProvider>
    </RealtimeProvider>
  )
}
