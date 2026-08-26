"use client"

// Issue #1001 M0 — Keeper watchdog governance, per workspace.
//
// Backed by internal/api/keeper_governance.go:
//
//   GET /api/v1/admin/keeper/governance  → the whole row                (ADMIN+)
//   PUT /api/v1/admin/keeper/governance  ← a PARTIAL update       (OWNER/ADMIN)
//
// Four cards, one subject each, each committing only its own fields. It used to
// be one card with twelve heterogeneous rows and a single Save in the header,
// which had two costs: an operator scanned a wall of unrelated controls looking
// for the one they came for, and fixing a typo in the risk threshold resent the
// watch spec, the governance model and the lease TTL along with it. The PUT is
// partial precisely so a card can send its own fields and nothing else.
//
// Cards with typed-in values keep every control in one draft behind one
// SaveFooter — including their switches. A switch that commits on the spot next
// to an input that needs Save makes one card commit two different ways
// depending on which control you touched, which is the inconsistency the
// settings pass (#1526) set out to remove.
//
// "No row" semantics: the behavioral watchdog is opt-in and default OFF per
// workspace — configured=false means it has never been enabled here, so the
// switch shows off. The server engine flag (serverEnabled) is shown only as
// context; it governs the credential-access gatekeeper, not this switch.
//
// The security contact must be an OWNER/ADMIN workspace member (the backend
// rejects anything else with a 400), so the picker is filtered to those roles
// from GET /workspaces/{id}/members. Empty contact = legacy fanout to everyone
// with the MANAGER role.

import React, { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Checkbox } from "@/components/ui/checkbox"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { SaveFooter } from "@/components/ui/save-footer"
import { useDirtyForm } from "@/hooks/use-dirty-form"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { useAbilities } from "@/hooks/use-abilities"
import { useCredentials } from "@/components/features/mcp/hooks/use-credentials"
import { apiFetch } from "@/lib/api-fetch"
import { adminFetch } from "@/lib/admin-api"
import { cn } from "@/lib/utils"

interface GovernanceResponse {
  configured: boolean
  enabled: boolean
  security_contact_user_id: string
  deny_notify_min_risk: number
  watch_spec?: string
  watch_presets?: string[]
  // Four-eyes credential gate (#1084): when true, an escalation raised by an
  // agent hired by user A cannot be resolved by user A — a different approver
  // is required. Backed by keeper_governance_settings.require_second_approver.
  require_second_approver?: boolean
  // The four-eyes rule as ENFORCED (#1559). The toggle above is only half of
  // it: keeper.TierPolicy.SecondApprover forces the rule on the top tier
  // whatever the toggle says, so a card that renders the toggle alone tells an
  // operator the opposite of what the resolve endpoint will do.
  //
  // tier_floor_label is the half that does not depend on the toggle — what is
  // still in force with it off — which is the only version of this answer the
  // card can use, because it has to render for a draft the server hasn't seen.
  // Absent from a server older than #1559: then the card says nothing rather
  // than name a tier from a constant of its own.
  effective_second_approver?: {
    min_security_level: number
    min_security_level_label?: string
    source: string
    tier_floor_security_level?: number
    tier_floor_label?: string
  }
  // Credential-lease auto-issuance TTL in seconds (#1373). 0 = off (grants stay
  // standing). A positive value makes a Keeper ALLOW / escalation approve
  // re-issue an L3/L4 grant as a lease of that length. Server accepts 0 or
  // [60, 2592000] and 400s anything else rather than clamping.
  auto_lease_seconds?: number
  // How often the watchdog reviews a tool call (#1001 M3): one in every N per
  // crew. 0 is the "never configured" sentinel and means the built-in default
  // (SAMPLE_EVERY_DEFAULT) — it does NOT mean "never". The server accepts
  // [1, 100] and 400s anything else, 0 included: there is no cadence that means
  // off, because that is what the enable switch is for.
  behavior_sample_every?: number
  // Non-blocking advisory returned by PUT — e.g. enabling the four-eyes rule on
  // a workspace that lacks a second eligible approver. Empty on GET.
  warning?: string
  // Governance-model override (backed by keeper_governance.go). All optional:
  // an empty provider means "use the server default model". When provider is
  // non-empty the server requires gov_model_id; credential is always optional.
  gov_model_provider?: string
  gov_model_id?: string
  gov_model_credential_id?: string
}

// GOV_MODEL_PROVIDERS mirrors the provider values accepted by
// keeper_governance.go / the CLI: "" (server default), "ollama", "anthropic",
// "openai_compat". Radix Select forbids value="" on items, so the "server
// default" option uses a sentinel that maps back to the empty wire value.
const GOV_PROVIDER_DEFAULT = "__server_default__"
const GOV_CREDENTIAL_NONE = "__none__"

const GOV_MODEL_PROVIDERS: {
  value: string
  label: string
  modelHint?: string
  /** Uppercase provider for GET /api/v1/models, when a picker can be offered. */
  catalogue?: string
  /** What choosing this costs and needs — the two things the label cannot say. */
  note?: string
}[] = [
  {
    value: "",
    label: "Use the instance judge",
    note: "Whatever the Credential access judge above is set to. Nothing extra to configure.",
  },
  {
    value: "ollama",
    label: "A different local model",
    modelHint: "qwen2.5:3b-instruct",
    catalogue: "OLLAMA",
    note: "Costs nothing per decision. Needs an ENDPOINT_URL credential if the model server is not the one the instance judge uses.",
  },
  {
    value: "anthropic",
    label: "Anthropic (Claude)",
    modelHint: "claude-haiku-4-5",
    catalogue: "ANTHROPIC",
    note: "Bills per decision against the API key you pick below. Sharper than a small local model on ambiguous intents.",
  },
  {
    value: "openai_compat",
    label: "OpenAI-compatible endpoint",
    modelHint: "gpt-4o-mini",
    catalogue: "OPENAI",
    note: "Any endpoint that speaks the OpenAI chat API. Needs an ENDPOINT_URL credential; add an API_KEY one if it authenticates.",
  },
]

// Credential types usable as a governance-model credential: an API key
// (anthropic / openai_compat) or an endpoint URL (a remote ollama / compat host).
const GOV_CREDENTIAL_TYPES = new Set(["API_KEY", "ENDPOINT_URL"])

/** One stage of the hosted-judge check — mirrors internal/api/admin_keeper_judge.go. */
interface GovTestStage {
  name: string
  label: string
  ok: boolean
  skipped?: boolean
  detail: string
  latency_ms?: number
}

interface GovTestResult {
  ok?: boolean
  stages: GovTestStage[]
  decision?: string
}

/**
 * Three states, not two. A skipped stage is not a passing one: "we did not get
 * far enough to ask" and "we asked and it worked" are the two answers an operator
 * most needs to tell apart, and a green tick for the first is how a check ends up
 * lying — which is exactly what the local judge check did before it learned to
 * compare its own latency against the credential path's budget.
 */
function GovStageRow({ stage }: { stage: GovTestStage }) {
  const mark = stage.ok ? "✓" : stage.skipped ? "–" : "✗"
  const colour = stage.ok
    ? "text-success"
    : stage.skipped
      ? "text-muted-foreground/60"
      : "text-destructive"
  return (
    <div className="flex items-start gap-2 px-4 py-1.5 text-xs">
      <span className={`shrink-0 font-mono ${colour}`} aria-hidden="true">{mark}</span>
      <span className="w-[13rem] shrink-0 text-foreground/80">{stage.label}</span>
      <span className={`min-w-0 flex-1 leading-snug ${stage.ok ? "text-muted-foreground" : colour}`}>
        {stage.detail}
      </span>
      {stage.latency_ms ? (
        <span className="shrink-0 tabular-nums text-muted-foreground/60">{stage.latency_ms}ms</span>
      ) : null}
    </div>
  )
}

// Mirrors governance.MaxWatchSpecLen (the server + CLI cap on the free-form spec).
const WATCH_SPEC_MAX_LEN = 4096

// WATCH_PRESETS mirrors internal/keeper/governance/presets.go — the five stable
// preset keys. The Go source is the authority for the wording actually injected
// into the evaluator prompts; these captions are UI summaries. Keep the key set
// in sync by hand (five stable keys; changing them is a product decision).
const WATCH_PRESETS: { key: string; label: string; caption: string }[] = [
  { key: "credentials", label: "Credential access", caption: "Disproportionate or bulk secret access, unjustified high-security reads." },
  { key: "egress", label: "Network egress", caption: "Exfiltration-shaped outbound: non-allowlisted hosts, piping secrets out." },
  { key: "memory", label: "Memory tampering", caption: "Overwriting facts, mass deletes, or planting misleading memory entries." },
  { key: "destructive", label: "Destructive ops", caption: "rm -rf, DROP/TRUNCATE without WHERE, force-push, wholesale overwrites." },
  { key: "secret_files", label: "Sensitive files", caption: "Reads of ~/.ssh, id_rsa, .env, cloud credential files, private keys." },
]

interface WorkspaceMember {
  id: string
  user_id: string
  role: string
  user?: {
    id: string
    email: string
    full_name: string | null
    avatar_url: string | null
  } | null
}

// Auto-lease bounds, mirroring governance.MinAutoLeaseSeconds /
// MaxAutoLeaseSeconds. The server rejects out-of-range values rather than
// clamping them, so validating here is what turns a bare 400 into an actionable
// message — it is not the gate.
const AUTO_LEASE_MIN_SECONDS = 60
const AUTO_LEASE_MAX_SECONDS = 30 * 24 * 60 * 60

// Sampling cadence bounds, mirroring governance.MinBehaviorSampleEvery /
// MaxBehaviorSampleEvery / DefaultBehaviorSampleEvery. Same deal as above: the
// server rejects rather than clamps, so this is the actionable-message layer.
//
// Neither bound is arbitrary. 1 is allowed — "review every tool call" is a real
// posture — but 0 is not, because a cadence of 0 stops the monitor while the
// switch above still says it is on. And past 100 the per-crew counter (in
// memory, reset each boot) would never reach the threshold inside a run.
const SAMPLE_EVERY_MIN = 1
const SAMPLE_EVERY_MAX = 100
const SAMPLE_EVERY_DEFAULT = 5
// Mirrors governance.WarnBehaviorSampleEveryBelow — the cadence under which the
// server also returns a non-blocking cost advisory. Said inline here too, so it
// arrives while the operator is choosing rather than after they have saved.
const SAMPLE_EVERY_WARN_BELOW = 3

// secondsToLeaseMinutes renders the wire value for the minutes input. 0 /
// undefined (auto-lease off) becomes "" so the field reads as empty rather than
// as a meaningful zero.
function secondsToLeaseMinutes(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return ""
  return String(Math.round(seconds / 60))
}

// leaseMinutesToSeconds parses the input back to wire seconds. "" (cleared) is a
// deliberate "off", so it maps to 0 rather than to an error. Returns null for a
// value that isn't a whole non-negative number of minutes, so the caller can
// explain the problem instead of sending NaN.
function leaseMinutesToSeconds(minutes: string): number | null {
  const trimmed = minutes.trim()
  if (trimmed === "") return 0
  const n = Number(trimmed)
  if (!Number.isInteger(n) || n < 0) return null
  return n * 60
}

// Presets live in a draft as a sorted, comma-joined key list rather than an
// array. useDirtyForm compares fields with Object.is, so an array would read as
// dirty the moment a checkbox was touched and stay dirty after being toggled
// back — and the wire order is not meaningful anyway.
function presetsToKey(keys: string[]): string {
  return [...keys].sort().join(",")
}

function keyToPresets(key: string): string[] {
  return key === "" ? [] : key.split(",")
}

// Radix Select forbids value="" on items, so the "everyone" option uses a
// sentinel that maps to the backend's empty security_contact_user_id.
const MANAGER_FANOUT = "__managers__"

export interface KeeperGovernancePanelProps {
  workspaceId: string | null | undefined
  /** Server-level keeper engine flag (GET /system/keeper) — shown as context
   *  only; the per-workspace watchdog toggle is independent (opt-in). */
  serverEnabled: boolean
  /**
   * Which half of the panel to render.
   *
   * The workspace judge override belongs next to the instance judge it
   * overrides — the two are one question ("what decides about credentials")
   * asked at two scopes, and putting them at opposite ends of the page is what
   * made an operator conclude there was no way to choose a model or a key. The
   * rest of the panel (watchdog, findings routing, leases) is a different
   * subject and stays below.
   *
   * Splitting by prop rather than by component keeps ONE fetch, one `put`, and
   * one error path for a single governance row — two components would each load
   * it and could disagree about what it says.
   */
  section?: "judge" | "policy"
}

/** Shape shared by every card: commit a partial governance update. */
type PutGovernance = (body: Record<string, unknown>) => Promise<GovernanceResponse>

export const KeeperGovernancePanel = React.memo(function KeeperGovernancePanel({
  workspaceId,
  serverEnabled,
  section = "policy",
}: KeeperGovernancePanelProps) {
  // Mirrors AgentLearningToggle: derive edit rights from CASL. The PUT is
  // roleManage (OWNER/ADMIN) server-side; only those roles get "manage" on
  // Workspace, so this lines up exactly. Server stays authoritative — the
  // greyed-out UI is a UX hint, not a security boundary.
  const { abilities } = useAbilities()
  const canEdit = useMemo(() => abilities.can("manage", "Workspace"), [abilities])

  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)
  const [gov, setGov] = useState<GovernanceResponse | null>(null)
  const [admins, setAdmins] = useState<WorkspaceMember[]>([])

  // Governance-model credential picker. Reuses the MCP credentials hook; we
  // only surface API_KEY / ENDPOINT_URL creds (the two usable as a model cred).
  const { credentials } = useCredentials(workspaceId ?? undefined)
  const govCredentials = useMemo(
    () => credentials.filter((c) => GOV_CREDENTIAL_TYPES.has(c.type)),
    [credentials],
  )

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!workspaceId) {
      setLoading(false)
      return
    }
    setLoading(true)
    setErr(null)
    try {
      const [govRes, membersRes] = await Promise.all([
        adminFetch("/api/v1/admin/keeper/governance", workspaceId,
          { signal },
        ),
        apiFetch(
          `/api/v1/workspaces/${workspaceId}/members?workspace_id=${encodeURIComponent(workspaceId)}`,
          { signal },
        ),
      ])
      if (signal?.aborted) return
      if (!govRes.ok) {
        setErr(`Failed to load governance settings (HTTP ${govRes.status})`)
        return
      }
      const body = (await govRes.json()) as GovernanceResponse
      if (signal?.aborted) return

      // A members failure only degrades the picker; governance still renders.
      if (membersRes.ok) {
        const members = (await membersRes.json()) as WorkspaceMember[]
        if (signal?.aborted) return
        setAdmins(
          (Array.isArray(members) ? members : []).filter(
            (m) => m.role === "OWNER" || m.role === "ADMIN",
          ),
        )
      } else {
        setAdmins([])
      }
      setGov(body)
    } catch (e) {
      // Aborts are expected when workspaceId changes mid-flight.
      if (e instanceof DOMException && e.name === "AbortError") return
      setErr(e instanceof Error ? e.message : "Failed to load governance settings")
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  // One writer for every card. Throws on failure so each card's SaveFooter
  // shows the server's message and keeps the draft — a failed write must never
  // silently discard what someone typed into a security control.
  const put = useCallback<PutGovernance>(async (body) => {
    if (!workspaceId) throw new Error("No workspace selected")
    const res = await adminFetch("/api/v1/admin/keeper/governance", workspaceId,
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      },
    )
    if (!res.ok) {
      let msg = `HTTP ${res.status}`
      try {
        const e = (await res.json()) as { error?: string; detail?: string }
        msg = e.error ?? e.detail ?? msg
      } catch {
        /* keep the status fallback */
      }
      throw new Error(msg)
    }
    const next = (await res.json()) as GovernanceResponse
    setGov(next)
    // A non-blocking advisory (e.g. four-eyes enabled without a second
    // eligible approver) is a warning toast, not an error — the save succeeded.
    if (next.warning) toast.warning(next.warning)
    return next
  }, [workspaceId])

  if (!workspaceId) return null

  if (loading) {
    return <Skeleton className="h-[180px] rounded-xl" data-testid="keeper-governance-loading" />
  }

  if (err || !gov) {
    return (
      <SettingsCard
        title="Watchdog"
        description="Workspace-level watchdog controls"
      >
        <div className="px-4 py-3 flex items-center justify-between gap-3">
          <span className="text-xs text-destructive/90">{err ?? "Failed to load governance settings"}</span>
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => { void load() }}
          >
            Retry
          </Button>
        </div>
      </SettingsCard>
    )
  }

  if (section === "judge") {
    return <GovernanceModelCard gov={gov} credentials={govCredentials} canEdit={canEdit} put={put} workspaceId={workspaceId} />
  }

  return (
    <>
      <WatchdogCard gov={gov} serverEnabled={serverEnabled} canEdit={canEdit} put={put} />
      <FindingsRoutingCard gov={gov} admins={admins} canEdit={canEdit} put={put} workspaceId={workspaceId} />
      <CredentialLeasesCard gov={gov} canEdit={canEdit} put={put} />
    </>
  )
})

// ── Watchdog: does it run, and what does it look for ────────────────────────

function WatchdogCard({
  gov, serverEnabled, canEdit, put,
}: {
  gov: GovernanceResponse
  serverEnabled: boolean
  canEdit: boolean
  put: PutGovernance
}) {
  const form = useDirtyForm({
    enabled: gov.enabled,
    presets: presetsToKey(gov.watch_presets ?? []),
    spec: gov.watch_spec ?? "",
    // Kept as a string so the number input can be cleared and retyped without
    // snapping mid-edit. Empty renders the sentinel as the default it means.
    sampleEvery: gov.behavior_sample_every ? String(gov.behavior_sample_every) : String(SAMPLE_EVERY_DEFAULT),
  })

  const sampleEveryNum = Number(form.draft.sampleEvery)
  const sampleEveryProblem =
    !Number.isInteger(sampleEveryNum) || form.draft.sampleEvery.trim() === ""
      ? "Must be a whole number."
      : sampleEveryNum === 0
        ? "0 would leave the watchdog on but never looking. Use the switch above to turn it off."
        : sampleEveryNum < SAMPLE_EVERY_MIN || sampleEveryNum > SAMPLE_EVERY_MAX
          ? `Must be from ${SAMPLE_EVERY_MIN} to ${SAMPLE_EVERY_MAX}.`
          : null

  function handleSave() {
    void form.submit(async (draft) => {
      await put({
        enabled: draft.enabled,
        watch_presets: keyToPresets(draft.presets),
        watch_spec: draft.spec,
        behavior_sample_every: Number(draft.sampleEvery),
      })
    })
  }

  const presetKeys = keyToPresets(form.draft.presets)

  return (
    <SettingsCard
      title="Watch what agents do"
      description="Separate from credential access. This samples the tool calls agents make — files read, commands run, hosts contacted — and raises a finding when something looks wrong. It never blocks a credential; the judge above does that."
    >
      <SettingsRow
        label="Turn it on"
        description={
          serverEnabled
            ? "Off by default. When on, a share of tool calls is reviewed and anything suspicious lands in the inbox."
            : "Off by default. The Keeper engine above is off too, so nothing is reviewed until both are on."
        }
      >
        <Switch
          checked={form.draft.enabled}
          onCheckedChange={(checked) => form.set("enabled", checked)}
          disabled={!canEdit}
          data-testid="keeper-governance-switch"
          aria-label="Toggle watchdog enabled"
        />
      </SettingsRow>

      {/* Everything below is what it looks for — shown only once it is ON.
          Asking an operator to choose rules for a monitor that is not running is
          a configuration screen for a thing that does not exist yet, and it is
          most of what made this page feel like work. */}
      {form.draft.enabled && (
      <>
      {/* How often it looks (#1001 M3). Every review is a call to the governance
          model, so this is the cost dial as much as the coverage dial — and the
          number was previously unreachable: the monitor ran on a hardwired 5 for
          every workspace.

          There is deliberately no "off" value here — the switch above is the off
          switch. A cadence that silenced the monitor while that switch still
          read on is the one thing this control must not be able to express. */}
      <SettingsRow
        label="Review one in every"
        description="Tool calls between reviews, counted per crew. Lower catches more and calls the governance model more often — 1 reviews everything. Higher is cheaper and quieter."
      >
        <span className="flex flex-col items-end gap-1">
          <span className="flex items-center gap-1.5">
            <Input
              type="number"
              min={SAMPLE_EVERY_MIN}
              max={SAMPLE_EVERY_MAX}
              step={1}
              inputMode="numeric"
              value={form.draft.sampleEvery}
              onChange={(e) => form.set("sampleEvery", e.target.value)}
              disabled={!canEdit}
              className="h-8 w-16 text-xs text-right tabular-nums"
              aria-label={`Review one in every N tool calls (${SAMPLE_EVERY_MIN}-${SAMPLE_EVERY_MAX})`}
              aria-invalid={sampleEveryProblem !== null}
              data-testid="keeper-governance-sample-every"
            />
            <span className="text-xs text-muted-foreground">tool calls</span>
          </span>
          {sampleEveryProblem ? (
            <span
              className="text-xs text-destructive/90 text-right max-w-[15rem]"
              data-testid="keeper-governance-sample-every-invalid"
            >
              {sampleEveryProblem}
            </span>
          ) : sampleEveryNum < SAMPLE_EVERY_WARN_BELOW ? (
            /* Allowed, and expensive. Said here rather than refused, because
               "review everything" is a legitimate posture — it is just one that
               puts a model round-trip behind most of what agents do. */
            <span
              className="text-xs text-muted-foreground/80 text-right max-w-[15rem] leading-snug"
              data-testid="keeper-governance-sample-every-cost"
            >
              Most tool calls will carry a governance-model round-trip. On a
              hosted judge that bills per review.
            </span>
          ) : null}
        </span>
      </SettingsRow>

      {/* A full-width block rather than a SettingsRow: a five-way multi-select
          does not belong in a right-aligned control slot. */}
      <div className="px-4 py-2.5 border-b border-border/40">
        <div className="text-xs text-foreground">What to flag</div>
        <div className="text-xs text-muted-foreground/80 mt-0.5 leading-snug">
          Pick the ones that matter here. These are added to the checks it always does.
        </div>
        <div className="mt-2 grid gap-2 sm:grid-cols-2">
          {WATCH_PRESETS.map((p) => {
            const on = presetKeys.includes(p.key)
            return (
              <label
                key={p.key}
                className="flex items-start gap-2 cursor-pointer"
                htmlFor={`keeper-watch-preset-${p.key}`}
              >
                <Checkbox
                  id={`keeper-watch-preset-${p.key}`}
                  checked={on}
                  onCheckedChange={(checked) =>
                    form.set(
                      "presets",
                      presetsToKey(
                        checked === true
                          ? [...presetKeys.filter((k) => k !== p.key), p.key]
                          : presetKeys.filter((k) => k !== p.key),
                      ),
                    )
                  }
                  disabled={!canEdit}
                  className="mt-0.5"
                  data-testid={`keeper-watch-preset-${p.key}`}
                />
                <span className="min-w-0">
                  <span className="text-xs text-foreground">{p.label}</span>
                  <span className="block text-xs text-muted-foreground/80 leading-snug">
                    {p.caption}
                  </span>
                </span>
              </label>
            )
          })}
        </div>
      </div>

      {/* Free-form rules — natural language, injected as authoritative policy. */}
      <div className="px-4 py-2.5">
        <div className="text-xs text-foreground">Anything else to flag</div>
        <div className="text-xs text-muted-foreground/80 mt-0.5 leading-snug">
          One rule per line, in your own words. Optional — leave it empty and the checks above still apply.
        </div>
        <Textarea
          value={form.draft.spec}
          onChange={(e) => form.set("spec", e.target.value)}
          disabled={!canEdit}
          rows={4}
          // Mirror the server/CLI cap (governance.MaxWatchSpecLen) client-side so
          // an over-long paste is refused before the round-trip, not lost to a 400.
          maxLength={WATCH_SPEC_MAX_LEN}
          placeholder={"flag any read of ~/.ssh or id_rsa\nflag credential access outside 08:00–18:00"}
          className="mt-2 text-xs font-mono"
          aria-label="Custom watch rules"
          data-testid="keeper-watch-spec"
        />
      </div>
      </>
      )}

      {canEdit && (
        <SaveFooter
          dirty={form.isDirty}
          status={form.status}
          error={form.error}
          canSave={sampleEveryProblem === null}
          onSave={handleSave}
          onCancel={form.reset}
          testId="keeper-watchdog-save"
        />
      )}
    </SettingsCard>
  )
}

// ── Findings & routing: who hears about a finding, and when ─────────────────

// keeperFindingsRecipient / keeperFindingsTestResult mirror
// internal/api/admin_keeper_findings.go.
interface FindingsRecipient {
  user_id: string
  email?: string
  name?: string
  role?: string
  reason: string
}

interface FindingsTestResult {
  recipients: FindingsRecipient[]
  warning?: string
}

function FindingsRoutingCard({
  gov, admins, canEdit, put, workspaceId,
}: {
  gov: GovernanceResponse
  admins: WorkspaceMember[]
  canEdit: boolean
  put: PutGovernance
  workspaceId: string
}) {
  // Routing check (POST /admin/keeper/findings/test). Separate from the card's
  // draft on purpose: it is an action, not a setting, and it must run against
  // what is SAVED — testing an unsaved contact would answer a question nobody
  // asked.
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<FindingsTestResult | null>(null)

  const sendTestFinding = async () => {
    if (testing) return
    setTesting(true)
    try {
      const res = await adminFetch("/api/v1/admin/keeper/findings/test", workspaceId,
        { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" },
      )
      if (!res.ok) {
        let msg = `HTTP ${res.status}`
        try {
          const e = (await res.json()) as { error?: string; detail?: string }
          msg = e.error ?? e.detail ?? msg
        } catch {
          /* keep the status fallback */
        }
        toast.error(`Test finding failed: ${msg}`)
        return
      }
      const body = (await res.json()) as FindingsTestResult
      setTestResult(body)
      if (body.recipients.length === 0) {
        toast.error("The test finding reached nobody — see the card for why.")
      } else {
        toast.success(`Test finding sent — check your inbox (${body.recipients.length} recipient${body.recipients.length === 1 ? "" : "s"}).`)
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Test finding failed")
    } finally {
      setTesting(false)
    }
  }

  const form = useDirtyForm({
    contact: gov.security_contact_user_id ?? "",
    // Kept as a string so the number input can be cleared and retyped without
    // snapping to a value mid-edit.
    risk: String(gov.deny_notify_min_risk ?? 7),
    secondApprover: gov.require_second_approver ?? false,
  })

  const riskNum = Number(form.draft.risk)
  const riskValid = Number.isInteger(riskNum) && riskNum >= 1 && riskNum <= 10

  // Server-reported, never a constant here: the tier table lives in
  // internal/keeper/tier.go and a copy of "L4" in the console is a copy that
  // goes stale silently.
  const tierFloorLabel = gov.effective_second_approver?.tier_floor_label ?? ""

  function handleSave() {
    void form.submit(async (draft) => {
      await put({
        security_contact_user_id: draft.contact,
        deny_notify_min_risk: Number(draft.risk),
        require_second_approver: draft.secondApprover,
      })
    })
  }

  // Keep the current contact selectable even if that member was demoted or
  // removed since it was saved — otherwise the Select renders blank and a
  // save would silently rewrite the contact.
  const contactInList =
    form.draft.contact === "" || admins.some((m) => m.user_id === form.draft.contact)

  return (
    <SettingsCard
      title="Findings &amp; routing"
      description="Who a finding reaches, and the threshold at which a DENY is worth someone's attention."
      actions={
        canEdit ? (
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => { void sendTestFinding() }}
            disabled={testing}
            data-testid="keeper-findings-test"
          >
            {testing ? "Sending…" : "Send test finding"}
          </Button>
        ) : undefined
      }
    >
      <SettingsRow
        label="Security contact"
        description="Findings target this person's inbox in realtime."
      >
        <Select
          value={form.draft.contact === "" ? MANAGER_FANOUT : form.draft.contact}
          onValueChange={(v) => form.set("contact", v === MANAGER_FANOUT ? "" : v)}
          disabled={!canEdit}
        >
          <SelectTrigger
            className="h-8 text-xs w-[220px]"
            aria-label="Security contact"
            data-testid="keeper-governance-contact"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={MANAGER_FANOUT} className="text-xs">
              Everyone with MANAGER role
            </SelectItem>
            {admins.map((m) => (
              <SelectItem key={m.user_id} value={m.user_id} className="text-xs">
                {m.user?.full_name || m.user?.email || m.user_id}
              </SelectItem>
            ))}
            {!contactInList && (
              <SelectItem value={form.draft.contact} className="text-xs">
                {form.draft.contact} (no longer OWNER/ADMIN)
              </SelectItem>
            )}
          </SelectContent>
        </Select>
      </SettingsRow>

      <SettingsRow
        label="Notify on DENY at risk ≥"
        description="ESCALATE decisions always notify; this additionally surfaces high-risk DENYs."
      >
        <span className="flex flex-col items-end gap-1">
          <Input
            type="number"
            min={1}
            max={10}
            step={1}
            inputMode="numeric"
            value={form.draft.risk}
            onChange={(e) => form.set("risk", e.target.value)}
            disabled={!canEdit}
            className="h-8 w-16 text-xs text-right tabular-nums"
            aria-label="DENY notification risk threshold (1-10)"
            aria-invalid={!riskValid}
            data-testid="keeper-governance-risk"
          />
          {!riskValid && (
            <span className="text-xs text-destructive/90" data-testid="keeper-governance-risk-invalid">
              Must be a whole number from 1 to 10.
            </span>
          )}
        </span>
      </SettingsRow>

      {/* Four-eyes credential gate (#1084). When on, an escalation raised by an
          agent hired by user A must be resolved by a DIFFERENT approver. The
          server warns (not blocks) if the workspace lacks a second eligible
          approver; that advisory arrives as a toast on save.

          Off is not "nobody needs a second approver" (#1559): the credential's
          tier forces the rule on its own at the floor the server reports, and
          it can only tighten this switch, never loosen it. Said here, once,
          while the switch is off — with it on the row's own description
          already says a second approver is required. */}
      <SettingsRow
        label="Require a second approver"
        description={
          <>
            Four-eyes: credential escalations can&rsquo;t be approved by the same person who
            owns the requesting agent. Needs ≥2 OWNER/ADMIN/MANAGER members.
            {!form.draft.secondApprover && tierFloorLabel && (
              <span className="block mt-1" data-testid="keeper-second-approver-tier-note">
                Off here, but {tierFloorLabel} credentials still require one. A credential&rsquo;s
                tier can only tighten this rule, never loosen it.
              </span>
            )}
          </>
        }
        border={false}
      >
        <Switch
          checked={form.draft.secondApprover}
          onCheckedChange={(checked) => form.set("secondApprover", checked)}
          disabled={!canEdit}
          data-testid="keeper-governance-second-approver"
          aria-label="Toggle require a second approver"
        />
      </SettingsRow>

      {testResult && (
        <div
          className="px-4 py-2.5 border-t border-border/40 bg-muted/20"
          data-testid="keeper-findings-test-result"
        >
          <div className="text-xs text-foreground/80">
            {testResult.recipients.length === 0
              ? "That finding reached nobody."
              : `A finding reaches ${testResult.recipients.length} ${testResult.recipients.length === 1 ? "person" : "people"}:`}
          </div>
          {testResult.warning && (
            <div className="text-xs text-destructive/90 mt-1 leading-snug">{testResult.warning}</div>
          )}
          <ul className="mt-1.5 space-y-1">
            {testResult.recipients.map((r) => (
              <li key={r.user_id} className="text-xs text-muted-foreground flex flex-wrap gap-x-2">
                <span className="text-foreground/80">{r.email || r.name || r.user_id}</span>
                {r.role && <span className="font-mono">{r.role}</span>}
                <span className="text-muted-foreground/70">{r.reason}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      {canEdit && (
        <SaveFooter
          dirty={form.isDirty}
          status={form.status}
          error={form.error}
          canSave={riskValid}
          onSave={handleSave}
          onCancel={form.reset}
          testId="keeper-findings-save"
        />
      )}
    </SettingsCard>
  )
}

// ── Credential leases: how long an approval stays good for ──────────────────

function CredentialLeasesCard({
  gov, canEdit, put,
}: {
  gov: GovernanceResponse
  canEdit: boolean
  put: PutGovernance
}) {
  // Held in MINUTES because that is the unit an operator thinks in, while the
  // wire uses seconds. A TTL the CLI set to a non-minute-aligned value (90s)
  // renders rounded — which is safe now that this card is the only thing that
  // ever sends auto_lease_seconds: an unrelated save cannot rewrite it, because
  // an unrelated save no longer carries the field at all.
  const form = useDirtyForm({ minutes: secondsToLeaseMinutes(gov.auto_lease_seconds) })

  const seconds = leaseMinutesToSeconds(form.draft.minutes)
  const problem =
    seconds === null
      ? "Must be a whole number of minutes (0 or empty turns it off)."
      : seconds > 0 && seconds < AUTO_LEASE_MIN_SECONDS
        ? "At least 1 minute — a shorter lease can lapse inside Keeper's own evaluation."
        : seconds > AUTO_LEASE_MAX_SECONDS
          ? "At most 30 days (43200 minutes)."
          : null

  function handleSave() {
    void form.submit(async (draft) => {
      const s = leaseMinutesToSeconds(draft.minutes)
      // Unreachable while canSave gates the footer; kept because sending null
      // would reach the server as `null` and 400 with something less useful.
      if (s === null) throw new Error("Auto-lease must be a whole number of minutes")
      await put({ auto_lease_seconds: s })
    })
  }

  return (
    <SettingsCard
      title="Credential leases"
      description="Whether a Keeper approval grants access indefinitely or for a while."
    >
      {/* #1373. Empty/0 = off, the default: grants stay standing and nothing
          changes. A value makes every Keeper ALLOW (and every approved
          agent-proposed credential) re-issue the L3/L4 grant as a lease of that
          length, refreshed on each approval. Never shortens a longer hand-set
          --ttl lease. */}
      <SettingsRow
        label="Auto-issue credential leases"
        description="Minutes an L3/L4 credential grant stays valid after each Keeper approval. Empty or 0 keeps grants standing (default). Min 1 minute, max 43200 (30 days). L1/L2 self-service keys are never leased."
        border={false}
      >
        <span className="flex flex-col items-end gap-1">
          <span className="flex items-center gap-1.5">
            <Input
              type="number"
              min={0}
              max={AUTO_LEASE_MAX_SECONDS / 60}
              step={1}
              inputMode="numeric"
              placeholder="off"
              value={form.draft.minutes}
              onChange={(e) => form.set("minutes", e.target.value)}
              disabled={!canEdit}
              className="h-8 w-20 text-xs text-right tabular-nums"
              aria-label="Credential auto-lease TTL in minutes (0 or empty to disable)"
              aria-invalid={problem !== null}
              data-testid="keeper-governance-auto-lease"
            />
            <span className="text-xs text-muted-foreground">min</span>
          </span>
          {problem && (
            <span className="text-xs text-destructive/90 text-right max-w-[15rem]" data-testid="keeper-governance-auto-lease-invalid">
              {problem}
            </span>
          )}
        </span>
      </SettingsRow>

      {canEdit && (
        <SaveFooter
          dirty={form.isDirty}
          status={form.status}
          error={form.error}
          canSave={problem === null}
          onSave={handleSave}
          onCancel={form.reset}
          testId="keeper-leases-save"
        />
      )}
    </SettingsCard>
  )
}

// ── Workspace governance model: this workspace's own judge ──────────────────

function GovernanceModelCard({
  gov, credentials, canEdit, put, workspaceId,
}: {
  gov: GovernanceResponse
  credentials: { id: string; name: string; type: string }[]
  canEdit: boolean
  put: PutGovernance
  /** Scopes the model catalogue lookup, which lists live from this workspace's
   *  credential for the provider when there is one. */
  workspaceId: string
}) {
  const form = useDirtyForm({
    provider: gov.gov_model_provider ?? "",
    modelId: gov.gov_model_id ?? "",
    credentialId: gov.gov_model_credential_id ?? "",
  })

  // A non-empty provider REQUIRES a model id (the server 400s otherwise); block
  // save and surface the requirement client-side.
  const modelMissing = form.draft.provider !== "" && form.draft.modelId.trim() === ""
  const chosen = GOV_MODEL_PROVIDERS.find((p) => p.value === form.draft.provider)

  // Verification, for the same reason the local judge has it: this card asks the
  // operator to pick one of several stored API keys, and picking the wrong or
  // exhausted one produced no feedback at all until the next real credential
  // request denied. A 401 and a 429 are both one-click fixes here and were both
  // invisible.
  const [testing, setTesting] = React.useState(false)
  const [testResult, setTestResult] = React.useState<GovTestResult | null>(null)

  // The model list for the selected provider, from the endpoint the rest of the
  // app uses (live from a workspace credential, curated fallback otherwise). It
  // replaces typing a tag from memory, which for a fail-closed judge means every
  // credential request denying on a typo.
  const [catalogue, setCatalogue] = React.useState<string[]>([])
  const catalogueFor = chosen?.catalogue
  React.useEffect(() => {
    if (!catalogueFor) { setCatalogue([]); return }
    const controller = new AbortController()
    void (async () => {
      try {
        const qs = new URLSearchParams({ provider: catalogueFor })
        if (workspaceId) qs.set("workspace_id", workspaceId)
        const res = await apiFetch(`/api/v1/models?${qs.toString()}`, { signal: controller.signal })
        // Every write is guarded on the signal. Without it a superseded request
        // — the provider was changed twice in a second — could land after the
        // newer one and clear a catalogue that had just been filled correctly.
        if (controller.signal.aborted) return
        if (!res.ok) { setCatalogue([]); return }
        const body = await res.json()
        if (controller.signal.aborted) return
        setCatalogue(((body?.models ?? []) as { id: string }[]).map((m) => m.id).slice(0, 10))
      } catch (e) {
        if (e instanceof DOMException && e.name === "AbortError") return
        // Otherwise silent: a missing picker degrades to the free-text field,
        // which still works. An error banner here would bury the field it is about.
        setCatalogue([])
      }
    })()
    return () => controller.abort()
  }, [catalogueFor, workspaceId])

  async function handleTest() {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await adminFetch("/api/v1/admin/keeper/judge/test-hosted", workspaceId, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          provider: form.draft.provider,
          model: form.draft.modelId.trim(),
          credential_id: form.draft.credentialId,
        }),
      })
      const body = await res.json().catch(() => ({}))
      if (!res.ok) {
        setTestResult({ ok: false, stages: [{ name: "error", label: "Test", ok: false, detail: body?.error || `HTTP ${res.status}` }] })
        return
      }
      setTestResult(body as GovTestResult)
    } catch (e) {
      setTestResult({ ok: false, stages: [{ name: "error", label: "Test", ok: false, detail: e instanceof Error ? e.message : "Network error" }] })
    } finally {
      setTesting(false)
    }
  }

  function handleSave() {
    void form.submit(async (draft) => {
      await put({
        gov_model_provider: draft.provider,
        // Trimmed to "" when the provider is server-default so we never send a
        // stale model id alongside an empty provider.
        gov_model_id: draft.provider === "" ? "" : draft.modelId.trim(),
        // Same guard for the credential: the server rejects a credential with
        // no provider (400), so drop a stale one when the provider resets.
        gov_model_credential_id: draft.provider === "" ? "" : draft.credentialId,
      })
    })
  }

  return (
    <SettingsCard
      title="Judge for this workspace"
      description="Which model decides credential access here — including a hosted one, on an API key you have stored. Overrides the instance judge for this workspace only, and governs the Reviews evaluators too. Resolved per request; no restart."
      actions={
        canEdit && form.draft.provider !== "" && !modelMissing ? (
          <Button
            variant="soft"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => { void handleTest() }}
            disabled={testing}
            data-testid="keeper-gov-test"
          >
            {testing ? "Testing…" : "Test"}
          </Button>
        ) : undefined
      }
    >
      {/* The other half of the #1558 scope explanation. An operator who wants a
          hosted judge starts on the instance card above, because that is the one
          titled "Credential access judge" — and the only thing that used to send
          them here was the server refusing the write. Say it on both cards, in
          the same words, before either mistake is possible. */}
      <div
        className="px-4 py-2 border-b border-border/40 text-xs leading-snug text-muted-foreground"
        data-testid="keeper-gov-scope"
      >
        <strong className="font-medium text-foreground/80">Scope:</strong> this workspace only. It overrides{" "}
        <strong className="font-medium text-foreground/80">Credential access judge</strong> above, which is
        instance-wide and speaks native Ollama only — so this card is the only place the credential judge can be
        Anthropic or OpenAI-compatible, sourcing its endpoint or API key from this workspace&apos;s vault.
        If that credential is later revoked, decisions fall back to the instance judge rather than failing.
      </div>

      <SettingsRow
        label="What decides"
        description={chosen?.note ?? "Leave on the instance judge unless this workspace needs its own."}
      >
        <Select
          value={form.draft.provider === "" ? GOV_PROVIDER_DEFAULT : form.draft.provider}
          onValueChange={(v) => form.set("provider", v === GOV_PROVIDER_DEFAULT ? "" : v)}
          disabled={!canEdit}
        >
          <SelectTrigger
            className="h-8 text-xs w-[220px]"
            aria-label="Governance model provider"
            data-testid="keeper-gov-provider"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {GOV_MODEL_PROVIDERS.map((p) => (
              <SelectItem
                key={p.value || GOV_PROVIDER_DEFAULT}
                value={p.value === "" ? GOV_PROVIDER_DEFAULT : p.value}
                className="text-xs"
              >
                {p.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </SettingsRow>

      {form.draft.provider !== "" && (
        <>
          <SettingsRow
            label="Model"
            description="Required. Pick one the provider serves — typing a tag from memory is how a judge ends up denying every request."
          >
            <span className="flex flex-col items-end gap-1.5">
              <Input
                type="text"
                value={form.draft.modelId}
                onChange={(e) => form.set("modelId", e.target.value)}
                disabled={!canEdit}
                placeholder={chosen?.modelHint}
                className="h-8 w-[240px] text-xs font-mono"
                aria-label="Governance model id"
                aria-required="true"
                aria-invalid={modelMissing}
                data-testid="keeper-gov-model-id"
              />
              {/* The catalogue the rest of the app already uses, so this picker
                  and the crew-canvas one cannot drift into offering different
                  Claude ids. Free text stays available for a model that is not
                  in the list yet. */}
              {catalogue.length > 0 && (
                <span className="flex flex-col items-end gap-1" data-testid="keeper-gov-models">
                  <span className="text-xs text-muted-foreground/70">click to use</span>
                  <span className="flex flex-wrap justify-end gap-1 max-w-[22rem]">
                    {catalogue.map((m) => (
                      <button
                        key={m}
                        type="button"
                        onClick={() => form.set("modelId", m)}
                        disabled={!canEdit}
                        className={cn(
                          "h-[19px] rounded border px-1.5 font-mono text-[10px] transition-colors",
                          m === form.draft.modelId.trim()
                            ? "border-primary/50 bg-primary/[0.12] text-primary/90"
                            : "border-border/60 bg-muted/30 text-muted-foreground hover:border-border hover:text-foreground",
                        )}
                      >
                        {m}
                      </button>
                    ))}
                  </span>
                </span>
              )}
              {modelMissing && (
                <span className="text-xs text-destructive/90" data-testid="keeper-gov-model-required">
                  A model is required for this provider.
                </span>
              )}
            </span>
          </SettingsRow>

          <SettingsRow
            label="Key"
            description="Which stored credential authenticates it. Several Anthropic keys is the normal case on an orchestration platform — each carries its own subscription limit — so this picks by NAME, and Test tells you whether the one you picked still answers."
            border={false}
          >
            <Select
              value={form.draft.credentialId === "" ? GOV_CREDENTIAL_NONE : form.draft.credentialId}
              onValueChange={(v) => form.set("credentialId", v === GOV_CREDENTIAL_NONE ? "" : v)}
              disabled={!canEdit}
            >
              <SelectTrigger
                className="h-8 text-xs w-[220px]"
                aria-label="Governance model credential"
                data-testid="keeper-gov-credential"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={GOV_CREDENTIAL_NONE} className="text-xs">
                  — none —
                </SelectItem>
                {credentials.map((c) => (
                  <SelectItem key={c.id} value={c.id} className="text-xs">
                    {c.name} ({c.type})
                  </SelectItem>
                ))}
                {/* Keep a saved-but-now-unlisted credential selectable so the
                    Select never renders blank and silently drops it on save. */}
                {form.draft.credentialId !== "" &&
                  !credentials.some((c) => c.id === form.draft.credentialId) && (
                    <SelectItem value={form.draft.credentialId} className="text-xs">
                      {form.draft.credentialId} (unavailable)
                    </SelectItem>
                  )}
              </SelectContent>
            </Select>
          </SettingsRow>
        </>
      )}

      {testResult && (
        <div
          className="border-t border-border/40 bg-muted/[0.15] py-1"
          role="status"
          data-testid="keeper-gov-test-result"
        >
          {testResult.stages.map((st) => (
            <GovStageRow key={st.name} stage={st} />
          ))}
          {testResult.ok && (
            <p className="px-4 py-1.5 text-xs text-success">
              This judge works. Save to put it in force for this workspace.
            </p>
          )}
        </div>
      )}

      {canEdit && (
        <SaveFooter
          dirty={form.isDirty}
          status={form.status}
          error={form.error}
          canSave={!modelMissing}
          onSave={handleSave}
          onCancel={form.reset}
          testId="keeper-gov-model-save"
        />
      )}
    </SettingsCard>
  )
}
