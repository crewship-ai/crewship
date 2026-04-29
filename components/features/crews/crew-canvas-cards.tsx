"use client"

import Link from "next/link"
import { cn } from "@/lib/utils"

// Small overview-stat cards extracted from crew-canvas.tsx for readability.
// HealthCard renders a labeled metric tile (optionally tinted by tone);
// QuickAction renders a square icon-tile button.

function HealthCard({ label, value, hint, tone, href }: {
  label: string
  value: string
  hint: string
  tone: "active" | "neutral" | "danger"
  href?: string
}) {
  const inner = (
    <div
      className={cn(
        "rounded-xl border bg-card p-4 transition-colors",
        tone === "danger" ? "border-red-500/30 ring-1 ring-red-500/20" :
        tone === "active" ? "border-white/10" : "border-white/8",
        href && "hover:border-white/20",
      )}
    >
      <div className="flex items-center justify-between mb-2">
        <span className="text-xs text-muted-foreground uppercase tracking-wide">{label}</span>
        {tone === "danger" && <span className="text-[10px] text-red-300">action needed</span>}
      </div>
      <div className={cn(
        "text-2xl font-semibold mb-1 tabular-nums",
        tone === "danger" ? "text-red-200" : "text-foreground",
      )}>
        {value}
      </div>
      <div className="text-[11px] text-muted-foreground">{hint}</div>
    </div>
  )
  return href ? <Link href={href}>{inner}</Link> : inner
}


function QuickAction({ icon, label, onClick, disabled }: {
  icon: React.ReactNode
  label: string
  onClick: () => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="rounded-lg border border-white/8 bg-card px-3 py-2.5 flex items-center gap-2.5 text-left hover:border-white/15 hover:bg-white/[0.02] disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
    >
      <span className="text-foreground/70">{icon}</span>
      <span className="text-xs text-foreground/85">{label}</span>
    </button>
  )
}

/**
 * Surfaces three states the user otherwise can't see until they hit
 * "send message" and get a backend error:
 *   - "needs_provision": user edited devcontainer/runtime config and saved.
 *     The PATCH cleared cached_image; a chat now would 500 with
 *     "Crew has devcontainer configuration but no provisioned image".
 *     Show an amber banner with a Provision button.
 *   - "running": polled job is mid-build. Show progress + ETA-ish hint.
 *   - "failed": the last build crashed. Show the error inline so the user
 *     sees WHY (e.g. a feature with a missing required parameter), not
 *     a generic toast.
 *
 * Polls every 3s while busy, every 30s when idle. Bails as soon as a
 * stable terminal state is reached.
 */

export { HealthCard, QuickAction }
