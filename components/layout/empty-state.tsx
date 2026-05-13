import { cn } from "@/lib/utils"
import { Card, CardContent } from "@/components/ui/card"

type EmptyStateSize = "card" | "inline"

interface EmptyStateProps {
  icon: React.ElementType
  title: string
  description: string
  /** "card" wraps in a bordered Card; "inline" renders flush with the surface. */
  size?: EmptyStateSize
  className?: string
  children?: React.ReactNode
}

export function EmptyState({
  icon: Icon,
  title,
  description,
  size = "card",
  className,
  children,
}: EmptyStateProps) {
  const body = (
    <div
      className={cn(
        "flex flex-col items-center justify-center px-4 text-center",
        size === "card" ? "py-10 sm:py-16" : "py-8",
      )}
    >
      <div className="mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-muted">
        <Icon className="h-6 w-6 text-muted-foreground" />
      </div>
      <h3 className="text-body font-semibold">{title}</h3>
      <p className="mt-1 max-w-sm text-body text-muted-foreground">{description}</p>
      {children}
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
