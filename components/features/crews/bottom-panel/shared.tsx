"use client"

import * as React from "react"

// Tiny presentational + formatting helpers shared by every tab.

export function EmptyState({ children, onRetry }: { children: React.ReactNode; onRetry?: () => void }) {
  return (
    <div className="h-full flex flex-col items-center justify-center gap-2 text-xs text-muted-foreground p-4 text-center">
      <span>{children}</span>
      {onRetry && (
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border border-border px-2.5 py-1 text-xs text-foreground/85 hover:bg-foreground/[0.04]"
        >
          Retry
        </button>
      )}
    </div>
  )
}

/** A counter a tab bumps to run its fetch effect again — the Retry behind
 *  every "Failed to load" in the dock (README §6: every error offers one). */
export function useRetry(): [number, () => void] {
  return React.useReducer((n: number) => n + 1, 0)
}

export function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })
}

/** Compact relative time ("just now", "5m ago", "3h ago", "2d ago").
 *  Falls back to the raw string for unparseable input. */
export function formatRelative(iso: string): string {
  const d = new Date(iso)
  const t = d.getTime()
  if (Number.isNaN(t)) return iso
  const diff = Math.floor((Date.now() - t) / 1000)
  // Future timestamps (e.g. a schedule's next_run_at) read forward.
  if (diff < 0) {
    const sec = -diff
    if (sec < 45) return "in moments"
    if (sec < 3600) return `in ${Math.floor(sec / 60)}m`
    if (sec < 86400) return `in ${Math.floor(sec / 3600)}h`
    if (sec < 86400 * 30) return `in ${Math.floor(sec / 86400)}d`
    return d.toLocaleDateString()
  }
  if (diff < 45) return "just now"
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)}d ago`
  return d.toLocaleDateString()
}

/** Short status pill colour by run/mission status string. Null-safe — a
 *  row missing `status` must not crash the whole tab. */
export function statusColor(status?: string | null): string {
  const s = (status ?? "").toLowerCase()
  if (s.includes("success") || s.includes("complete") || s.includes("done") || s.includes("ok")) return "text-success"
  if (s.includes("fail") || s.includes("error")) return "text-destructive"
  if (s.includes("run") || s.includes("active") || s.includes("progress")) return "text-primary"
  if (s.includes("wait") || s.includes("escalat") || s.includes("pending") || s.includes("review")) return "text-warn"
  return "text-muted-foreground"
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`
}
