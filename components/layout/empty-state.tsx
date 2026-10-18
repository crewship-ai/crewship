import { cn } from "@/lib/utils"
import { Card, CardContent } from "@/components/ui/card"

/** "compact" is flush with its surface; "inline" is its former name. */
type EmptyStateSize = "card" | "compact" | "inline"

interface EmptyStateProps {
  icon: React.ElementType
  title: string
  description: string
  /** "card" wraps in a bordered Card; "compact" renders flush with the
   *  surface (a tab, a card body). "inline" is the same as "compact". */
  size?: EmptyStateSize
  /** The one button (or link) under the description. */
  action?: React.ReactNode
  className?: string
  children?: React.ReactNode
}

export function EmptyState({
  icon: Icon,
  title,
  description,
  size = "card",
  action,
  className,
  children,
}: EmptyStateProps) {
  const flush = size !== "card"
  const body = (
    <div
      data-slot="empty-state"
      data-size={flush ? "compact" : "card"}
      className={cn(
        "flex flex-col items-center justify-center px-4 text-center",
        flush ? "py-8" : "py-10 sm:py-16",
      )}
    >
      <div className="icon-tile mb-4 flex h-14 w-14 items-center justify-center rounded-2xl">
        <Icon className="h-6 w-6" />
      </div>
      <h3 className="text-default font-semibold tracking-[-0.02em]">{title}</h3>
      <p className="mt-1 max-w-sm text-body text-muted-foreground">{description}</p>
      {action && <div data-slot="empty-state-action" className="mt-4">{action}</div>}
      {children}
    </div>
  )

  if (flush) {
    return <div className={className}>{body}</div>
  }

  return (
    <Card className={className}>
      <CardContent className="p-0">{body}</CardContent>
    </Card>
  )
}
