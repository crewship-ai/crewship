"use client"

import { useEffect, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import type { BottomPanelContext } from "./types"
import { EmptyState } from "./shared"

const NOISE_KEYS = new Set([
  "_count", "config_hash", "cached_image", "cached_requirements",
  "deleted_at", "webhook_secret", "mcp_config_json",
  "schedule_last_run", "schedule_next_run",
])

function filterNoise(rec: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(rec)) {
    if (NOISE_KEYS.has(k)) continue
    if (v === null || v === undefined || v === "") continue
    out[k] = v
  }
  return out
}

function toYaml(rec: Record<string, unknown>, indent = 0): string {
  const pad = "  ".repeat(indent)
  let out = ""
  for (const [k, v] of Object.entries(rec)) {
    if (v === null || v === undefined) continue
    if (typeof v === "object" && !Array.isArray(v)) {
      out += `${pad}${k}:\n${toYaml(v as Record<string, unknown>, indent + 1)}`
    } else if (Array.isArray(v)) {
      out += `${pad}${k}:\n`
      for (const item of v) {
        if (typeof item === "object" && item !== null) {
          out += `${pad}  - ${toYaml(item as Record<string, unknown>, indent + 2).replace(/^\s+/, "")}`
        } else {
          out += `${pad}  - ${item}\n`
        }
      }
    } else {
      // Escape backslashes BEFORE quotes — otherwise a value containing
      // `\"` becomes `\\"` (backslash + escaped quote) which JSON-style
      // parsers interpret as a literal backslash followed by an
      // unescaped quote, breaking the quoted run. Order matters.
      const s = typeof v === "string" && (v.includes("\n") || v.includes(":"))
        ? `"${v.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`
        : String(v)
      out += `${pad}${k}: ${s}\n`
    }
  }
  return out
}

/**
 * YAML — fetches the entity's record and renders a read-only YAML-ish
 * projection of the user-relevant fields. Not full YAML — agents/crews
 * have many fields that are noisy (timestamps, _count, container hashes).
 */
export function YamlTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelContext }) {
  const [data, setData] = useState<Record<string, unknown> | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Resolve the fetch URL from the context up front so the effect can depend
  // on the URL string (identity-stable) rather than the context object — a
  // non-memoized context would otherwise refetch + flash "Loading…" on every
  // parent render.
  let url: string | null = null
  switch (context?.kind) {
    case "agent": url = `/api/v1/agents/${context.agentId}?workspace_id=${workspaceId}`; break
    case "crew": url = `/api/v1/crews/${context.crewId}?workspace_id=${workspaceId}`; break
    case "mission": url = `/api/v1/crews/${context.crewId}/missions/${context.missionId}?workspace_id=${workspaceId}`; break
    case "routine": url = `/api/v1/workspaces/${workspaceId}/pipelines/${encodeURIComponent(context.slug)}`; break
  }

  useEffect(() => {
    if (!url) return
    let cancelled = false
    setData(null)
    setError(null)
    apiFetch(url)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((rec) => { if (!cancelled) setData(rec) })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [url])

  if (!context || context.kind === "run") return <EmptyState>Select an entity to see its spec.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (data === null) return <EmptyState>Loading…</EmptyState>

  const yaml = toYaml(filterNoise(data))

  return (
    <div className="h-full overflow-y-auto p-3 text-[11px] leading-relaxed font-mono text-foreground/80 whitespace-pre">
      <HighlightedYaml yaml={yaml} />
    </div>
  )
}

/**
 * HighlightedYaml — lightweight tokenizer that paints keys, strings,
 * numbers and booleans in distinct colors. Handles multi-line quoted
 * strings (system_prompt et al.) by tracking an "inside string" state
 * across lines.
 */
function HighlightedYaml({ yaml }: { yaml: string }) {
  const lines = yaml.split("\n")
  const out: React.ReactNode[] = []
  let inString = false

  const findUnescapedQuote = (s: string, start: number): number => {
    for (let i = start; i < s.length; i++) {
      if (s[i] === "\\") { i++; continue }
      if (s[i] === '"') return i
    }
    return -1
  }

  lines.forEach((line, idx) => {
    if (inString) {
      const closeIdx = findUnescapedQuote(line, 0)
      if (closeIdx === -1) {
        out.push(
          <div key={idx}>
            <span className="text-amber-200">{line.length === 0 ? " " : line}</span>
          </div>,
        )
      } else {
        const head = line.slice(0, closeIdx + 1)
        const tail = line.slice(closeIdx + 1)
        inString = false
        out.push(
          <div key={idx}>
            <span className="text-amber-200">{head}</span>
            {tail && <span>{tail}</span>}
          </div>,
        )
      }
      return
    }

    const indentMatch = line.match(/^(\s*)/)
    const indent = indentMatch ? indentMatch[0] : ""
    let rest = line.slice(indent.length)
    const tokens: React.ReactNode[] = []
    if (indent) tokens.push(<span key="indent">{indent}</span>)

    if (rest === "") {
      out.push(<div key={idx}>{tokens.length ? tokens : " "}</div>)
      return
    }

    if (rest.startsWith("- ")) {
      tokens.push(<span key="bullet" className="text-zinc-500">- </span>)
      rest = rest.slice(2)
    } else if (rest === "-") {
      tokens.push(<span key="bullet" className="text-zinc-500">-</span>)
      rest = ""
    }

    const kv = rest.match(/^([A-Za-z_][A-Za-z0-9_-]*):(\s*)(.*)$/)
    if (kv) {
      const [, key, sp, value] = kv
      tokens.push(<span key="key" className="text-sky-300">{key}</span>)
      tokens.push(<span key="colon" className="text-zinc-500">:</span>)
      if (sp) tokens.push(<span key="sp">{sp}</span>)
      if (value.length > 0) {
        tokens.push(...renderYamlValue(value, () => { inString = true }, findUnescapedQuote))
      }
    } else if (rest.length > 0) {
      tokens.push(<span key="rest">{rest}</span>)
    }

    out.push(<div key={idx}>{tokens}</div>)
  })

  return <>{out}</>
}

function renderYamlValue(
  value: string,
  setInString: () => void,
  findUnescapedQuote: (s: string, start: number) => number,
): React.ReactNode[] {
  if (value.startsWith('"')) {
    const closeIdx = findUnescapedQuote(value, 1)
    if (closeIdx === -1) {
      setInString()
      return [<span key="v" className="text-amber-200">{value}</span>]
    }
    const quoted = value.slice(0, closeIdx + 1)
    const trail = value.slice(closeIdx + 1)
    const nodes: React.ReactNode[] = [<span key="v" className="text-amber-200">{quoted}</span>]
    if (trail) nodes.push(<span key="t">{trail}</span>)
    return nodes
  }
  if (/^-?\d+(\.\d+)?$/.test(value)) {
    return [<span key="v" className="text-emerald-300">{value}</span>]
  }
  if (/^(true|false|null|yes|no|~)$/i.test(value)) {
    return [<span key="v" className="text-violet-300">{value}</span>]
  }
  return [<span key="v" className="text-amber-200">{value}</span>]
}
