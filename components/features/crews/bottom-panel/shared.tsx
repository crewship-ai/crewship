"use client"

// Tiny presentational + formatting helpers shared by every tab.

export function EmptyState({ children }: { children: React.ReactNode }) {
  return (
    <div className="h-full flex items-center justify-center text-xs text-muted-foreground p-4 text-center">
      {children}
    </div>
  )
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
  const sec = Math.floor((Date.now() - t) / 1000)
  if (sec < 45) return "just now"
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`
  if (sec < 86400 * 30) return `${Math.floor(sec / 86400)}d ago`
  return d.toLocaleDateString()
}

/** Short status pill colour by run/mission status string. */
export function statusColor(status: string): string {
  const s = status.toLowerCase()
  if (s.includes("success") || s.includes("complete") || s.includes("done") || s.includes("ok")) return "text-emerald-300"
  if (s.includes("fail") || s.includes("error")) return "text-red-300"
  if (s.includes("run") || s.includes("active") || s.includes("progress")) return "text-blue-300"
  if (s.includes("wait") || s.includes("escalat") || s.includes("pending") || s.includes("review")) return "text-amber-300"
  return "text-muted-foreground"
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`
}
