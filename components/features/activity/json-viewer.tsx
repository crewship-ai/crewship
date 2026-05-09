"use client"

import { useMemo, useState } from "react"
import { cn } from "@/lib/utils"
import { Braces, Copy, Table as TableIcon } from "lucide-react"

// JSONViewer — small JSON inspector with two modes:
//   - JSON: pretty-printed, monospace (the default everywhere)
//   - Table: top-level key→value rows when the value is an object,
//     or row-per-item when it's an array of objects
//
// The Table view is the n8n hero pattern — it makes nested API
// responses readable at a glance without forcing the user to parse
// indentation.
//
// Schema view is deferred — it's not adding much over Table for
// hand-written DSL pipelines. Worth re-adding if/when we ship
// generated agents that emit arbitrary JSON shapes.

interface JSONViewerProps {
  // Raw value — string output from the run record (likely JSON-encoded
  // text), or already-parsed object for inputs we know are structured.
  value: unknown
  // Optional cap on bytes / characters before we truncate to keep the
  // panel lightweight. Default 64KB.
  maxChars?: number
}

type Mode = "json" | "table"

export function JSONViewer({ value, maxChars = 65_536 }: JSONViewerProps) {
  const [mode, setMode] = useState<Mode>("json")
  const [copied, setCopied] = useState(false)

  const parsed = useMemo(() => parseInput(value), [value])
  // Table mode is only meaningful for objects/arrays. Strings/numbers
  // fall back to JSON view automatically.
  const tableEnabled = parsed.kind === "object" || parsed.kind === "array"

  const onCopy = async () => {
    // navigator.clipboard is undefined under SSR, in non-secure
    // contexts (insecure http://), and in some test runners — so
    // probe before calling. Without the guard, the inner await throws
    // a TypeError before the try-catch can swallow it.
    const cb =
      typeof navigator !== "undefined" ? navigator.clipboard : undefined
    if (!cb) return
    try {
      const text =
        typeof value === "string" ? value : JSON.stringify(value, null, 2)
      await cb.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* ignore — clipboard rejection is non-fatal */
    }
  }

  return (
    <div className="flex flex-col gap-1.5">
      {/* Toggle row */}
      <div className="flex items-center gap-1">
        <button
          type="button"
          onClick={() => setMode("json")}
          aria-pressed={mode === "json"}
          className={cn(
            "flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px]",
            mode === "json"
              ? "bg-blue-500/15 text-blue-300"
              : "text-muted-foreground/60 hover:text-foreground/80",
          )}
        >
          <Braces className="h-3 w-3" /> JSON
        </button>
        <button
          type="button"
          onClick={() => tableEnabled && setMode("table")}
          aria-pressed={mode === "table"}
          disabled={!tableEnabled}
          className={cn(
            "flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px]",
            !tableEnabled && "cursor-default opacity-40",
            mode === "table"
              ? "bg-blue-500/15 text-blue-300"
              : "text-muted-foreground/60 hover:text-foreground/80",
          )}
        >
          <TableIcon className="h-3 w-3" /> Table
        </button>
        <div className="flex-1" />
        <button
          type="button"
          onClick={onCopy}
          aria-label="Copy as JSON"
          className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] text-muted-foreground/60 hover:text-foreground/80"
        >
          <Copy className="h-3 w-3" />
          {copied ? "Copied" : "Copy"}
        </button>
      </div>

      {mode === "json" || !tableEnabled ? (
        <PrettyJSON parsed={parsed} maxChars={maxChars} />
      ) : (
        <TableJSON parsed={parsed} />
      )}
    </div>
  )
}

// ── helpers ─────────────────────────────────────────────────────────

type ParsedValue =
  | { kind: "scalar"; raw: string; typed: unknown }
  | { kind: "object"; obj: Record<string, unknown> }
  | { kind: "array"; arr: unknown[] }
  | { kind: "string"; str: string }
  | { kind: "empty" }

function parseInput(value: unknown): ParsedValue {
  if (value === undefined || value === null || value === "") return { kind: "empty" }
  if (typeof value === "string") {
    const trimmed = value.trim()
    if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
      try {
        const parsed = JSON.parse(trimmed)
        if (Array.isArray(parsed)) return { kind: "array", arr: parsed }
        if (parsed && typeof parsed === "object") {
          return { kind: "object", obj: parsed as Record<string, unknown> }
        }
        return { kind: "scalar", raw: String(parsed), typed: parsed }
      } catch {
        /* fall through to plain string */
      }
    }
    return { kind: "string", str: value }
  }
  if (Array.isArray(value)) return { kind: "array", arr: value }
  if (typeof value === "object") return { kind: "object", obj: value as Record<string, unknown> }
  return { kind: "scalar", raw: String(value), typed: value }
}

function PrettyJSON({
  parsed,
  maxChars,
}: {
  parsed: ParsedValue
  maxChars: number
}) {
  let body = ""
  if (parsed.kind === "empty") body = "(none)"
  else if (parsed.kind === "string") body = parsed.str
  else if (parsed.kind === "scalar") body = parsed.raw
  else {
    const data = parsed.kind === "object" ? parsed.obj : parsed.arr
    body = JSON.stringify(data, null, 2)
  }
  const truncated = body.length > maxChars
  const display = truncated ? body.slice(0, maxChars) + "\n… (truncated)" : body
  return (
    <pre className="overflow-auto whitespace-pre-wrap rounded bg-background/60 p-2 font-mono text-[10px] text-foreground/80">
      {display}
    </pre>
  )
}

function TableJSON({ parsed }: { parsed: ParsedValue }) {
  if (parsed.kind === "object") {
    const entries = Object.entries(parsed.obj)
    if (entries.length === 0) {
      return (
        <div className="rounded bg-background/60 p-2 text-[10px] text-muted-foreground/60">
          (empty object)
        </div>
      )
    }
    return (
      <div className="overflow-auto rounded border border-white/[0.06]">
        <table className="w-full text-[10px]">
          <thead className="bg-white/[0.04]">
            <tr>
              <th className="px-2 py-1 text-left font-medium text-muted-foreground/70">
                Key
              </th>
              <th className="px-2 py-1 text-left font-medium text-muted-foreground/70">
                Value
              </th>
            </tr>
          </thead>
          <tbody>
            {entries.map(([k, v]) => (
              <tr key={k} className="border-t border-white/[0.04]">
                <td className="px-2 py-1 align-top font-mono text-blue-300">{k}</td>
                <td className="px-2 py-1 align-top font-mono text-foreground/80">
                  {summarizeValue(v)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    )
  }
  if (parsed.kind === "array") {
    const sample = parsed.arr[0]
    const isArrayOfObjects =
      sample && typeof sample === "object" && !Array.isArray(sample)
    if (!isArrayOfObjects) {
      return (
        <div className="overflow-auto rounded border border-white/[0.06]">
          <table className="w-full text-[10px]">
            <thead className="bg-white/[0.04]">
              <tr>
                <th className="px-2 py-1 text-left font-medium text-muted-foreground/70">
                  #
                </th>
                <th className="px-2 py-1 text-left font-medium text-muted-foreground/70">
                  Value
                </th>
              </tr>
            </thead>
            <tbody>
              {parsed.arr.map((v, i) => (
                <tr key={i} className="border-t border-white/[0.04]">
                  <td className="px-2 py-1 font-mono text-muted-foreground/70">{i}</td>
                  <td className="px-2 py-1 font-mono text-foreground/80">
                    {summarizeValue(v)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )
    }
    // Array of objects — column per union of keys (capped at 8).
    const allKeys = new Set<string>()
    for (const item of parsed.arr) {
      if (item && typeof item === "object" && !Array.isArray(item)) {
        for (const k of Object.keys(item as Record<string, unknown>)) allKeys.add(k)
        if (allKeys.size > 8) break
      }
    }
    const columns = Array.from(allKeys).slice(0, 8)
    return (
      <div className="overflow-auto rounded border border-white/[0.06]">
        <table className="w-full text-[10px]">
          <thead className="bg-white/[0.04]">
            <tr>
              <th className="px-2 py-1 text-left font-medium text-muted-foreground/70">
                #
              </th>
              {columns.map((c) => (
                <th key={c} className="px-2 py-1 text-left font-medium text-blue-300">
                  {c}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {parsed.arr.map((item, i) => {
              const obj = (item ?? {}) as Record<string, unknown>
              return (
                <tr key={i} className="border-t border-white/[0.04]">
                  <td className="px-2 py-1 font-mono text-muted-foreground/70">{i}</td>
                  {columns.map((c) => (
                    <td key={c} className="px-2 py-1 font-mono text-foreground/80">
                      {summarizeValue(obj[c])}
                    </td>
                  ))}
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    )
  }
  return null
}

function summarizeValue(v: unknown): string {
  if (v === undefined) return ""
  if (v === null) return "null"
  if (typeof v === "string") {
    return v.length > 80 ? v.slice(0, 79) + "…" : v
  }
  if (typeof v === "number" || typeof v === "boolean") return String(v)
  // For nested objects/arrays, show a compact preview.
  try {
    const s = JSON.stringify(v)
    return s.length > 80 ? s.slice(0, 79) + "…" : s
  } catch {
    return String(v)
  }
}
