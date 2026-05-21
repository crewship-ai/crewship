"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { AlertTriangle, Loader2 } from "lucide-react"
import { useAbilities } from "@/hooks/use-abilities"
import { cn } from "@/lib/utils"

// PR-G F2/F4.2 UI surface — per-crew policy controls.
//
// Renders the autonomy_level + behavior_mode pair as a single panel
// because the (full × block) combination is forbidden server-side
// (see internal/policy/types.go Validate). The UI mirrors that rule
// inline: when autonomy=full is picked, the block option is greyed
// out with a tooltip explaining why; the operator can't even submit
// the invalid combination.
//
// Backend contract:
//   GET  /api/v1/crews/{crewId}/policy  → { autonomy_level, behavior_mode, set_by_user_id?, set_at?, reason? }
//   PUT  /api/v1/crews/{crewId}/policy  ← { autonomy_level, behavior_mode, reason }   (ADMIN+)
//
// Read is workspace-member; write is ADMIN+ (server enforces). If
// the operator lacks the role the PUT returns 403 and we surface a
// toast — we don't pre-hide the controls because role isn't always
// known on the client and a stale role would fail noisily anyway.

type AutonomyLevel = "strict" | "guided" | "trusted" | "full"
type BehaviorMode = "warn" | "block"

interface PolicyResponse {
  autonomy_level: AutonomyLevel
  behavior_mode: BehaviorMode
  set_by_user_id?: string | null
  set_at?: string | null
  reason?: string | null
}

const AUTONOMY_LEVELS: ReadonlyArray<{
  value: AutonomyLevel
  label: string
  description: string
}> = [
  {
    value: "strict",
    label: "Strict",
    description: "Every governable action needs operator Approve. Compliance-grade.",
  },
  {
    value: "guided",
    label: "Guided",
    description: "Read-only auto, writes need OK. Default for new crews.",
  },
  {
    value: "trusted",
    label: "Trusted",
    description: "Most actions auto; writes log to inbox for after-the-fact review.",
  },
  {
    value: "full",
    label: "Full",
    description: "Autonomous; journal-only. For power-team workflows.",
  },
]

const BEHAVIOR_MODES: ReadonlyArray<{
  value: BehaviorMode
  label: string
  description: string
}> = [
  {
    value: "warn",
    label: "Warn",
    description: "Anti-pattern hits land as non-blocking inbox notes; agent proceeds.",
  },
  {
    value: "block",
    label: "Block",
    description: "Anti-pattern hits interrupt the agent + inbox approval gate.",
  },
]

export interface CrewPolicyControlsProps {
  crewId: string
  workspaceId: string
  /**
   * Read-only roles still see the values + audit trail; only ADMIN+
   * can change them. The component doesn't hard-block writes
   * client-side (server is authoritative) but greys out the controls
   * to set expectations.
   */
  canEdit?: boolean
}

// MAX_EPHEMERAL_AGENTS_CEILING mirrors the server-side cap in
// crews_update.go (also enforced as SQL CHECK >= 0). Keeping the
// constant local so a future bump only requires touching one place
// per surface.
const MAX_EPHEMERAL_AGENTS_CEILING = 100

export function CrewPolicyControls({ crewId, workspaceId, canEdit }: CrewPolicyControlsProps) {
  // Server-side authority on RBAC; UI just mirrors so non-admins see
  // controls disabled rather than failing only at save time with 403.
  // If the caller explicitly passes canEdit, honor it (lets parents
  // override in admin overlays); otherwise derive from CASL abilities.
  const { abilities } = useAbilities()
  const effectiveCanEdit = useMemo(() => {
    if (typeof canEdit === "boolean") return canEdit
    return abilities.can("update", "Crew")
  }, [canEdit, abilities])
  const [policy, setPolicy] = useState<PolicyResponse | null>(null)
  // max_ephemeral_agents lives on the crew row (not the policy table),
  // so we fetch it separately from GET /crews/{id} and PATCH it via
  // the generic crew PATCH endpoint. Kept on this panel rather than
  // a third settings card because it's logically a "governance" knob —
  // it caps how many hires the policy.DecisionAutoHire branch can
  // create before the quota guard fires.
  const [maxEphemeral, setMaxEphemeral] = useState<number | null>(null)
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  // Pending state for "I clicked an option but haven't confirmed with a reason yet"
  const [pendingAutonomy, setPendingAutonomy] = useState<AutonomyLevel | null>(null)
  const [pendingBehavior, setPendingBehavior] = useState<BehaviorMode | null>(null)
  // Stored as a string so the input can hold a transient empty value
  // while the operator is mid-edit without being forced to 0.
  const [pendingMaxEphemeral, setPendingMaxEphemeral] = useState<string | null>(null)
  const [reason, setReason] = useState("")

  const load = useCallback(async () => {
    setLoading(true)
    setErr(null)
    try {
      // Parallel fetches — policy and crew metadata are independent;
      // a network error on one MUST NOT prevent the other from
      // rendering. Promise.all rejects both on first failure, which
      // would drop us into the global error path and hide a successful
      // policy load — switched to allSettled so each fetch is judged
      // on its own merit. Policy is the required surface (we error out
      // if it failed); quota silently defaults to 10 if its fetch
      // tripped, matching the server-side default.
      const [policyResult, crewResult] = await Promise.allSettled([
        fetch(`/api/v1/crews/${crewId}/policy`, {
          headers: { "X-Workspace-ID": workspaceId },
        }),
        fetch(`/api/v1/crews/${crewId}`, {
          headers: { "X-Workspace-ID": workspaceId },
        }),
      ])
      if (policyResult.status === "rejected") {
        setErr(policyResult.reason instanceof Error ? policyResult.reason.message : "Failed to load policy")
        return
      }
      if (!policyResult.value.ok) {
        setErr(`Failed to load policy (HTTP ${policyResult.value.status})`)
        return
      }
      const body = (await policyResult.value.json()) as PolicyResponse
      setPolicy(body)
      if (crewResult.status === "fulfilled" && crewResult.value.ok) {
        const crewBody = (await crewResult.value.json()) as { max_ephemeral_agents?: number }
        setMaxEphemeral(typeof crewBody.max_ephemeral_agents === "number" ? crewBody.max_ephemeral_agents : 10)
      } else {
        // Quota fetch failed (network or non-2xx); fall back to the
        // server-side default rather than blocking policy panel render.
        setMaxEphemeral(10)
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to load policy")
    } finally {
      setLoading(false)
    }
  }, [crewId, workspaceId])

  useEffect(() => {
    void load()
  }, [load])

  const targetAutonomy = pendingAutonomy ?? policy?.autonomy_level ?? "guided"
  const targetBehavior = pendingBehavior ?? policy?.behavior_mode ?? "warn"
  const forbiddenCombination = targetAutonomy === "full" && targetBehavior === "block"

  // Parse the pending quota value into a validated number (or null if
  // unchanged). Done here once so dirty/save/error messaging stay in sync.
  const parsedQuota = (() => {
    if (pendingMaxEphemeral === null) return { value: null as number | null, valid: true }
    const trimmed = pendingMaxEphemeral.trim()
    if (trimmed === "") return { value: null as number | null, valid: false }
    if (!/^\d+$/.test(trimmed)) return { value: null as number | null, valid: false }
    const n = parseInt(trimmed, 10)
    if (!Number.isFinite(n) || n < 0 || n > MAX_EPHEMERAL_AGENTS_CEILING) {
      return { value: null as number | null, valid: false }
    }
    return { value: n, valid: true }
  })()
  const quotaDirty = parsedQuota.value !== null && parsedQuota.value !== maxEphemeral
  const quotaInvalid = pendingMaxEphemeral !== null && !parsedQuota.valid

  const policyFieldDirty = (pendingAutonomy !== null && pendingAutonomy !== policy?.autonomy_level)
    || (pendingBehavior !== null && pendingBehavior !== policy?.behavior_mode)
  const dirty = policyFieldDirty || quotaDirty

  const save = useCallback(async () => {
    if (!policy || forbiddenCombination || quotaInvalid) return
    // Reason is required only when the policy table is being mutated
    // (audit trail lives there); a pure quota bump is a column edit on
    // the crew row and doesn't need an explainer per PRD §6 F2.
    if (policyFieldDirty && reason.trim() === "") {
      toast.error("Reason is required (audit trail)")
      return
    }
    setSaving(true)
    try {
      // Two writes when both surfaces are dirty: the policy table
      // owns autonomy + behavior_mode + reason (audit trail), while
      // max_ephemeral_agents is a column on the crew row. We do them
      // sequentially in POLICY-FIRST order: a failed policy update
      // (RBAC / validation) aborts the save without touching the quota
      // column, so the operator sees one clean failure with no partial
      // state. If quota then fails after policy succeeded, the toast
      // explicitly tells the operator which half landed — see the
      // catch branch at the bottom of this try block. Atomic
      // cross-table writes would need a server-side transaction; for
      // now this ordering keeps the dangerous failure mode (silent
      // partial governance change) off the table.
      if (policyFieldDirty) {
        const res = await fetch(`/api/v1/crews/${crewId}/policy`, {
          method: "PUT",
          headers: {
            "Content-Type": "application/json",
            "X-Workspace-ID": workspaceId,
          },
          body: JSON.stringify({
            autonomy_level: targetAutonomy,
            behavior_mode: targetBehavior,
            reason: reason.trim(),
          }),
        })
        if (!res.ok) {
          let msg = `HTTP ${res.status}`
          try {
            const err = (await res.json()) as { error?: string }
            if (err.error) msg = err.error
          } catch {
            /* keep status-only message */
          }
          toast.error(`Failed to update policy: ${msg}`)
          return
        }
        const body = (await res.json()) as PolicyResponse
        setPolicy(body)
        setPendingAutonomy(null)
        setPendingBehavior(null)
      }

      if (quotaDirty && parsedQuota.value !== null) {
        const qRes = await fetch(`/api/v1/crews/${crewId}`, {
          method: "PATCH",
          headers: {
            "Content-Type": "application/json",
            "X-Workspace-ID": workspaceId,
          },
          body: JSON.stringify({ max_ephemeral_agents: parsedQuota.value }),
        })
        if (!qRes.ok) {
          let msg = `HTTP ${qRes.status}`
          try {
            const errBody = (await qRes.json()) as { error?: string }
            if (errBody.error) msg = errBody.error
          } catch {
            /* keep status-only message */
          }
          if (policyFieldDirty) {
            toast.error(`Policy saved, but quota update failed: ${msg}. Quota left at previous value; re-try the quota change in isolation.`)
          } else {
            toast.error(`Failed to update quota: ${msg}`)
          }
          return
        }
        const crewBody = (await qRes.json()) as { max_ephemeral_agents?: number }
        if (typeof crewBody.max_ephemeral_agents === "number") {
          setMaxEphemeral(crewBody.max_ephemeral_agents)
        }
        setPendingMaxEphemeral(null)
      }

      setReason("")
      toast.success(
        policyFieldDirty && quotaDirty
          ? "Policy + quota updated"
          : policyFieldDirty
            ? "Policy updated"
            : "Quota updated",
      )
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to update policy")
    } finally {
      setSaving(false)
    }
  }, [crewId, workspaceId, policy, targetAutonomy, targetBehavior, reason, forbiddenCombination, quotaDirty, quotaInvalid, parsedQuota.value, policyFieldDirty])

  if (loading) {
    return (
      <div className="rounded-xl border border-white/8 bg-card p-4 flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-3.5 w-3.5 animate-spin" /> Loading policy…
      </div>
    )
  }
  if (err) {
    return (
      <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-4 text-sm text-red-300">
        {err}
      </div>
    )
  }

  return (
    <div className="rounded-xl border border-white/8 bg-card p-4 space-y-4">
      <div>
        <div className="text-xs uppercase tracking-wider text-muted-foreground mb-2">Autonomy level</div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          {AUTONOMY_LEVELS.map((opt) => (
            <button
              key={opt.value}
              type="button"
              disabled={!effectiveCanEdit || saving}
              onClick={() => setPendingAutonomy(opt.value)}
              className={cn(
                "text-left rounded-lg border px-3 py-2 transition-colors",
                targetAutonomy === opt.value
                  ? "border-primary/60 bg-primary/10"
                  : "border-white/10 hover:bg-white/5",
                (!effectiveCanEdit || saving) && "opacity-50 cursor-not-allowed",
              )}
              aria-pressed={targetAutonomy === opt.value}
              data-testid={`autonomy-${opt.value}`}
            >
              <div className="text-sm font-medium">{opt.label}</div>
              <div className="text-[11px] text-muted-foreground mt-0.5">{opt.description}</div>
            </button>
          ))}
        </div>
      </div>

      <div>
        <div className="text-xs uppercase tracking-wider text-muted-foreground mb-2">Behavior mode</div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          {BEHAVIOR_MODES.map((opt) => {
            const disabledByCombination = opt.value === "block" && targetAutonomy === "full"
            return (
              <button
                key={opt.value}
                type="button"
                disabled={!effectiveCanEdit || saving || disabledByCombination}
                onClick={() => setPendingBehavior(opt.value)}
                className={cn(
                  "text-left rounded-lg border px-3 py-2 transition-colors",
                  targetBehavior === opt.value && !disabledByCombination
                    ? "border-primary/60 bg-primary/10"
                    : "border-white/10 hover:bg-white/5",
                  (!effectiveCanEdit || saving || disabledByCombination) && "opacity-50 cursor-not-allowed",
                )}
                aria-pressed={targetBehavior === opt.value}
                title={disabledByCombination ? "block + full autonomy is contradictory and rejected server-side" : undefined}
                data-testid={`behavior-${opt.value}`}
              >
                <div className="text-sm font-medium">{opt.label}</div>
                <div className="text-[11px] text-muted-foreground mt-0.5">{opt.description}</div>
              </button>
            )
          })}
        </div>
      </div>

      {forbiddenCombination && (
        <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 flex items-start gap-2 text-xs text-amber-300">
          <AlertTriangle className="h-4 w-4 shrink-0 mt-0.5" />
          <div>
            <div className="font-medium">Forbidden combination</div>
            <div className="mt-0.5 text-amber-300/80">
              autonomy=full × behavior_mode=block is rejected server-side (opt-in trust × opt-in restriction).
              Pick one or the other.
            </div>
          </div>
        </div>
      )}

      {/* Ephemeral quota — lives on the crew row, not the policy table,
          but it caps the hire flow which is governance-adjacent. Placed
          below the policy controls so the cancel/save bar covers both. */}
      <div>
        <label
          htmlFor={`max-ephemeral-${crewId}`}
          className="block text-xs uppercase tracking-wider text-muted-foreground mb-2"
        >
          Ephemeral agent quota
        </label>
        <div className="flex items-center gap-3">
          <input
            id={`max-ephemeral-${crewId}`}
            type="number"
            inputMode="numeric"
            min={0}
            max={MAX_EPHEMERAL_AGENTS_CEILING}
            step={1}
            disabled={!effectiveCanEdit || saving || maxEphemeral === null}
            value={pendingMaxEphemeral ?? (maxEphemeral !== null ? String(maxEphemeral) : "")}
            onChange={(e) => setPendingMaxEphemeral(e.target.value)}
            className={cn(
              "w-24 rounded border bg-background px-2 py-1.5 text-sm focus:outline-none",
              quotaInvalid ? "border-red-500/50 focus:border-red-500/70" : "border-white/10 focus:border-primary/50",
              (!effectiveCanEdit || saving) && "opacity-50 cursor-not-allowed",
            )}
            aria-invalid={quotaInvalid}
            aria-describedby="ephemeral-quota-help"
            data-testid="max-ephemeral-agents-input"
          />
          <div id="ephemeral-quota-help" className="text-[11px] text-muted-foreground flex-1">
            Hard cap on concurrent ephemeral (hired) agents this crew can have. Ghosts don&rsquo;t count.
            <span className="block text-muted-foreground/60 mt-0.5">
              Integer 0-{MAX_EPHEMERAL_AGENTS_CEILING}; default 10.
            </span>
          </div>
        </div>
        {quotaInvalid && (
          <div className="mt-1.5 text-[11px] text-red-300">
            Must be a whole number between 0 and {MAX_EPHEMERAL_AGENTS_CEILING}.
          </div>
        )}
      </div>

      {(dirty || reason !== "") && (
        <div className="space-y-2 pt-2 border-t border-white/5">
          <label
            htmlFor={`crew-policy-reason-${crewId}`}
            className="block text-xs uppercase tracking-wider text-muted-foreground"
          >
            Reason {policyFieldDirty ? "(required, audit trail)" : "(optional — quota-only change)"}
          </label>
          <input
            id={`crew-policy-reason-${crewId}`}
            type="text"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="why are you changing this policy?"
            className="w-full rounded border border-white/10 bg-background px-2 py-1.5 text-sm focus:outline-none focus:border-primary/50"
            disabled={saving}
          />
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => { void save() }}
              disabled={!dirty || forbiddenCombination || quotaInvalid || (policyFieldDirty && reason.trim() === "") || saving}
              className={cn(
                "text-xs px-3 py-1.5 rounded border transition-colors",
                "bg-primary/20 border-primary/40 text-primary hover:bg-primary/30",
                "disabled:opacity-50 disabled:cursor-not-allowed",
              )}
            >
              {saving ? "Saving…" : "Save policy"}
            </button>
            <button
              type="button"
              onClick={() => {
                setPendingAutonomy(null)
                setPendingBehavior(null)
                setPendingMaxEphemeral(null)
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

      {policy?.set_at && (
        <div className="text-[11px] text-muted-foreground pt-2 border-t border-white/5">
          Last changed {new Date(policy.set_at).toLocaleString()}
          {policy.reason ? ` — ${policy.reason}` : ""}
        </div>
      )}
    </div>
  )
}
