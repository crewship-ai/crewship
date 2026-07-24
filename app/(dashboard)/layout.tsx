"use client"

import { useEffect } from "react"
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
import { RealtimeToasts } from "@/components/layout/realtime-toasts"
import { RealtimeStatusBanner } from "@/components/layout/realtime-status-banner"

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const { status } = useSession()
  const { workspaceId } = useWorkspace()

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

  return (
    <RealtimeProvider>
      {/* Workspace-scoped journal lookup (crew/agent/mission id → name,
          slug, color). One fetch shared by every journal surface — the
          journal page, crew-journal, and the crew/agent activity feeds —
          which resolve ids to display names client-side. Must sit inside
          RealtimeProvider (it invalidates on crew/agent realtime events). */}
      <JournalLookupProvider workspaceId={workspaceId}>
      {/* One workspace-scoped "active routine runs" subscription shared
          by the toolbar live chip and the /routines live surfaces —
          must sit inside RealtimeProvider (it consumes WS events). */}
      <ActiveRoutineRunsProvider>
        <SidebarProvider>
          <AppSidebar />
          <SidebarInset>
            <AppToolbar />
            <RealtimeStatusBanner />
            <RuntimeBanner />
            <UpdateBanner />
            <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden bg-background rounded-t-2xl">
              {children}
            </div>
          </SidebarInset>
        </SidebarProvider>
        <RealtimeToasts />
      </ActiveRoutineRunsProvider>
      </JournalLookupProvider>
    </RealtimeProvider>
  )
}
