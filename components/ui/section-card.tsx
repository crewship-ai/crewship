import * as React from "react"

import { cn } from "@/lib/utils"
import { Card, CardContent, CardHeader, CardTitle, CardDescription, CardAction } from "@/components/ui/card"

interface SectionCardProps extends Omit<React.ComponentProps<"div">, "title"> {
  /**
   * Surface variant:
   * - `card` (default) — standard shadcn card (`bg-card`, elevated)
   * - `subtle` — nested section surface, uses `bg-surface-subtle` token.
   *   Replaces ad-hoc `bg-[#18171D]` sprinkled through orchestration.
   */
  surface?: "card" | "subtle"
  title?: React.ReactNode
  description?: React.ReactNode
  actions?: React.ReactNode
  /** Render children without CardContent wrapper (for custom padding). */
  bare?: boolean
}

/**
 * SectionCard — canonical container for grouped content inside a page.
 * Thin wrapper over shadcn Card with an optional "subtle" surface variant
 * for nested sections inside a parent card.
 */
export function SectionCard({
  surface = "card",
  title,
  description,
  actions,
  bare = false,
  className,
  children,
  ...props
}: SectionCardProps) {
  return (
    <Card
      className={cn(
        surface === "subtle" && "bg-surface-subtle border-border/60 shadow-none",
        className
      )}
      {...props}
    >
      {(title || description || actions) && (
        <CardHeader>
          {title && <CardTitle className="text-heading">{title}</CardTitle>}
          {description && <CardDescription className="text-body">{description}</CardDescription>}
          {actions && <CardAction>{actions}</CardAction>}
        </CardHeader>
      )}
      {bare ? children : <CardContent>{children}</CardContent>}
    </Card>
  )
}
