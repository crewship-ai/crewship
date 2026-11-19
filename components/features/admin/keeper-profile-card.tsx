"use client"

// The decision rules: "how much does the judge get to work with, and when does
// a person confirm?"
//
// A sibling of KeeperJudgeCard rather than another section inside it. That
// card answers "what decides" (server, model, time budget); this one answers
// "under what rules". They are configured together and read separately.
//
// Backed by the same endpoint, internal/api/admin_keeper_config.go:
//
//   GET /api/v1/admin/keeper/config → { judge_profile: { …per-field provenance } }
//   PUT /api/v1/admin/keeper/config ← partial update (instance admin)
//
// The three switches (judge_evidence, judge_hard_gate, judge_precedent) go on
// the wire as true, false or null, and nothing else: triFromJSON answers 400
// to the strings "on"/"off" this card used to send.
//
// The facts list, precedent and "ask more than once" are stored and validated
// by the server. Nothing in the judge's prompt path reads precedent or the
// sample count yet (grep internal/keeper); the card sets them as the API
// defines them.

import { useCallback, useEffect, useState } from "react"
import { Gauge, KeyRound, Layers, ListChecks, Lock, Repeat, User } from "lucide-react"

import { adminFetch } from "@/lib/admin-api"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { Segmented } from "@/components/ui/segmented"
import { SaveFooter } from "@/components/ui/save-footer"
import { useDirtyForm } from "@/hooks/use-dirty-form"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { SettingsCard, SettingsRow, settingsControl } from "@/components/features/settings/shared"
import { CREDENTIAL_LEVELS, FACT_LABEL } from "@/components/features/admin/security/security-model"
import { OptionList, type Choice } from "@/components/features/admin/security/security-kit"
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
  precedent: ConfigField<boolean>
  precedent_n: ConfigField<number>
  consistency_samples: ConfigField<number>
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

/** HumanApprovalNever — keeper.HumanApprovalNever. There is no level 5; 5 is
 *  the sentinel for "no level is escalated on the model's behalf". */
const NEVER = 5

/** keepercfg.MinPromptBudgetTokens / MaxPromptBudgetTokens. 0 is the separate
 *  "no cap" sentinel and is always allowed. */
const MIN_BUDGET = 512
const MAX_BUDGET = 131072
/** keepercfg.MaxPrecedentExamples. */
const MAX_PRECEDENT = 10

/** The level, named for what it adds. The server's own words for the presets
 *  are lean / standard / thorough (internal/keepercfg/profile.go). */
const LEVELS: Choice<string>[] = [
  { value: "lean", title: "Lean", description: "Fastest. The request, the grant and the facts it computes." },
  { value: "standard", title: "Standard", description: "Adds earlier decisions as examples. For models of 9 to 14 B." },
  { value: "thorough", title: "Thorough", description: "Adds asking three times on level 3 and 4. Slowest, and the biggest prompt." },
]

/** "When a person confirms": the lowest level a judge ALLOW turns into a wait
 *  for a person. 0 is the level table's own answer, level 4 only. */
const CONFIRM_FROM: Choice<string>[] = [
  { value: "0", title: "Level 4 only", description: "The default: only critical secrets wait for a person." },
  { value: "3", title: "From level 3", description: "High and critical secrets wait for a person." },
  { value: "2", title: "From level 2", description: "A person confirms almost everything." },
  { value: "1", title: "From level 1", description: "A person confirms every request." },
  { value: String(NEVER), title: "Never", description: "Full autonomy: every level is granted without a person." },
]

function sourceNote(source?: ConfigSource): string {
  return source === "instance" ? "Set here"
    : source === "profile" ? "From the level"
      : source === "env" ? "From the server's settings"
        : "Built-in default"
}

function ProvenanceChip({ source }: { source: ConfigSource }) {
  return (
    <span
      className={cn(
        "inline-flex items-center h-[18px] px-1.5 rounded text-micro font-medium border",
        source === "instance"
          ? "text-primary/90 border-primary/30 bg-primary/[0.08]"
          : "text-muted-foreground border-border/60 bg-muted/30",
      )}
    >
      {sourceNote(source)}
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

// Facts live in a draft as a sorted, comma-joined key list, for the same reason
// the watch presets do: useDirtyForm compares with Object.is.
const factsKey = (facts: readonly string[]) => [...facts].sort().join(",")
const keyFacts = (key: string) => (key === "" ? [] : key.split(","))

export function KeeperProfileCard({ workspaceId }: { workspaceId?: string | null }) {
  const [p, setP] = useState<Partial<ProfileBlock> | null>(null)
  const [err, setErr] = useState<string | null>(null)
  // An instance-wide setting: the PUT is authedInstanceMut, which admits an
  // instance admin whatever their role in this workspace and refuses a
  // workspace ADMIN who is not one. The card follows the same rule (review
  // R2); the server stays the gate.
  const canEdit = useIsInstanceAdmin() === true

  const allFacts = p?.available_facts ?? []
  const savedSamples = p?.consistency_samples?.value ?? 1
  const form = useDirtyForm({
    name: p?.name?.value ?? "lean",
    evidence: p?.evidence?.value ?? true,
    facts: factsKey(p?.evidence_facts?.value ?? allFacts),
    hardGate: p?.hard_gate?.value ?? true,
    precedent: p?.precedent?.value ?? false,
    precedentN: String(p?.precedent_n?.value ?? 3),
    samples: String(savedSamples),
    // 4 and 0 are the same rule today (a person from level 4); show one.
    escalateFrom: String(p?.escalate_from?.value === 4 ? 0 : p?.escalate_from?.value ?? 0),
    budget: String(p?.prompt_budget_tokens?.value ?? 0),
  })

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!workspaceId) return
    try {
      const res = await adminFetch("/api/v1/admin/keeper/config", workspaceId, { signal })
      if (signal?.aborted) return
      if (!res.ok) {
        setErr(await errorFrom(res, `Couldn't load the decision rules (HTTP ${res.status}).`))
        return
      }
      const body = (await res.json()) as ConfigResponse
      setP(body.judge_profile ?? null)
      setErr(null)
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") return
      setErr("Couldn't load the decision rules.")
    }
  }, [workspaceId])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  // The server's rules, held here so the operator learns them from the field
  // rather than from a round trip. Mirrors keepercfg.validateProfile.
  const budgetNum = Number(form.draft.budget)
  const budgetInvalid =
    form.draft.budget.trim() !== "" &&
    (!Number.isFinite(budgetNum) ||
      !Number.isInteger(budgetNum) ||
      (budgetNum !== 0 && (budgetNum < MIN_BUDGET || budgetNum > MAX_BUDGET)))
  const precedentNum = Number(form.draft.precedentN)
  const precedentInvalid = form.draft.precedent && (!Number.isInteger(precedentNum) || precedentNum < 1 || precedentNum > MAX_PRECEDENT)
  const picked = keyFacts(form.draft.facts)
  // An empty list would mean "every fact" to the server: say so rather than send it.
  const factsInvalid = form.draft.evidence && picked.length === 0

  function handleSave() {
    if (!workspaceId) return
    void form.submit(async (draft) => {
      // PARTIAL. Only what the operator actually moved goes on the wire —
      // sending the whole block would clobber whatever the CLI, or another
      // admin, had set on a field nobody touched here.
      const body: Record<string, unknown> = {}
      if (draft.name !== (p?.name?.value ?? "lean")) body.judge_profile = draft.name
      // true / false only: "on" and "off" are a 400 (triFromJSON).
      if (draft.evidence !== (p?.evidence?.value ?? true)) body.judge_evidence = draft.evidence
      if (draft.facts !== factsKey(p?.evidence_facts?.value ?? allFacts)) {
        // Every fact picked is the server's own default: "" clears the list.
        body.judge_evidence_facts = keyFacts(draft.facts).length === allFacts.length ? "" : keyFacts(draft.facts).join(",")
      }
      if (draft.hardGate !== (p?.hard_gate?.value ?? true)) body.judge_hard_gate = draft.hardGate
      if (draft.precedent !== (p?.precedent?.value ?? false)) body.judge_precedent = draft.precedent
      if (draft.precedentN !== String(p?.precedent_n?.value ?? 3)) body.judge_precedent_n = Number(draft.precedentN)
      if (draft.samples !== String(savedSamples)) body.judge_consistency_samples = Number(draft.samples)
      if (draft.escalateFrom !== String(p?.escalate_from?.value === 4 ? 0 : p?.escalate_from?.value ?? 0)) {
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
      <SettingsCard title="How careful" description="What the judge is allowed to use when it decides.">
        <div className="px-4 py-3 text-xs text-destructive">{err}</div>
      </SettingsCard>
    )
  }

  if (!p) {
    return (
      <SettingsCard title="How careful" description="What the judge is allowed to use when it decides.">
        <div className="px-4 py-3"><Skeleton className="h-16 w-full" /></div>
      </SettingsCard>
    )
  }

  const autonomy = Number(form.draft.escalateFrom) === NEVER
  const sampleOptions = [...new Set([1, 3, 5, savedSamples])].sort((a, b) => a - b)
    .map((n) => ({ value: String(n), label: n === 1 ? "Once" : `${n} times` }))

  return (
    <>
      <SettingsCard icon={ListChecks} title="How careful" description="Start from a level, then adjust. Applies to the next credential request, without a restart.">
        <div className="px-4 pt-3 text-label text-muted-foreground">
          <WithProvenance source={p.name?.source}>The level sets the switches below together. A small model given too much decides worse, not better: pick the one that matches your judge.</WithProvenance>
        </div>
        <OptionList label="Level" value={form.draft.name} onChange={(v) => form.set("name", v)} disabled={!canEdit} className="pt-0"
          options={LEVELS.filter((l) => (p.choices ?? ["lean", "standard", "thorough"]).includes(l.value))} data-testid="keeper-profile-level" />

        <SettingsRow lead={Layers} label="Computed facts"
          description={<WithProvenance source={p.evidence?.source}>Facts the database already knows, placed above the conversation so they outrank what the history claims.</WithProvenance>}>
          <Switch checked={form.draft.evidence} onCheckedChange={(v) => form.set("evidence", v)} disabled={!canEdit}
            aria-label="Computed facts" data-testid="keeper-profile-evidence" />
        </SettingsRow>
        <div className="border-b border-border px-4 py-2.5" data-testid="keeper-profile-facts">
          <div className="text-control font-medium">Facts it may compute</div>
          <div className="mt-0.5 text-label text-muted-foreground">
            <WithProvenance source={p.evidence_facts?.source}>
              {picked.length === allFacts.length ? `All ${allFacts.length} facts.` : `${picked.length} of ${allFacts.length} facts.`}
            </WithProvenance>
          </div>
          <div role="group" aria-label="Facts it may compute" className="mt-2 flex flex-wrap gap-1.5">
            {allFacts.map((f) => {
              const on = picked.includes(f)
              return (
                <button key={f} type="button" role="checkbox" aria-checked={on} disabled={!canEdit || !form.draft.evidence}
                  onClick={() => form.set("facts", factsKey(on ? picked.filter((k) => k !== f) : [...picked, f]))}
                  data-testid={`keeper-profile-fact-${f}`}
                  className={cn("inline-flex h-8 items-center rounded-lg border px-2.5 text-control transition-colors disabled:opacity-50 coarse:min-h-[44px]",
                    on ? "border-primary bg-primary/10 text-foreground" : "border-border text-muted-foreground hover:text-foreground")}>
                  {FACT_LABEL[f] ?? f.replace(/_/g, " ")}
                </button>
              )
            })}
          </div>
          {factsInvalid && <p role="status" data-testid="facts-invalid" className="mt-2 text-label text-destructive">Pick at least one fact, or turn computed facts off.</p>}
        </div>

        <SettingsRow lead={Repeat} label="Compare with earlier decisions"
          description={<WithProvenance source={p.precedent?.source}>Shows the judge how alike requests were decided before.</WithProvenance>}>
          <Switch checked={form.draft.precedent} onCheckedChange={(v) => form.set("precedent", v)} disabled={!canEdit}
            aria-label="Compare with earlier decisions" data-testid="keeper-profile-precedent" />
          <Input type="number" min={1} max={MAX_PRECEDENT} step={1} inputMode="numeric" value={form.draft.precedentN}
            onChange={(e) => form.set("precedentN", e.target.value)} disabled={!canEdit || !form.draft.precedent}
            className={cn(settingsControl, "w-16 sm:w-16 text-right tabular-nums")} aria-label={`How many earlier decisions (1-${MAX_PRECEDENT})`}
            aria-invalid={precedentInvalid} data-testid="keeper-profile-precedent-n" />
          <span className="text-label text-muted-foreground">most similar</span>
        </SettingsRow>
        {precedentInvalid && <p role="status" data-testid="precedent-invalid" className="border-b border-border px-4 py-2 text-label text-destructive">Compare with a whole number from 1 to {MAX_PRECEDENT} earlier decisions.</p>}

        <SettingsRow lead={Layers} label="Ask more than once"
          description={<WithProvenance source={p.consistency_samples?.source}>The majority answer wins. Slower, and costs more.</WithProvenance>}>
          <Segmented ariaLabel="Ask more than once" value={form.draft.samples} onChange={(v) => form.set("samples", v)}
            options={sampleOptions.map((o) => ({ ...o, disabled: !canEdit }))} />
        </SettingsRow>

        <SettingsRow lead={Lock} label="Refuse a credential bound to nothing"
          description={<WithProvenance source={p.hard_gate?.source}>An agent with no binding to a level 3 or 4 credential is refused without asking the judge. Independent of the facts above.</WithProvenance>}>
          <Switch checked={form.draft.hardGate} onCheckedChange={(v) => form.set("hardGate", v)} disabled={!canEdit}
            aria-label="Refuse a credential bound to nothing" data-testid="keeper-profile-hard-gate" />
        </SettingsRow>

        <SettingsRow lead={Gauge} label="Prompt budget" border={false}
          description={<WithProvenance source={p.prompt_budget_tokens?.source}>Caps the assembled prompt; the conversation is what gets trimmed. 0 means no cap. Set it to your judge&apos;s context window minus the reply.</WithProvenance>}>
          <Input type="number" min={0} value={form.draft.budget} onChange={(e) => form.set("budget", e.target.value)} disabled={!canEdit}
            className={cn(settingsControl, "sm:w-28")} aria-label="Prompt budget" data-testid="keeper-profile-budget" />
          <span className="text-label text-muted-foreground">tokens</span>
        </SettingsRow>
        {budgetInvalid && (
          <div data-testid="budget-invalid" role="status" className="px-4 py-2 text-xs text-destructive border-t border-border">
            The prompt budget must be a whole number between {MIN_BUDGET} and {MAX_BUDGET} tokens, or 0 for no cap.
          </div>
        )}
        {p.stamp && (
          <div className="px-4 py-2.5 border-t border-border text-label text-muted-foreground">
            Recorded on each decision as <code className="text-micro bg-muted/50 rounded px-1 py-0.5">{p.stamp}</code>
            <span className="block mt-1">Two decisions taken under different rules are not comparable, so the audit trail carries the ones in force at the time.</span>
          </div>
        )}
      </SettingsCard>

      <SettingsCard icon={User} title="When a person confirms" description="Credentials are sorted into four levels of sensitivity.">
        <div className="px-4 pt-3 text-label text-muted-foreground"><WithProvenance source={p.escalate_from?.source}>A judge ALLOW at this level and above becomes a wait for a person.</WithProvenance></div>
        <OptionList label="When a person confirms" value={form.draft.escalateFrom} onChange={(v) => form.set("escalateFrom", v)} disabled={!canEdit}
          options={CONFIRM_FROM} data-testid="keeper-profile-escalate" />
        {autonomy && (
          // Loud because it is the only setting on this page that takes a person
          // out of a production credential. It is a legitimate choice on your own
          // instance, but nobody should arrive at it without noticing.
          <div data-testid="autonomy-warning"
            className="mx-4 mb-3 rounded border border-destructive/40 bg-destructive/[0.06] px-3 py-2 text-label leading-relaxed text-destructive">
            <strong>Every level is granted without a person</strong>, including level 4 production
            credentials. An agent that satisfies the judge gets them with nobody in the loop.
            <span className="block mt-1 text-destructive/80">
              This does not turn the Keeper off: a denial is still a denial, the minimum reason lengths still refuse thin
              justifications before the judge is asked, the refusal of a credential bound to nothing still applies, and every
              decision is still on the audit trail. What it removes is the human confirmation step.
            </span>
          </div>
        )}
        {canEdit && (
          <SaveFooter
            dirty={form.isDirty}
            count={form.dirtyCount}
            status={form.status}
            error={form.error}
            canSave={!budgetInvalid && !precedentInvalid && !factsInvalid}
            onSave={handleSave}
            onCancel={form.reset}
            label="Decision rules"
          />
        )}
      </SettingsCard>

      <SettingsCard icon={KeyRound} title="The four levels" description="Read-only: set on each credential in the Vault.">
        {CREDENTIAL_LEVELS.map((l, i) => (
          <SettingsRow key={l.level} label={`${l.level} · ${l.label}`} description={l.handover} border={i < CREDENTIAL_LEVELS.length - 1}>
            <span className="text-label text-muted-foreground">reason of at least {l.minReason} characters</span>
          </SettingsRow>
        ))}
      </SettingsCard>
    </>
  )
}
