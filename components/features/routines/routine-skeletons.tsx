"use client"

// Shared loading skeletons for routine detail-panel sub-tabs. Plain
// "Loading…" text creates a visual cliff each tab-switch — skeletons
// keep the panel structure stable while data loads, matching how the
// rest of the app (mission timeline, journal feed) handles loading.

import { cn } from "@/lib/utils"

interface SkeletonRowsProps {
  rows?: number
  className?: string
}

export function RoutineListSkeleton({ rows = 4, className }: SkeletonRowsProps) {
  return (
    <ol className={cn("space-y-1.5", className)}>
      {Array.from({ length: rows }).map((_, i) => (
        <li
          key={i}
          className="rounded-md border border-white/[0.06] bg-card/40 px-3 py-2.5"
        >
          <div className="flex items-center gap-3">
            <SkeletonDot />
            <div className="flex-1 space-y-1.5">
              <SkeletonBar widthClass="w-2/3" />
              <SkeletonBar widthClass="w-1/3" muted />
            </div>
            <SkeletonBar widthClass="w-12" muted />
          </div>
        </li>
      ))}
    </ol>
  )
}

export function RoutineDetailSkeleton({ className }: { className?: string }) {
  return (
    <div className={cn("space-y-3", className)}>
      <SkeletonBar widthClass="w-1/2" />
      <SkeletonBar widthClass="w-full" muted />
      <SkeletonBar widthClass="w-3/4" muted />
      <div className="mt-4 grid grid-cols-2 gap-3">
        <SkeletonBlock />
        <SkeletonBlock />
        <SkeletonBlock />
        <SkeletonBlock />
      </div>
    </div>
  )
}

export function RoutineRunsSkeleton({ rows = 3 }: SkeletonRowsProps) {
  return (
    <ol className="space-y-1.5">
      {Array.from({ length: rows }).map((_, i) => (
        <li
          key={i}
          className="rounded-md border border-white/[0.06] bg-card/40 p-2.5"
        >
          <div className="flex items-center gap-2">
            <SkeletonDot />
            <SkeletonBar widthClass="w-32" />
            <div className="flex-1" />
            <SkeletonBar widthClass="w-16" muted />
          </div>
        </li>
      ))}
    </ol>
  )
}

function SkeletonBar({
  widthClass = "w-32",
  muted = false,
}: {
  widthClass?: string
  muted?: boolean
}) {
  return (
    <div
      className={cn(
        "h-2.5 animate-pulse rounded",
        widthClass,
        muted ? "bg-muted/40" : "bg-muted/60",
      )}
    />
  )
}

function SkeletonDot() {
  return <div className="h-3 w-3 shrink-0 animate-pulse rounded-full bg-muted/60" />
}

function SkeletonBlock() {
  return <div className="h-12 animate-pulse rounded-md bg-muted/40" />
}
