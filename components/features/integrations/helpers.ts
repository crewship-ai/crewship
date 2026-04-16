import type { CrewIntegration } from "./types"

/**
 * Pure serialisation helpers shared by the integrations page and its
 * sub-components. Extracted from the original monolithic page.tsx so
 * the sub-components can import them directly without pulling in the
 * entire `"use client"` module.
 */

export function parseArgs(argsJson: string | null): string {
  if (!argsJson) return ""
  try {
    const arr = JSON.parse(argsJson) as string[]
    if (!Array.isArray(arr)) return ""
    // Round-trip safe: JSON-encode each arg and join, so spaces inside args are preserved
    return JSON.stringify(arr)
  } catch {
    return ""
  }
}

export function serializeArgs(argsStr: string): string {
  const trimmed = argsStr.trim()
  if (!trimmed) return "[]"
  // If it's already valid JSON array, use as-is
  try {
    const parsed = JSON.parse(trimmed)
    if (Array.isArray(parsed)) return JSON.stringify(parsed)
  } catch {
    // Not JSON — fall back to space-splitting (user typed plain text)
  }
  const parts = trimmed.split(/\s+/).filter(Boolean)
  return JSON.stringify(parts)
}

export function parseEnv(envJson: string | null): { key: string; value: string }[] {
  if (!envJson) return []
  try {
    const obj = JSON.parse(envJson) as Record<string, string>
    return Object.entries(obj).map(([key, value]) => ({ key, value }))
  } catch {
    return []
  }
}

export function serializeEnv(entries: { key: string; value: string }[]): string {
  return JSON.stringify(
    Object.fromEntries(entries.filter((e) => e.key.trim()).map((e) => [e.key, e.value])),
  )
}

export function subtitleFor(server: CrewIntegration): string {
  if (server.transport === "streamable-http") return server.endpoint ?? ""
  const cmd = server.command ?? ""
  const args = parseArgs(server.args_json)
  return `${cmd} ${args}`.trim()
}
