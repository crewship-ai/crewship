/**
 * Shared log formatting utilities for agent log viewers.
 * Used by both the full logs page and the compact task live logs panel.
 */

const SECRET_RE = /(?:sk-[a-zA-Z0-9_-]{10,}|ghp_[a-zA-Z0-9]{36,}|gho_[a-zA-Z0-9]{36,}|xoxb-[a-zA-Z0-9-]+|AIza[a-zA-Z0-9_-]{35}|eyJ[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]+)/g

/** Replace known secret patterns (API keys, tokens) with `***` to prevent accidental exposure in logs. */
export function redactSecrets(s: string): string {
  return s.replace(SECRET_RE, "***")
}

/** Format an ISO timestamp as "YYYY-MM-DD HH:MM:SS" for log display. Falls back to raw string on parse failure. */
export function formatLogTime(ts: string): string {
  try {
    const d = new Date(ts)
    return d.toISOString().slice(0, 19).replace("T", " ")
  } catch {
    return ts
  }
}

/** Tailwind CSS color classes for log severity levels. */
export const LEVEL_COLORS: Record<string, string> = {
  INFO: "text-neutral-500",
  WARN: "text-amber-500",
  ERROR: "text-red-500",
}

/** Tailwind CSS color classes for agent event types in the log viewer. */
export const EVENT_COLORS: Record<string, string> = {
  status: "text-yellow-400",
  thinking: "text-neutral-500",
  text: "text-white",
  tool_call: "text-cyan-400",
  tool_result: "text-emerald-400",
  rate_limit: "text-amber-400",
  failover: "text-yellow-400",
  error: "text-red-400",
  result: "text-purple-400",
  system: "text-blue-400",
  image: "text-pink-400",
}

/** A structured log entry from an agent, with event type and optional metadata. */
export interface LogEntry {
  ts: string
  level: string
  agent: string
  event: string
  content?: string
  metadata?: Record<string, unknown>
}
