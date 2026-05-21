"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { Loader2, Sparkles } from "lucide-react"
import { Switch } from "@/components/ui/switch"
import { useAbilities } from "@/hooks/use-abilities"

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
}

export function AgentLearningToggle({ agentId, workspaceId, canEdit }: AgentLearningToggleProps) {
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

  const load = useCallback(async () => {
    setLoading(true)
    setErr(null)
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/learning`, {
        headers: { "X-Workspace-ID": workspaceId },
      })
      if (!res.ok) {
        setErr(`Failed to load (HTTP ${res.status})`)
        return
      }
      const body = (await res.json()) as LearningResponse
      setState(body)
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to load")
    } finally {
      setLoading(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => {
    void load()
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
      const res = await fetch(`/api/v1/agents/${agentId}/learning`, {
        method: "PATCH",
        headers: {
          "Content-Type": "application/json",
          "X-Workspace-ID": workspaceId,
        },
        body: JSON.stringify({ enabled: pendingEnabled, reason: reason.trim() }),
      })
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
      <div className="rounded-xl border border-white/8 bg-card p-4 flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-3.5 w-3.5 animate-spin" /> Loading…
      </div>
    )
  }
  if (err) {
    return <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-4 text-sm text-red-300">{err}</div>
  }

  return (
    <div className="rounded-xl border border-white/8 bg-card p-4 space-y-3">
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-start gap-2.5">
          <Sparkles className={target ? "h-4 w-4 text-emerald-400 mt-0.5" : "h-4 w-4 text-muted-foreground mt-0.5"} />
          <div>
            <div className="text-sm font-medium">Self-improving mode</div>
            <div className="text-xs text-muted-foreground mt-0.5 max-w-xl">
              When ON, keeper evaluator ALLOW decisions auto-apply (recommended skills flip
              to active, captured lessons land in <code className="text-[10px]">lessons.md</code>).
              When OFF, every proposal queues a blocking inbox item for operator approval.
              DENY + ESCALATE always gate through inbox regardless. Still subordinate to
              this crew&rsquo;s autonomy level.
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
          <input
            id={`agent-learning-reason-${agentId}`}
            type="text"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={pendingEnabled ? "why grant autonomy to this agent?" : "why revoke autonomy?"}
            className="w-full rounded border border-white/10 bg-background px-2 py-1.5 text-sm focus:outline-none focus:border-primary/50"
            disabled={saving}
          />
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => { void save() }}
              disabled={reason.trim() === "" || saving}
              className="text-xs px-3 py-1.5 rounded border bg-primary/20 border-primary/40 text-primary hover:bg-primary/30 disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {saving ? "Saving…" : "Confirm"}
            </button>
            <button
              type="button"
              onClick={() => {
                setPendingEnabled(null)
                setReason("")
              }}
              disabled={saving}
              className="text-xs px-3 py-1.5 rounded border border-white/10 hover:bg-white/5"
            >
              Cancel
            </button>
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
