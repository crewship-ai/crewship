"use client"

import type { ReactNode } from "react"
import { Shield } from "lucide-react"

import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { SubBar } from "@/components/layout/sub-bar"
import { PageNoAccess } from "@/components/layout/page-state"
import { Skeleton } from "@/components/ui/skeleton"

/**
 * The Admin sub-pages (People & workspaces, Security, Backups) belong to
 * instance administrators, like the console itself: a workspace OWNER or
 * ADMIN is told so, and the page behind the gate is not mounted, so it reads
 * nothing. null (session still loading) waits instead of judging.
 */
export function InstanceAdminGate({ area, children }: { area: string; children: ReactNode }) {
  const instanceAdmin = useIsInstanceAdmin()
  if (instanceAdmin === null) {
    return (
      <div className="p-4 md:p-6" role="status" aria-busy="true" aria-label={`Loading ${area}`}>
        <Skeleton className="h-12 w-64 rounded-card" />
      </div>
    )
  }
  if (!instanceAdmin) {
    return (
      <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col">
        <SubBar icon={Shield} title="Admin" ariaLabel="Admin Console" />
        <PageNoAccess
          area={area}
          needed="an instance administrator"
          primary={{ label: "Back to Home", href: "/" }}
          className="flex-1"
        />
      </div>
    )
  }
  return <>{children}</>
}
