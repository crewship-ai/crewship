"use client"

import { Card, CardContent } from "@/components/ui/card"
import { AnimatedNumber } from "@/components/ui/animated-number"
import { FlashHighlight } from "@/components/ui/flash-highlight"

interface StatCardProps {
  title: string
  value: string | number
  subtitle: string
  icon: React.ElementType
  iconClassName?: string
  className?: string
  /** Optional animated icon component (lucide-animated). Renders instead of static icon. */
  animatedIcon?: React.ReactNode
}

export function StatCard({ title, value, subtitle, icon: Icon, iconClassName, className, animatedIcon }: StatCardProps) {
  return (
    <FlashHighlight trigger={value}>
      <Card className={className}>
        <CardContent className="p-4 sm:p-5">
          <div className="flex items-center justify-between">
            <div className="eyebrow text-muted-foreground">{title}</div>
            <div className={`flex h-8 w-8 items-center justify-center rounded-lg ${iconClassName ?? "icon-tile"}`}>
              {animatedIcon ?? <Icon className="h-4 w-4" />}
            </div>
          </div>
          <div className="mt-1.5 text-display font-semibold tracking-[-0.035em] tabular-nums">
            {typeof value === "number" ? <AnimatedNumber value={value} /> : value}
          </div>
          <div className="mt-1 text-label text-muted-foreground">{subtitle}</div>
        </CardContent>
      </Card>
    </FlashHighlight>
  )
}
