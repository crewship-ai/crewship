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
  /** Optional animated icon component (lucide-animated). Renders instead of static icon. */
  animatedIcon?: React.ReactNode
}

export function StatCard({ title, value, subtitle, icon: Icon, iconClassName, animatedIcon }: StatCardProps) {
  return (
    <FlashHighlight trigger={value}>
      <Card>
        <CardContent className="p-4 sm:p-5">
          <div className="flex items-center justify-between">
            <div className="text-xs text-muted-foreground uppercase tracking-wide font-medium">{title}</div>
            <div className={`flex h-8 w-8 items-center justify-center rounded-lg ${iconClassName ?? "bg-muted"}`}>
              {animatedIcon ?? <Icon className="h-4 w-4" />}
            </div>
          </div>
          <div className="mt-1 text-2xl sm:text-3xl font-bold">
            {typeof value === "number" ? <AnimatedNumber value={value} /> : value}
          </div>
          <div className="mt-1 text-xs text-muted-foreground">{subtitle}</div>
        </CardContent>
      </Card>
    </FlashHighlight>
  )
}
