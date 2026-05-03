"use client"

import { useEffect } from "react"
import { useSession } from "@/hooks/use-auth"
import { Loader2 } from "lucide-react"
import { AppSidebar } from "@/components/layout/app-sidebar"
import { AppToolbar } from "@/components/layout/app-toolbar"
import { RuntimeBanner } from "@/components/layout/runtime-banner"
import { SidebarProvider, SidebarInset } from "@/components/ui/sidebar"
import { RealtimeProvider } from "@/hooks/use-realtime"
import { RealtimeToasts } from "@/components/layout/realtime-toasts"
import { RealtimeStatusBanner } from "@/components/layout/realtime-status-banner"

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const { status } = useSession()

  useEffect(() => {
    if (status === "unauthenticated") {
      // Immediate redirect — no spinner, no delay
      window.location.replace("/login")
    }
  }, [status])

  if (status === "loading" || status === "unauthenticated") {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground/40" />
      </div>
    )
  }

  return (
    <RealtimeProvider>
      <SidebarProvider>
        <AppSidebar />
        <SidebarInset>
          <AppToolbar />
          <RealtimeStatusBanner />
          <RuntimeBanner />
          <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden bg-background rounded-t-2xl">
            {children}
          </div>
        </SidebarInset>
      </SidebarProvider>
      <RealtimeToasts />
    </RealtimeProvider>
  )
}
