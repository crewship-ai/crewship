"use client"

import { useCallback, useEffect, useState } from "react"
import { RefreshCw } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"
import { SettingsCard, SettingsRow, SettingsEmpty } from "../shared"

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

// Keys MUST match the backend slot ids emitted by
// GET /api/v1/system/aux-status (llm.Slot constants in
// internal/llm/aux.go: curator, keeper, behavior, memory_health,
// negative). A mismatch here silently blanks the description column
// (#866.4).
const SLOT_DESCRIPTIONS: Record<string, string> = {
  curator: "Daily skill review + memory consolidation routines",
  keeper: "Keeper evaluator gate — lesson / skill / escalation decisions",
  behavior: "F4.2 behavior monitor post-tool-call evaluations",
  memory_health: "F1 memory-health audit + consolidation scoring",
  negative: "F4.4 negative lesson capture",
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
  const { workspaceId } = useWorkspace()
  const [slots, setSlots] = useState<AuxSlot[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setErr(null)
    try {
      // aux-status is ADMIN+ (#868) — the endpoint resolves the caller's role
      // from the workspace, so the id must be supplied.
      const res = await apiFetch(`/api/v1/system/aux-status?workspace_id=${workspaceId ?? ""}`)
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
  }, [workspaceId])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <SettingsCard
      title="Auxiliary models"
      description="Each slot is the cheap / fast model the keeper invokes for that subsystem (PRD §6 F3). Per-workspace YAML overrides are on the Tier-2 roadmap; today values come from built-in defaults unless the server was started with explicit env-set overrides."
      actions={
        <>
          <Badge
            variant="outline"
            className="text-[10px] px-1.5 py-0.5 h-5 bg-muted text-muted-foreground uppercase tracking-wider border-transparent"
            title="This tab is a read-only diagnostic. Slots are configured via YAML (per-workspace overrides are on the roadmap)."
          >
            read-only
          </Badge>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => void load()}
            disabled={loading}
            className="h-7 px-2.5 text-xs"
            title="Re-read aux-status from the server"
            data-testid="aux-status-refresh"
          >
            <RefreshCw className={loading ? "h-3 w-3 mr-1.5 animate-spin" : "h-3 w-3 mr-1.5"} />
            Refresh
          </Button>
        </>
      }
    >
      {loading ? (
        <div className="p-4 flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner className="h-3.5 w-3.5" /> Loading…
        </div>
      ) : err ? (
        <div role="alert" className="p-4 text-sm text-destructive">{err}</div>
      ) : slots.length === 0 ? (
        <SettingsEmpty>
          No auxiliary slots configured. The keeper will refuse F4 endpoints with 503 until
          at least one slot is reachable (set <code className="text-[10px]">ANTHROPIC_API_KEY</code> and
          restart, or wire an explicit override in <code className="text-[10px]">crewship.yaml</code>).
        </SettingsEmpty>
      ) : (
        slots.map((s, idx) => (
          // The outer div + ".col-span-12" description class are load-bearing
          // for the existing #866.4 regression test
          // (components/features/settings/__tests__/aux-status-section.test.tsx),
          // which asserts on `[data-testid="aux-slot-<slot>"]` containing a
          // non-empty `.col-span-12` description cell. Kept as-is rather than
          // reshaping that test — this pass is presentational only.
          <div key={s.slot} data-testid={`aux-slot-${s.slot}`}>
            <SettingsRow
              label={<span className="font-mono">{s.slot}</span>}
              description={<span className="col-span-12">{SLOT_DESCRIPTIONS[s.slot] ?? ""}</span>}
              border={idx < slots.length - 1}
            >
              <div className="flex items-center gap-3 text-xs">
                <span className="text-muted-foreground">{s.provider || "—"}</span>
                <span className="truncate max-w-[200px]" title={s.model}>{s.model || "—"}</span>
                <span className="text-muted-foreground tabular-nums">{s.timeout_ms}ms</span>
                <SourcePill source={s.source} />
              </div>
            </SettingsRow>
          </div>
        ))
      )}
    </SettingsCard>
  )
}
