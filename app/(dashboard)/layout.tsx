"use client"

import { useEffect } from "react"
import { useSession } from "@/hooks/use-auth"
import { useRouter } from "next/navigation"
import { Loader2 } from "lucide-react"
import { AppSidebar } from "@/components/layout/app-sidebar"
import { AppToolbar } from "@/components/layout/app-toolbar"
import { RuntimeBanner } from "@/components/layout/runtime-banner"
import { SidebarProvider, SidebarInset } from "@/components/ui/sidebar"
import { RealtimeProvider } from "@/hooks/use-realtime"
import { RealtimeToasts } from "@/components/layout/realtime-toasts"

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const { status } = useSession()
  const router = useRouter()

  useEffect(() => {
    if (status === "unauthenticated") {
      router.push("/login")
    }
  }, [status, router])

  if (status === "loading" || status === "unauthenticated") {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    )
  }

  return (
    <RealtimeProvider>
      <SidebarProvider defaultOpen={false}>
        <AppSidebar />
        <SidebarInset>
          <AppToolbar />
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
