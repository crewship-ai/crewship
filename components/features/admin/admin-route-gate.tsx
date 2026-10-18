"use client"

import type { ReactNode } from "react"
import { Shield } from "lucide-react"

import { Skeleton } from "@/components/ui/skeleton"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { SubBar } from "@/components/layout/sub-bar"
import { PageNoAccess } from "@/components/layout/page-state"

/**
 * The instance-admin gate for the Admin sub-routes (/admin/people, /security,
 * /backups), the same one /admin applies: a person who is not an instance
 * administrator is told so, on the page they opened, and nothing of the
 * console is mounted for them (so none of its APIs is called from here). The
 * server enforces the same rule on every admin API; this is the honest page.
 */
export function AdminRouteGate({ title, children }: { title: string; children: ReactNode }) {
  const instanceAdmin = useIsInstanceAdmin()

  if (instanceAdmin === null) {
    return (
      <div className="mx-auto w-full max-w-screen-2xl space-y-4 p-4 md:p-6" role="status" aria-busy="true" aria-label={`Loading ${title}`}>
        <Skeleton className="h-12 w-48 rounded-card" />
        <Skeleton className="h-40 rounded-card" />
      </div>
    )
  }

  if (!instanceAdmin) {
    return (
      <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col">
        <SubBar icon={Shield} title={title} ariaLabel={title} />
        <PageNoAccess
          area="the Admin console"
          needed="an instance administrator"
          primary={{ label: "Back to Home", href: "/" }}
          className="flex-1"
        />
      </div>
    )
  }

  return <>{children}</>
}
