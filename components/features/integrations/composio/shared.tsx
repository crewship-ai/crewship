"use client"

import * as React from "react"

import { StatusPill } from "@/components/ui/status-pill"

import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import type { Toolkit, BindingMode } from "./types"

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
      className="flex items-center justify-center rounded bg-primary/10 text-[10px] font-semibold uppercase text-primary"
      style={{ width: size, height: size }}
    >
      {toolkit.slug.slice(0, 2)}
    </span>
  )
}

/** Composio account state as the one status pill; the raw enum used to be
 *  printed beside an icon (ACTIVE, INITIATED, FAILED). */
export function StatusDot({ status }: { status: string }) {
  return <StatusPill status={status} />
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

// Title-case a toolkit slug for chip/label display ("gmail" → "Gmail").
export function toolkitLabel(slug: string): string {
  if (!slug) return ""
  return slug.charAt(0).toUpperCase() + slug.slice(1)
}

// Read/write heuristic for a Composio tool slug. Tools whose name carries a
// read-shaped verb (GET/LIST/FETCH/SEARCH/READ/FIND/VIEW) are treated as
// read-only; everything else is a write. Used by the custom tool picker and the
// "Read-only" quick-select.
const READ_VERBS = new Set([
  "GET", "LIST", "FETCH", "SEARCH", "READ", "FIND", "DOWNLOAD", "EXPORT",
  "RETRIEVE", "COUNT", "CHECK", "VIEW", "GETPROFILE",
])
// Mutation tokens that disqualify a slug from "read" even when it also carries a
// read verb (e.g. CALENDAR_LIST_INSERT). Mirrors the server's composioWriteVerbs.
const WRITE_VERBS = new Set([
  "CREATE", "UPDATE", "DELETE", "INSERT", "PATCH", "PUT", "POST", "SEND", "ADD",
  "REMOVE", "MODIFY", "SET", "MOVE", "TRASH", "CLEAR", "IMPORT", "WRITE", "REPLY",
  "FORWARD", "ENABLE", "DISABLE", "REVOKE", "MARK", "ARCHIVE", "UNARCHIVE",
  "ASSIGN", "RENAME", "DUPLICATE", "COPY", "UPLOAD", "BATCH", "DRAFT", "REPLACE",
  "MERGE", "CANCEL", "DECLINE", "ACCEPT", "APPROVE", "SUBMIT", "UNTRASH",
  "RESTORE", "STOP", "START", "RUN", "EXECUTE", "TRIGGER", "PUBLISH",
  "UNPUBLISH", "LOCK", "UNLOCK",
])
export function isReadTool(slug: string): boolean {
  // Strip the leading toolkit prefix, tokenize; write verbs win, then read verbs.
  const tokens = slug.toUpperCase().replace(/^[^_]+_/, "").split("_")
  if (tokens.some((t) => WRITE_VERBS.has(t))) return false
  return tokens.some((t) => READ_VERBS.has(t))
}

// A scope chip: brand icon + "<App> · <Scope>", colour-coded by mode
// (full=emerald, read=blue, custom=amber). Shared by the agent-access list and
// the per-agent Connectors card so both read from one renderer.
const SCOPE_STYLES: Record<BindingMode, string> = {
  full: "border-success/30 bg-success/[0.08] text-success",
  read: "border-blue-400/35 bg-blue-500/[0.12] text-blue-300",
  custom: "border-warn/30 bg-warn/[0.08] text-warn",
}

export function ScopeChip({
  toolkit,
  mode,
  count,
}: {
  toolkit: Toolkit
  mode: BindingMode
  count?: number
}) {
  const label =
    mode === "full"
      ? "Full"
      : mode === "read"
        ? "Read-only"
        : count != null
          ? `Custom (${count})`
          : "Custom"
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11px]",
        SCOPE_STYLES[mode],
      )}
    >
      <ToolkitIcon toolkit={toolkit} size={14} />
      <span>
        {toolkitLabel(toolkit.slug)} · {label}
      </span>
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
