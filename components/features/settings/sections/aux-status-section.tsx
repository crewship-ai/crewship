"use client"

import { useCallback, useEffect, useState } from "react"
import { RefreshCw } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"

// PR-G F3 UI surface — auxiliary model slot diagnostic panel.
//
// Read-only by design (yaml override coming in PR-F per PRD §6.1
// Tier-2 #1). Operator sees per-slot which provider / model / timeout
// is resolved, and whether the value came from explicit yaml config
// or fell back to the bundled DefaultAuxiliaryModels().
//
// Backend contract:
//   GET /api/v1/system/aux-status  → { slots: [{ slot, provider, model, timeout_ms, source }, ...] }
//
// Auth: any authenticated workspace member (values are non-secret).

interface AuxSlot {
  slot: string
  provider: string
  model: string
  timeout_ms: number
  source: "explicit" | "fallback"
}

interface AuxStatusResponse {
  slots: AuxSlot[]
}

const SLOT_DESCRIPTIONS: Record<string, string> = {
  curator: "Daily skill review + memory consolidation routines",
  behavior: "F4.2 behavior monitor post-tool-call evaluations",
  memory_search: "F1 memory.search ranking auxiliary",
  skill_review: "F4.1 skill activation / archive recommendations",
  negative_learning: "F4.4 negative lesson capture",
}

function SourcePill({ source }: { source: AuxSlot["source"] }) {
  if (source === "explicit") {
    return (
      <span className="text-[10px] px-1.5 py-0.5 rounded bg-primary/15 text-primary-hover uppercase tracking-wider">
        explicit
      </span>
    )
  }
  return (
    <span className="text-[10px] px-1.5 py-0.5 rounded bg-muted text-muted-foreground uppercase tracking-wider">
      fallback
    </span>
  )
}

export function AuxStatusSection() {
  const [slots, setSlots] = useState<AuxSlot[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setErr(null)
    try {
      const res = await apiFetch("/api/v1/system/aux-status")
      if (!res.ok) {
        setErr(`Failed (HTTP ${res.status})`)
        return
      }
      const body = (await res.json()) as unknown
      // Unchecked `as AuxStatusResponse` could push `undefined` or a
      // non-array into setSlots, which then explodes on the
      // `slots.map(...)` render path with "x.map is not a function".
      // Validate the shape at the boundary so a backend regression or
      // hostile response surfaces as a friendly error string instead
      // of a React crash. CodeRabbit round-11 catch.
      if (
        !body ||
        typeof body !== "object" ||
        !Array.isArray((body as { slots?: unknown }).slots)
      ) {
        setErr("Unexpected response shape from /api/v1/system/aux-status")
        return
      }
      setSlots((body as AuxStatusResponse).slots)
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to load")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">Auxiliary models</h2>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => void load()}
          disabled={loading}
          className="text-xs h-7"
          title="Re-read aux-status from the server"
          data-testid="aux-status-refresh"
        >
          <RefreshCw className={loading ? "h-3 w-3 mr-1.5 animate-spin" : "h-3 w-3 mr-1.5"} />
          Refresh
        </Button>
      </div>
      <p className="text-xs text-muted-foreground -mt-1">
        Each slot is the cheap / fast model the keeper invokes for that subsystem (PRD §6 F3).
        <span className="ml-1">
          Per-workspace YAML overrides are on the Tier-2 roadmap; today values come from
          built-in defaults unless the server was started with explicit env-set overrides.
        </span>
      </p>

      {loading && (
        <div className="rounded-xl border border-white/8 bg-card p-4 flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner className="h-3.5 w-3.5" /> Loading…
        </div>
      )}
      {err && (
        <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-4 text-sm text-red-300">{err}</div>
      )}
      {!loading && !err && slots.length === 0 && (
        <div className="rounded-xl border border-white/8 bg-card p-4 text-sm text-muted-foreground">
          No auxiliary slots configured. The keeper will refuse F4 endpoints with 503 until
          at least one slot is reachable (set <code className="text-[10px]">ANTHROPIC_API_KEY</code> and
          restart, or wire an explicit override in <code className="text-[10px]">crewship.yaml</code>).
        </div>
      )}
      {!loading && !err && slots.length > 0 && (
        <div className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
          {slots.map((s) => (
            <div
              key={s.slot}
              className="px-4 py-2.5 grid grid-cols-12 items-center gap-2 text-sm"
              data-testid={`aux-slot-${s.slot}`}
            >
              <div className="col-span-3 font-mono text-xs">{s.slot}</div>
              <div className="col-span-2 text-muted-foreground">{s.provider || "—"}</div>
              <div className="col-span-4 truncate" title={s.model}>
                {s.model || "—"}
              </div>
              <div className="col-span-2 text-right text-muted-foreground tabular-nums text-xs">
                {s.timeout_ms}ms
              </div>
              <div className="col-span-1 text-right">
                <SourcePill source={s.source} />
              </div>
              <div className="col-span-12 text-[11px] text-muted-foreground pl-0">
                {SLOT_DESCRIPTIONS[s.slot] ?? ""}
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
