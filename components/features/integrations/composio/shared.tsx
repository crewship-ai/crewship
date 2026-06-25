"use client"

import * as React from "react"
import { CheckCircle2, AlertCircle } from "lucide-react"

import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import type { Toolkit } from "./types"

// Composio serves brand logos off this CDN (allowed by the dashboard CSP).
// When the catalog doesn't carry an explicit logo URL we derive one from the
// toolkit slug so connected-account / agent-access chips still render a brand
// mark instead of bare initials.
export function brandLogo(slug: string): string {
  return `https://logos.composio.dev/api/${slug}`
}

// ToolkitIcon renders a Composio brand logo as a plain <img> (next/image chokes
// on remote SVGs under static export). Falls back from the explicit logo → the
// slug-derived CDN logo → a tinted two-letter glyph.
export function ToolkitIcon({ toolkit, size = 20 }: { toolkit: Toolkit; size?: number }) {
  const src = toolkit.logo || (toolkit.slug ? brandLogo(toolkit.slug) : "")
  const [failed, setFailed] = React.useState(false)
  // Reset the fallback state when the underlying logo source changes — otherwise
  // a re-used component instance keeps showing the glyph for a different toolkit
  // after one icon 404s.
  React.useEffect(() => {
    setFailed(false)
  }, [src])
  if (src && !failed) {
    return (
      <img
        src={src}
        alt=""
        width={size}
        height={size}
        className="rounded object-contain"
        onError={() => setFailed(true)}
      />
    )
  }
  return (
    <span
      className="flex items-center justify-center rounded bg-blue-500/10 text-[10px] font-semibold uppercase text-blue-400"
      style={{ width: size, height: size }}
    >
      {toolkit.slug.slice(0, 2)}
    </span>
  )
}

export function StatusDot({ status }: { status: string }) {
  const ok = status?.toUpperCase() === "ACTIVE" || status?.toUpperCase() === "ENABLED"
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 text-[11px]",
        ok ? "text-emerald-400" : "text-amber-400",
      )}
    >
      {ok ? <CheckCircle2 className="h-3 w-3" /> : <AlertCircle className="h-3 w-3" />}
      {status}
    </span>
  )
}

export function EmptyHint({ text }: { text: string }) {
  return (
    <div className="rounded-xl border border-dashed border-white/10 p-4 text-[11px] text-muted-foreground">
      {text}
    </div>
  )
}

// A connected-account / app pill (brand icon + slug), reused across tabs.
export function AppChip({
  toolkit,
  children,
}: {
  toolkit: Toolkit
  children?: React.ReactNode
}) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-lg border border-white/10 bg-white/[0.03] px-2 py-1 text-[11px]">
      <ToolkitIcon toolkit={toolkit} size={14} />
      <span className="capitalize">{toolkit.slug}</span>
      {children}
    </span>
  )
}

export function TableSkeleton({ rows = 4 }: { rows?: number }) {
  return (
    <div className="space-y-2">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-11 rounded-lg" />
      ))}
    </div>
  )
}
