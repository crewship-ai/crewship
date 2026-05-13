"use client"

import { cn } from "@/lib/utils"

// Shared loading skeletons. Promoted from the routines feature where
// they originated — every page now uses the same primitives.

interface SkeletonRowsProps {
  rows?: number
  className?: string
}

export function ListRowSkeleton({ rows = 4, className }: SkeletonRowsProps) {
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

export function DetailPanelSkeleton({ className }: { className?: string }) {
  return (
    <div className={cn("space-y-3 p-4", className)}>
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

export function TableRowSkeleton({ rows = 6, columns = 4 }: { rows?: number; columns?: number }) {
  return (
    <div className="space-y-1.5">
      {Array.from({ length: rows }).map((_, i) => (
        <div
          key={i}
          className="flex items-center gap-4 rounded-md border border-white/[0.06] bg-card/40 px-3 py-2.5"
        >
          {Array.from({ length: columns }).map((_, j) => (
            <SkeletonBar
              key={j}
              widthClass={j === 0 ? "w-1/4" : j === columns - 1 ? "w-16" : "w-1/6"}
              muted={j > 0}
            />
          ))}
        </div>
      ))}
    </div>
  )
}

export function CardGridSkeleton({
  cards = 6,
  columns = 3,
}: {
  cards?: number
  columns?: 2 | 3 | 4
}) {
  const colClass =
    columns === 2 ? "grid-cols-2" : columns === 4 ? "grid-cols-4" : "grid-cols-3"
  return (
    <div className={cn("grid gap-3", colClass)}>
      {Array.from({ length: cards }).map((_, i) => (
        <div
          key={i}
          className="space-y-2 rounded-md border border-white/[0.06] bg-card/40 p-3"
        >
          <SkeletonBar widthClass="w-2/3" />
          <SkeletonBar widthClass="w-full" muted />
          <SkeletonBar widthClass="w-1/2" muted />
        </div>
      ))}
    </div>
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
