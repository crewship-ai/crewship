import { Card, CardContent } from "@/components/ui/card"

interface StatCardProps {
  title: string
  value: string | number
  subtitle: string
  icon: React.ElementType
  iconClassName?: string
}

export function StatCard({ title, value, subtitle, icon: Icon, iconClassName }: StatCardProps) {
  return (
    <Card>
      <CardContent className="p-5">
        <div className="flex items-center justify-between">
          <div className="text-xs text-muted-foreground uppercase tracking-wide font-medium">{title}</div>
          <div className={`flex h-8 w-8 items-center justify-center rounded-lg ${iconClassName ?? "bg-muted"}`}>
            <Icon className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-1 text-3xl font-bold">{value}</div>
        <div className="mt-1 text-xs text-muted-foreground">{subtitle}</div>
      </CardContent>
    </Card>
  )
}
