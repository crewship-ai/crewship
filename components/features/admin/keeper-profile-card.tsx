"use client"

// The judge PROFILE — the card that answers "how much does the judge get to work
// with, and who confirms what it decides?"
//
// Deliberately a sibling of KeeperJudgeCard rather than another section inside
// it. That card answers "what decides" (endpoint, model, budget); this one
// answers "under what rules". They are configured together and read separately,
// and the judge card is already 800 lines.
//
// Backed by the same endpoint, internal/api/admin_keeper_config.go:
//
//   GET /api/v1/admin/keeper/config → { judge_profile: { …per-field provenance } }
//   PUT /api/v1/admin/keeper/config ← partial update (ADMIN+/OWNER)
//
// Two capabilities the API accepts are NOT rendered here: precedent and
// consistency_samples. They are stored and validated and nothing implements
// them, so a switch would be a promise the product does not keep — and this card
// is exactly where an operator would believe it. The CLI marks them RESERVED for
// anyone who goes looking; the admin page says nothing rather than something
// false. When they land, they get rows here in the same change.

import { useCallback, useEffect, useState } from "react"

import { adminFetch } from "@/lib/admin-api"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { SaveFooter } from "@/components/ui/save-footer"
import { useDirtyForm } from "@/hooks/use-dirty-form"
import { useAbilities } from "@/hooks/use-abilities"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { cn } from "@/lib/utils"

type ConfigSource = "instance" | "env" | "profile" | "default"

interface ConfigField<T> {
  value: T
  source: ConfigSource
  editable: boolean
}

interface ProfileBlock {
  name: ConfigField<string>
  evidence: ConfigField<boolean>
  evidence_facts: ConfigField<string[]>
  hard_gate: ConfigField<boolean>
  escalate_from: ConfigField<number>
  prompt_budget_tokens: ConfigField<number>
  overridden: boolean
  choices: string[]
  available_facts: string[]
  stamp: string
}

/** Every field optional: an older server, or a proxy that mangled the body,
 *  must produce an inert card rather than take the admin page down. Somebody
 *  opens this page when something is already wrong. */
interface ConfigResponse {
  judge_profile?: Partial<ProfileBlock>
}

/** HumanApprovalNever — keeper.HumanApprovalNever. There is no L5; 5 is the
 *  sentinel for "no tier is escalated on the model's behalf". */
const NEVER = 5

/** keepercfg.MinPromptBudgetTokens / MaxPromptBudgetTokens. 0 is the separate
 *  "no cap" sentinel and is always allowed. */
const MIN_BUDGET = 512
const MAX_BUDGET = 131072

function ProvenanceChip({ source }: { source: ConfigSource }) {
  const label =
    source === "instance" ? "instance override"
      : source === "profile" ? "from the preset"
        : source === "env" ? "from server config"
          : "built-in default"
  return (
    <span
      className={cn(
        "inline-flex items-center h-[15px] px-1.5 rounded text-[9px] font-medium uppercase tracking-wide border",
        source === "instance"
          ? "text-primary/90 border-primary/30 bg-primary/[0.08]"
          : "text-muted-foreground border-border/60 bg-muted/30",
      )}
    >
      {label}
    </span>
  )
}

function WithProvenance({ source, children }: { source?: ConfigSource; children: React.ReactNode }) {
  return (
    <span className="block">
      <span className="block">{children}</span>
      <span className="mt-1 inline-flex"><ProvenanceChip source={source ?? "default"} /></span>
    </span>
  )
}

async function errorFrom(res: Response, fallback: string): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string; detail?: string }
    return body.error ?? body.detail ?? fallback
  } catch {
    return fallback
  }
}

export function KeeperProfileCard({ workspaceId }: { workspaceId?: string | null }) {
  const [p, setP] = useState<Partial<ProfileBlock> | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const { abilities } = useAbilities()
  const canEdit = abilities.can("manage", "Workspace")

  const form = useDirtyForm({
    name: p?.name?.value ?? "lean",
    evidence: p?.evidence?.value ?? true,
    hardGate: p?.hard_gate?.value ?? true,
    escalateFrom: String(p?.escalate_from?.value ?? 0),
    budget: String(p?.prompt_budget_tokens?.value ?? 0),
  })

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!workspaceId) return
    try {
      const res = await adminFetch("/api/v1/admin/keeper/config", workspaceId, { signal })
      if (signal?.aborted) return
      if (!res.ok) {
        setErr(await errorFrom(res, `Couldn't load the judge profile (HTTP ${res.status}).`))
        return
      }
      const body = (await res.json()) as ConfigResponse
      setP(body.judge_profile ?? null)
      setErr(null)
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") return
      setErr("Couldn't load the judge profile.")
    }
  }, [workspaceId])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  function handleSave() {
    if (!workspaceId) return
    void form.submit(async (draft) => {
      // PARTIAL. Only what the operator actually moved goes on the wire —
      // sending the whole block would clobber whatever the CLI, or another
      // admin, had set on a field nobody touched here.
      const body: Record<string, unknown> = {}
      if (draft.name !== (p?.name?.value ?? "lean")) body.judge_profile = draft.name
      if (draft.evidence !== (p?.evidence?.value ?? true)) {
        body.judge_evidence = draft.evidence ? "on" : "off"
      }
      if (draft.hardGate !== (p?.hard_gate?.value ?? true)) {
        body.judge_hard_gate = draft.hardGate ? "on" : "off"
      }
      if (Number(draft.escalateFrom) !== (p?.escalate_from?.value ?? 0)) {
        body.judge_escalate_from = Number(draft.escalateFrom)
      }
      if (Number(draft.budget) !== (p?.prompt_budget_tokens?.value ?? 0)) {
        body.judge_prompt_budget_tokens = Number(draft.budget)
      }

      const res = await adminFetch("/api/v1/admin/keeper/config", workspaceId, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) throw new Error(await errorFrom(res, `Failed to save (HTTP ${res.status})`))
      const next = (await res.json()) as ConfigResponse
      setP(next.judge_profile ?? null)
    })
  }

  if (err) {
    return (
      <SettingsCard title="Judge profile" description="What the judge is allowed to use when it decides.">
        <div className="px-4 py-3 text-xs text-destructive">{err}</div>
      </SettingsCard>
    )
  }

  if (!p) {
    return (
      <SettingsCard title="Judge profile" description="What the judge is allowed to use when it decides.">
        <div className="px-4 py-3"><Skeleton className="h-16 w-full" /></div>
      </SettingsCard>
    )
  }

  // The server's rule, held here so the operator learns it from the field rather
  // than from a round trip. Stripping the minus sign was not enough: "-1" became
  // "1", which is refused for the same reason — every non-zero value below the
  // floor is rejected. Mirrors keepercfg.validateProfile.
  const budgetNum = Number(form.draft.budget)
  const budgetInvalid =
    form.draft.budget.trim() !== "" &&
    (!Number.isFinite(budgetNum) ||
      !Number.isInteger(budgetNum) ||
      (budgetNum !== 0 && (budgetNum < MIN_BUDGET || budgetNum > MAX_BUDGET)))

  const autonomy = Number(form.draft.escalateFrom) === NEVER
  const facts = p.evidence_facts?.value ?? []
  const allFacts = p.available_facts ?? []

  return (
    <SettingsCard
      title="Judge profile"
      description="What the judge is allowed to use when it decides, and who confirms the result. Applies to the next credential request — no restart."
    >
      <SettingsRow
        label="Preset"
        description={
          <WithProvenance source={p.name?.source}>
            Sets the switches below together. Bigger presets cost context, and a small model
            given too much decides worse rather than better — pick the one that matches your judge.
          </WithProvenance>
        }
      >
        <Select
          value={form.draft.name}
          onValueChange={(v) => form.set("name", v)}
          disabled={!canEdit}
        >
          <SelectTrigger className="h-8 w-[150px] text-xs"><SelectValue /></SelectTrigger>
          <SelectContent>
            {(p.choices ?? ["lean", "standard", "thorough"]).map((c) => (
              <SelectItem key={c} value={c} className="text-xs">{c}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </SettingsRow>

      <SettingsRow
        label="Computed facts"
        description={
          <WithProvenance source={p.evidence?.source}>
            Facts the database already knows — is this credential bound to this agent, how many
            prior requests, how many denials in the last 7 days, is there open assigned work —
            placed above the conversation so they outrank what the history claims.
            {allFacts.length > 0 && (
              <span className="block mt-1 text-[11px] text-muted-foreground">
                {facts.length === allFacts.length
                  ? `All ${allFacts.length} facts.`
                  : `${facts.length} of ${allFacts.length} facts.`}{" "}
                Narrow the selection with <code className="text-[10px]">crewship keeper profile set --evidence-facts</code>.
              </span>
            )}
          </WithProvenance>
        }
      >
        <Switch
          checked={form.draft.evidence}
          onCheckedChange={(v) => form.set("evidence", v)}
          disabled={!canEdit}
          aria-label="Computed facts"
          data-testid="keeper-profile-evidence"
        />
      </SettingsRow>

      <SettingsRow
        label="Refuse an unbound credential"
        description={
          <WithProvenance source={p.hard_gate?.source}>
            An agent with no binding to an L3/L4 credential is refused without calling the model
            at all. Independent of the facts block above: shrinking the prompt does not stop the
            refusals.
          </WithProvenance>
        }
      >
        <Switch
          checked={form.draft.hardGate}
          onCheckedChange={(v) => form.set("hardGate", v)}
          disabled={!canEdit}
          aria-label="Refuse an unbound credential"
          data-testid="keeper-profile-hard-gate"
        />
      </SettingsRow>

      <SettingsRow
        label="Human approval from"
        description={
          <WithProvenance source={p.escalate_from?.source}>
            A judge ALLOW at this tier and above becomes an escalation, so a person confirms it.
            The default puts a human on L4 only.
          </WithProvenance>
        }
      >
        <Select
          value={form.draft.escalateFrom}
          onValueChange={(v) => form.set("escalateFrom", v)}
          disabled={!canEdit}
        >
          <SelectTrigger className="h-8 w-[190px] text-xs"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="0" className="text-xs">Tier default (L4 only)</SelectItem>
            <SelectItem value="1" className="text-xs">L1 and above</SelectItem>
            <SelectItem value="2" className="text-xs">L2 and above</SelectItem>
            <SelectItem value="3" className="text-xs">L3 and above</SelectItem>
            <SelectItem value="4" className="text-xs">L4 and above</SelectItem>
            <SelectItem value={String(NEVER)} className="text-xs">Never — full autonomy</SelectItem>
          </SelectContent>
        </Select>
      </SettingsRow>

      {autonomy && (
        // Loud because it is the only setting on this page that takes a person
        // out of a production credential. It is a legitimate choice on your own
        // instance — but it should never be one somebody arrives at without
        // noticing, and the two things it does NOT do are the ones an operator
        // is most likely to assume it does.
        <div
          data-testid="autonomy-warning"
          className="mx-4 mb-3 rounded border border-destructive/40 bg-destructive/[0.06] px-3 py-2 text-[11px] leading-relaxed text-destructive"
        >
          <strong>Every tier is granted without a person</strong>, including L4 production
          credentials. An agent that satisfies the judge gets them with nobody in the loop.
          <span className="block mt-1 text-destructive/80">
            This does not turn the Keeper off: a DENY is still a DENY, the per-tier intent
            minimums still refuse thin justifications before the model is asked, the unbound-credential
            refusal still applies, and every decision is still on the audit trail. What it removes is
            the human confirmation step.
          </span>
        </div>
      )}

      <SettingsRow
        label="Prompt budget"
        description={
          <WithProvenance source={p.prompt_budget_tokens?.source}>
            Caps the assembled prompt. The conversation is what gets trimmed; the watch policy,
            the tier and the request itself never do — without a cap the model server truncates
            from the front and drops the rules the judge was meant to apply.
            <span className="block mt-1 text-[11px] text-muted-foreground">
              0 means no cap. Set it to your judge&apos;s context window minus the reply.
            </span>
          </WithProvenance>
        }
      >
        <Input
          type="number"
          min={0}
          value={form.draft.budget}
          onChange={(e) => form.set("budget", e.target.value)}
          disabled={!canEdit}
          className="h-8 w-[110px] text-xs"
          aria-label="Prompt budget"
          data-testid="keeper-profile-budget"
        />
      </SettingsRow>

      {budgetInvalid && (
        <div
          data-testid="budget-invalid"
          role="status"
          className="px-4 py-2 text-xs text-destructive border-b border-border/40"
        >
          The prompt budget must be a whole number between {MIN_BUDGET} and{" "}
          {MAX_BUDGET} tokens, or 0 for no cap.
        </div>
      )}

      {p.stamp && (
        <div className="px-4 py-2.5 border-t border-border/60 text-[11px] text-muted-foreground">
          Recorded on each decision as{" "}
          <code className="text-[10px] bg-muted/50 rounded px-1 py-0.5">{p.stamp}</code>
          <span className="block mt-1">
            Two decisions taken under different profiles are not comparable, so the audit trail
            carries the one in force at the time.
          </span>
        </div>
      )}

      {canEdit && (
        <SaveFooter
          dirty={form.isDirty}
          status={form.status}
          error={form.error}
          canSave={!budgetInvalid}
          onSave={handleSave}
          onCancel={form.reset}
        />
      )}
    </SettingsCard>
  )
}
