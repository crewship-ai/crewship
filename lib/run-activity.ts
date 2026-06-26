// Humanize raw journal entries into a readable per-run activity timeline.
//
// The journal (internal/journal) already records everything an agent does
// during a run — exec.command, file.written, network.egress, llm.call,
// run.started/completed/failed — all sharing one trace_id. This module turns
// those raw, machine-shaped entries into one-line "what the agent did" rows
// for the RunActivityTimeline rail. Pure + framework-free so it unit-tests
// without React.
//
// Keep the entry-type coverage mirrored with lib/types/journal.ts. New types
// fall back to their `summary` text; explicit noise types are dropped.

import { iconForEntryType } from "@/lib/journal-icons"
import type { JournalEntry } from "@/lib/types/journal"
import type { LucideIcon } from "lucide-react"

/** Visual tone for a row; the timeline maps these to icon/accent colours. */
export type RunActivityTone = "default" | "active" | "success" | "warn" | "error"

export interface RunActivityRow {
  id: string
  ts: string
  icon: LucideIcon
  tone: RunActivityTone
  /** Primary one-liner, e.g. "Wrote file" or "Fetched news.ycombinator.com". */
  title: string
  /** Optional second line with the concrete target (path, command, url). */
  detail?: string
  /** Optional right-aligned metadata, e.g. "412 B" or "exit 0 · 1.2s". */
  meta?: string
}

// Entry types that are pure machine noise for a human-facing run timeline:
// output chunks, per-tick metrics, cache hits, presence pings. These get
// dropped (humanizeEntry returns null) so the rail stays the highlights reel.
const NOISE_TYPES = new Set<string>([
  "exec.output_chunk",
  "container.metrics",
  "container.snapshot",
  "llm.cache_hit",
  "agent.status_change",
])

// ---- small payload accessors (payload is free-form JSON) -------------------

function str(p: Record<string, unknown> | undefined, ...keys: string[]): string | undefined {
  if (!p) return undefined
  for (const k of keys) {
    const v = p[k]
    if (typeof v === "string" && v.length > 0) return v
  }
  return undefined
}

function num(p: Record<string, unknown> | undefined, ...keys: string[]): number | undefined {
  if (!p) return undefined
  for (const k of keys) {
    const v = p[k]
    if (typeof v === "number" && Number.isFinite(v)) return v
  }
  return undefined
}

// ---- formatting helpers (exported for direct unit coverage) ----------------

/** Human byte size: "412 B", "2.0 KB", "5.0 MB". Null for junk input. */
export function formatBytes(n: number | undefined): string | null {
  if (n === undefined || !Number.isFinite(n) || n < 0) return null
  if (n < 1024) return `${Math.round(n)} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

/** Human duration: "820ms", "1.2s", "1m 5s". Null for junk input. */
export function formatDuration(ms: number | undefined): string | null {
  if (ms === undefined || !Number.isFinite(ms) || ms < 0) return null
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const mins = Math.floor(ms / 60_000)
  const secs = Math.round((ms % 60_000) / 1000)
  return `${mins}m ${secs}s`
}

/** Format a USD cost without lying about zero: "$0.0021". */
function formatCost(usd: number | undefined): string | null {
  if (usd === undefined || !Number.isFinite(usd) || usd < 0) return null
  return `$${usd.toFixed(4)}`
}

/** Join non-empty parts with the middle-dot separator the UI uses elsewhere. */
function joinMeta(...parts: (string | null | undefined)[]): string | undefined {
  const kept = parts.filter((p): p is string => !!p)
  return kept.length ? kept.join(" · ") : undefined
}

/** Strip scheme/path from a URL or host so the title stays short. */
function hostOnly(raw: string | undefined): string | undefined {
  if (!raw) return undefined
  return raw.replace(/^https?:\/\//, "").replace(/\/.*$/, "")
}

// ---- the mapping -----------------------------------------------------------

/**
 * Convert one journal entry into a readable timeline row, or null when the
 * entry is noise / not worth surfacing in a human run feed.
 */
export function humanizeEntry(e: JournalEntry): RunActivityRow | null {
  if (NOISE_TYPES.has(e.entry_type)) return null

  const p = e.payload
  const icon = iconForEntryType(e.entry_type)
  const base = { id: e.id, ts: e.ts, icon }

  switch (e.entry_type) {
    case "run.started":
    case "assignment.running":
      return { ...base, tone: "active", title: "Run started", detail: actorLabel(e) }

    case "run.completed":
    case "assignment.completed": {
      const meta = joinMeta(
        formatCost(num(p, "cost_usd", "cost")),
        stepLabel(num(p, "steps", "step_count")),
        formatDuration(num(p, "duration_ms")),
      )
      return { ...base, tone: "success", title: "Completed", meta }
    }

    case "run.failed":
    case "assignment.failed":
      return {
        ...base,
        tone: "error",
        title: "Failed",
        detail: str(p, "error", "message") ?? (e.summary || undefined),
        meta: joinMeta(formatDuration(num(p, "duration_ms"))),
      }

    case "run.cancelled":
      return { ...base, tone: "warn", title: "Cancelled", detail: e.summary || undefined }

    case "run.timeout":
      return { ...base, tone: "error", title: "Timed out", detail: e.summary || undefined }

    case "network.egress": {
      const host = hostOnly(str(p, "host", "url"))
      const method = str(p, "method")
      const status = num(p, "status_code")
      const errTone = status !== undefined && status >= 400
      return {
        ...base,
        tone: errTone ? "error" : "default",
        title: host ? `Fetched ${host}` : "Network request",
        meta: joinMeta(method, status !== undefined ? String(status) : null),
      }
    }

    case "exec.command": {
      const cmd = str(p, "command", "cmd")
      const exit = num(p, "exit_code")
      const failed = exit !== undefined && exit !== 0
      return {
        ...base,
        tone: failed ? "error" : "default",
        title: "Ran command",
        detail: cmd,
        meta: joinMeta(
          exit !== undefined ? `exit ${exit}` : null,
          formatDuration(num(p, "duration_ms")),
        ),
      }
    }

    case "file.written": {
      const op = str(p, "op")
      const deleted = op === "deleted" || op === "removed"
      return {
        ...base,
        tone: "default",
        title: deleted ? "Deleted file" : "Wrote file",
        detail: str(p, "path"),
        meta: deleted ? undefined : formatBytes(num(p, "size")) ?? undefined,
      }
    }

    case "network.port_opened": {
      const port = num(p, "port")
      return { ...base, tone: "default", title: port ? `Opened port ${port}` : "Opened port" }
    }

    case "llm.call": {
      const model = str(p, "model")
      const inTok = num(p, "input_tokens", "prompt_tokens")
      const outTok = num(p, "output_tokens", "completion_tokens")
      const tokens = inTok !== undefined || outTok !== undefined
        ? `${(inTok ?? 0) + (outTok ?? 0)} tok`
        : null
      return {
        ...base,
        tone: "default",
        title: "Model call",
        detail: model,
        meta: joinMeta(tokens, formatCost(num(p, "cost_usd", "cost"))),
      }
    }

    case "keeper.request":
      return { ...base, tone: "warn", title: "Requested credential", detail: e.summary || undefined }

    case "keeper.decision": {
      const decision = (str(p, "decision") ?? "").toLowerCase()
      const denied = decision === "deny" || decision === "denied"
      return {
        ...base,
        tone: denied ? "error" : "success",
        title: denied ? "Credential denied" : "Credential granted",
        detail: e.summary || undefined,
      }
    }

    default: {
      // Known-but-unmapped (memory.updated, peer.conversation, …): show the
      // backend's human summary if it wrote one. No summary → not worth a row.
      const summary = e.summary?.trim()
      if (!summary) return null
      return {
        ...base,
        tone: severityTone(e.severity),
        title: summary,
      }
    }
  }
}

/** Map a run's journal entries to readable rows, oldest first, noise removed. */
export function humanizeRun(entries: JournalEntry[]): RunActivityRow[] {
  return entries
    .map(humanizeEntry)
    .filter((r): r is RunActivityRow => r !== null)
    .sort((a, b) => (a.ts < b.ts ? -1 : a.ts > b.ts ? 1 : 0))
}

// ---- tiny helpers ----------------------------------------------------------

function actorLabel(e: JournalEntry): string | undefined {
  return str(e.payload, "triggered_by", "actor_name") ?? e.actor_id ?? undefined
}

function stepLabel(n: number | undefined): string | null {
  if (n === undefined || n < 0) return null
  return `${n} step${n === 1 ? "" : "s"}`
}

function severityTone(sev: unknown): RunActivityTone {
  switch (sev) {
    case "error":
      return "error"
    case "warn":
      return "warn"
    default:
      return "default"
  }
}
