import * as React from "react"

import { cn } from "@/lib/utils"
import { PageHeader } from "@/components/layout/page-header"
import { StatCard } from "@/components/layout/stat-card"

export interface PageShellStatItem {
  title: string
  value: string | number
  subtitle: string
  icon: React.ElementType
  iconClassName?: string
  animatedIcon?: React.ReactNode
}

interface PageShellProps extends Omit<React.ComponentProps<"div">, "title"> {
  title: string
  description: string
  /** Right-side header actions (buttons, dropdowns). */
  actions?: React.ReactNode
  /** Optional KPI band rendered directly below the header. */
  stats?: PageShellStatItem[]
  /** Optional toolbar rendered below the header (typically ToolbarStrip). */
  toolbar?: React.ReactNode
  /** Padding variant. `compact` drops to p-4 for data-dense pages. */
  density?: "default" | "compact"
  /** Remove the default container padding (for pages that own their own layout). */
  unpadded?: boolean
}

/**
 * PageShell — canonical wrapper for every non-orchestration page.
 * Bundles PageHeader + optional stats band + optional toolbar + content.
 *
 * Use this to guarantee every page in the app shares identical padding,
 * header typography, and section spacing. Pages that need a multi-panel
 * layout (drawer, sidebar rail) should skip PageShell and compose
 * manually — see the orchestration page as reference.
 *
 * @example
 * <PageShell
 *   title="Credentials"
 *   description="Shared secrets and CLI tokens"
 *   actions={<Button>New Credential</Button>}
 *   stats={[{ title: "Active", value: 12, subtitle: "healthy", icon: CheckCircle }]}
 * >
 *   <CredentialsTable />
 * </PageShell>
 */
export function PageShell({
  title,
  description,
  actions,
  stats,
  toolbar,
  density = "default",
  unpadded = false,
  className,
  children,
  ...props
}: PageShellProps) {
  return (
    <div
      className={cn(
        "flex flex-col",
        !unpadded && (density === "compact" ? "gap-4 p-4" : "gap-6 p-6"),
        className
      )}
      {...props}
    >
      <PageHeader title={title} description={description}>
        {actions}
      </PageHeader>

      {stats && stats.length > 0 && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {stats.map((stat) => (
            <StatCard
              key={stat.title}
              title={stat.title}
              value={stat.value}
              subtitle={stat.subtitle}
              icon={stat.icon}
              iconClassName={stat.iconClassName}
              animatedIcon={stat.animatedIcon}
            />
          ))}
        </div>
      )}

      {toolbar}

      {children}
    </div>
  )
}
