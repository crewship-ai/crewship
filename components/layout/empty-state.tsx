import { cn } from "@/lib/utils"
import { Card, CardContent } from "@/components/ui/card"

/**
 * - "card": wrapped in a bordered Card — a page-level empty state.
 * - "inline": flush with the surface it sits on.
 * - "compact": flush and smaller, for a list or tab inside a DetailCard
 *   (the register the former `components/ui/detail` EmptyState drew).
 */
export type EmptyStateSize = "card" | "inline" | "compact"

export interface EmptyStateProps {
  icon: React.ElementType
  title: string
  description?: string
  size?: EmptyStateSize
  /** Rendered under the copy, in its own spaced row (a button, a link). */
  action?: React.ReactNode
  className?: string
  /** Rendered directly under the copy, for callers that lay out their own row. */
  children?: React.ReactNode
}

/**
 * The one empty state (#3344). There used to be a second component of the
 * same name in `components/ui/detail`; its props (`action`, an optional
 * `description`) and its look (`size="compact"`) live here now, and
 * components/__tests__/one-empty-state.test.ts keeps it that way.
 */
export function EmptyState({
  icon: Icon,
  title,
  description,
  size = "card",
  action,
  className,
  children,
}: EmptyStateProps) {
  if (size === "compact") {
    return (
      <div className={cn("flex flex-col items-center justify-center px-6 py-12 text-center", className)}>
        <div className="icon-tile mb-3 flex h-12 w-12 items-center justify-center rounded-card">
          <Icon className="h-6 w-6" />
        </div>
        <div className="text-body font-medium text-foreground">{title}</div>
        {description && <p className="type-row mt-1.5 max-w-sm leading-relaxed text-muted-foreground">{description}</p>}
        {children}
        {action && <div className="mt-4">{action}</div>}
      </div>
    )
  }

  const body = (
    <div
      className={cn(
        "flex flex-col items-center justify-center px-4 text-center",
        size === "card" ? "py-10 sm:py-16" : "py-8",
      )}
    >
      <div className="icon-tile mb-4 flex h-14 w-14 items-center justify-center rounded-card">
        <Icon className="h-6 w-6" />
      </div>
      <h3 className="text-default font-semibold tracking-[-0.02em]">{title}</h3>
      {description && <p className="mt-1 max-w-sm text-body text-muted-foreground">{description}</p>}
      {children}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )

  if (size === "inline") {
    return <div className={className}>{body}</div>
  }

  return (
    <Card className={className}>
      <CardContent className="p-0">{body}</CardContent>
    </Card>
  )
}
