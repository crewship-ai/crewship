"use client"
import * as React from "react"
import { ArrowDownToLine, Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"

export const card = "rounded-xl border border-white/[0.08] bg-card"
export const time = (value?: string) =>
  value ? new Date(value).toLocaleString() : "—"
export function IncomingError({
  message,
  onRetry,
}: {
  message: string
  onRetry?: () => void
}) {
  return (
    <div
      role="alert"
      className="rounded-lg border border-destructive/30 bg-destructive/[0.06] p-3 text-xs text-destructive"
    >
      {message}
      {onRetry && (
        <Button variant="ghost" size="sm" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  )
}
export function Loading() {
  return (
    <div
      aria-label="Loading incoming endpoints"
      className="space-y-4 p-4 md:p-6"
    >
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-28 rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-64 rounded-xl" />
    </div>
  )
}
export function Panel({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <section className={card}>
      <h3 className="border-b border-white/[0.06] px-4 py-3 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        {title}
      </h3>
      {children}
    </section>
  )
}
export function Empty({ onAdd }: { onAdd: () => void }) {
  return (
    <div className="flex flex-col items-center gap-3 px-6 py-12 text-center">
      <span className="rounded-lg bg-white/[0.04] p-3">
        <ArrowDownToLine className="size-5 text-muted-foreground" />
      </span>
      <h3 className="text-sm font-medium">No incoming endpoints yet</h3>
      <p className="max-w-md text-xs leading-relaxed text-muted-foreground">
        An incoming webhook is where another service reaches Crewship — GitHub,
        your CI, a form, a script.
      </p>
      <Button variant="soft" size="sm" onClick={onAdd}>
        <Plus className="size-3.5" />
        Add an incoming webhook
      </Button>
    </div>
  )
}
