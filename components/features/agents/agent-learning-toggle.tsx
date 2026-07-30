"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { Sparkles } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"

// PR-G F4.1 UX — per-agent self-learning toggle.
//
// Renders the agents.self_learning_enabled flag (v106) as a switch
// with an inline reason field that appears when the user is about to
// flip it. Reason is required server-side because every flip is
// audit-relevant — we don't surface an opt-out for the reason field.
//
// Backend contract:
//   GET   /api/v1/agents/{agentId}/learning  → { enabled, set_by_user_id?, set_at?, reason? }
//   PATCH /api/v1/agents/{agentId}/learning  ← { enabled, reason }   (ADMIN+)
//
// The toggle is OFF by default (governance-first). Turning it ON
// surfaces a one-paragraph explanation of what changes — operators
// shouldn't flip this without knowing the consequence.

interface LearningResponse {
  agent_id: string
  enabled: boolean
  set_by_user_id?: string | null
  set_at?: string | null
  reason?: string | null
}

export interface AgentLearningToggleProps {
  agentId: string
  workspaceId: string
  canEdit?: boolean
  /**
   * Drop the card chrome — the caller already supplies one. Without this the
   * component nests a bordered card inside a bordered card, which reads as a
   * mistake rather than a hierarchy.
   */
  bare?: boolean
}

export function AgentLearningToggle({ agentId, workspaceId, canEdit , bare = false }: AgentLearningToggleProps) {
  const SHELL = bare ? "p-4" : "rounded-xl border border-white/8 bg-card p-4"
  // Mirrors the CrewPolicyControls pattern: if caller passes canEdit
  // explicitly we honor it (lets admin overlays override), otherwise
  // derive from CASL abilities. Self-learning is ADMIN+ on the server
  // so it lines up with `manage`/`update` on Agent. Server is still
  // authoritative — UI greying is a UX hint, not a security boundary.
  const { abilities } = useAbilities()
  const effectiveCanEdit = useMemo(() => {
    if (typeof canEdit === "boolean") return canEdit
    // Backend PATCH requires canRole("manage") — mirror exactly so the
    // UI doesn't enable the toggle for a user who'll then bounce off
    // 403 at save time. Broader "update" permission is insufficient.
    return abilities.can("manage", "Agent")
  }, [canEdit, abilities])
  const [state, setState] = useState<LearningResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [pendingEnabled, setPendingEnabled] = useState<boolean | null>(null)
  const [reason, setReason] = useState("")

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true)
    setErr(null)
    try {
      const res = await apiFetch(
        `/api/v1/agents/${agentId}/learning?workspace_id=${encodeURIComponent(workspaceId)}`,
        { signal },
      )
      if (!res.ok) {
        setErr(`Failed to load (HTTP ${res.status})`)
        return
      }
      const body = (await res.json()) as LearningResponse
      if (signal?.aborted) return
      setState(body)
    } catch (e) {
      // Ignore aborts — they happen by design when agentId / workspaceId
      // change while a previous request is still in flight (stale-
      // response guard).
      if (e instanceof DOMException && e.name === "AbortError") return
      setErr(e instanceof Error ? e.message : "Failed to load")
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => {
    // AbortController per effect run: if agentId / workspaceId change
    // before the previous fetch resolves, abort it so its setState
    // doesn't overwrite the new identifier's state. Without this, a
    // slow first request can land AFTER the second response and the
    // operator sees the wrong agent's flag.
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const currentEnabled = state?.enabled ?? false
  const target = pendingEnabled ?? currentEnabled
  const dirty = pendingEnabled !== null && pendingEnabled !== currentEnabled

  const save = useCallback(async () => {
    if (pendingEnabled === null) return
    if (reason.trim() === "") {
      toast.error("Reason is required (audit trail)")
      return
    }
    setSaving(true)
    try {
      const res = await apiFetch(
        `/api/v1/agents/${agentId}/learning?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ enabled: pendingEnabled, reason: reason.trim() }),
        },
      )
      if (!res.ok) {
        let msg = `HTTP ${res.status}`
        try {
          const e = (await res.json()) as { error?: string }
          if (e.error) msg = e.error
        } catch {
          /* keep */
        }
        toast.error(`Failed: ${msg}`)
        return
      }
      const body = (await res.json()) as LearningResponse
      setState(body)
      setPendingEnabled(null)
      setReason("")
      toast.success(body.enabled ? "Self-learning enabled" : "Self-learning disabled")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to update")
    } finally {
      setSaving(false)
    }
  }, [agentId, workspaceId, pendingEnabled, reason])

  if (loading) {
    return (
      <div className={cn(SHELL, "flex items-center gap-2 text-sm text-muted-foreground")}>
        <Spinner className="h-3.5 w-3.5" /> Loading…
      </div>
    )
  }
  if (err) {
    return (
      <div className={cn(bare ? "p-4" : "rounded-xl border border-destructive/30 bg-destructive/5 p-4", "text-sm text-destructive")}>
        {err}
      </div>
    )
  }

  return (
    <div className={cn(SHELL, "space-y-3")}>
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-start gap-2.5">
          <Sparkles className={target ? "h-4 w-4 text-success mt-0.5" : "h-4 w-4 text-muted-foreground mt-0.5"} />
          <div>
            <div className="type-row font-medium">Self-improving mode</div>
            <div className="type-meta mt-1 max-w-xl leading-relaxed text-muted-foreground">
              {/* Says what the flag actually gates. Its only two readers are
                  keeper_phase2.go (the lesson) and agent_persona.go (the
                  persona). The previous wording also promised that skills
                  "flip to active", which no code path does. */}
              This does not decide whether the agent learns — it decides <b className="font-medium text-foreground">who
              signs off</b> when it wants to edit its own notes: a lesson captured from a run
              (<code className="font-mono">lessons.md</code>) or a change to how it describes itself
              (<code className="font-mono">PERSONA.md</code>).
              <span className="mt-1 block">
                <b className="font-medium text-foreground">On</b> — the change is written straight away.{" "}
                <b className="font-medium text-foreground">Off</b> — it waits for you as a blocking inbox item.
              </span>
              <span className="mt-1 block">
                Anything the evaluator denies or escalates comes to you either way, and this can only
                tighten the crew&rsquo;s autonomy level, never loosen it.
              </span>
            </div>
          </div>
        </div>
        <Switch
          checked={target}
          onCheckedChange={(checked) => setPendingEnabled(checked)}
          disabled={!effectiveCanEdit || saving}
          data-testid="agent-learning-switch"
          aria-label="Toggle self-improving mode"
        />
      </div>

      {dirty && (
        <div className="space-y-2 pt-2 border-t border-white/5">
          <label
            htmlFor={`agent-learning-reason-${agentId}`}
            className="block text-xs uppercase tracking-wider text-muted-foreground"
          >
            Reason (required)
          </label>
          <Input
            id={`agent-learning-reason-${agentId}`}
            type="text"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={pendingEnabled ? "why grant autonomy to this agent?" : "why revoke autonomy?"}
            disabled={saving}
          />
          <div className="flex items-center gap-2">
            <Button
              type="button"
              size="sm"
              onClick={() => { void save() }}
              disabled={reason.trim() === "" || saving}
            >
              {saving ? "Saving…" : "Confirm"}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => {
                setPendingEnabled(null)
                setReason("")
              }}
              disabled={saving}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}

      {state?.set_at && (
        <div className="text-[11px] text-muted-foreground pt-2 border-t border-white/5">
          Last changed {new Date(state.set_at).toLocaleString()}
          {state.reason ? ` — ${state.reason}` : ""}
        </div>
      )}
    </div>
  )
}
