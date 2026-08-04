// Fixtures for the /routines-new design preview.
//
// These are NOT mock data in the usual sense. TODAY_DSL is a faithful
// transcription of a routine that really ran in production for a month
// (monthly accounting pack: pull the bank statement out of Gmail, find
// every matching invoice, file them on Drive, reconcile the totals,
// then park on a human approval). It is here because the preview makes
// a product argument, and the argument only lands against a real
// routine: six of its seven steps are `agent_run`, and the prompts of
// those steps describe deterministic work in prose —
//
//   "spusť v containeru: date -d ... +%Y-%m"     → date arithmetic
//   "ověř POMOCÍ SKRIPTU (ne odhadem)"            → a script that exists
//   "Zapiš parse výstup do souboru /tmp/parsed.json"
//
// GRANULAR_DSL is the same routine expressed with the step kinds the
// executor already has. Nothing here needs a backend change: transform,
// script, http, foreach, notify and wait are all in
// internal/pipeline/types.go today. The difference is entirely in how
// much of the work the operator can SEE.
//
// Everything is a plain literal — the preview must render identically
// for everyone, with no workspace, no seed and no network.

import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { PipelineDSL, TraceStep } from "@/lib/trace/types"

/* ------------------------------------------------------------------ *
 *  A. The routine as it exists today — 7 steps, 6 of them opaque      *
 * ------------------------------------------------------------------ */

const TODAY_STEPS: TraceStep[] = [
  {
    id: "mesic",
    type: "agent_run",
    agent_slug: "kontrolor",
    prompt:
      "Urči účetní OBDOBÍ. Pokud je '{{ inputs.obdobi }}' neprázdné, vrať tuto hodnotu. " +
      "Jinak spusť v containeru: date -d \"$(date +%Y-%m-01) -1 day\" +%Y-%m",
  },
  {
    id: "parse",
    type: "agent_run",
    agent_slug: "kontrolor",
    needs: ["mesic"],
    prompt:
      "Zdroj výpisu = Gmail. Najdi výpis od '{{ inputs.vypis_odesilatel }}' za období " +
      "{{ steps.mesic.output }}, stáhni přílohu a rozparsuj pohyby.",
  },
  {
    id: "plan",
    type: "agent_run",
    agent_slug: "kontrolor",
    needs: ["parse"],
    prompt:
      "Skillem 'doklad-nazvy' urči u každého výdajového pohybu dodavatel_slug a typ. " +
      "Sestav worklist všech pohybů, které potřebují doklad.",
  },
  {
    id: "collect",
    type: "agent_run",
    agent_slug: "sberac",
    needs: ["plan", "mesic"],
    prompt:
      "Pro KAŽDOU položku worklistu: skillem 'dohledat-doklad' najdi doklad a nahraj " +
      "do {rok}/{Měsíc}/Přijaté/ na sdíleném disku.",
  },
  {
    id: "verify",
    type: "agent_run",
    agent_slug: "kontrolor",
    needs: ["collect", "parse"],
    prompt:
      "Jsi crew lead. DETERMINISTICKY ověř pomocí skriptu (ne odhadem): zapiš parse " +
      "výstup do /tmp/parsed.json a spusť verify.py.",
  },
  {
    id: "reconcile",
    type: "agent_run",
    agent_slug: "kontrolor",
    needs: ["parse", "verify", "mesic"],
    prompt:
      "Skillem 'rekonciliace' ověř kontrolní součet výdajů proti souhrn.vydaje_celkem " +
      "a uzavři měsíc.",
  },
  {
    id: "notify",
    type: "wait",
    needs: ["reconcile"],
    wait: {
      kind: "approval",
      approval_prompt: "Účetní podklady — zkontroluj a schval",
    },
  },
]

export const TODAY_DSL: PipelineDSL = { steps: TODAY_STEPS }

/* ------------------------------------------------------------------ *
 *  B. The same routine, granulated — 14 steps, 3 of them opaque       *
 * ------------------------------------------------------------------ */

const GRANULAR_STEPS: TraceStep[] = [
  {
    // Was an agent turn that shelled out to `date`. It is arithmetic.
    id: "obdobi",
    type: "transform",
    transform: {
      input: "{{ inputs.obdobi }}",
      expression: "default(previous_month())",
    },
  },
  {
    // The MCP gateway call the prompt described in prose.
    id: "gmail_hledat",
    type: "http",
    needs: ["obdobi"],
    http: {
      method: "POST",
      url: "https://mcp.crewship.local/gmail/messages.search",
      body: '{"from":"{{ inputs.vypis_odesilatel }}","after":"{{ steps.obdobi.output }}"}',
    },
  },
  {
    id: "stahnout_vypis",
    type: "http",
    needs: ["gmail_hledat"],
    http: {
      method: "GET",
      url: "https://mcp.crewship.local/gmail/attachment",
    },
  },
  {
    // scripts/parse_vypis.py — it already exists in the project.
    id: "parse_vypis",
    type: "script",
    needs: ["stahnout_vypis"],
    code: { runtime: "scripts/parse_vypis.py" },
  },
  {
    // Genuinely a judgement call: naming conventions, supplier matching.
    id: "pojmenovat",
    type: "agent_run",
    agent_slug: "kontrolor",
    needs: ["parse_vypis"],
    prompt: "Skillem 'doklad-nazvy' urči dodavatel_slug a typ u každého výdaje.",
  },
  {
    id: "worklist",
    type: "transform",
    needs: ["pojmenovat"],
    transform: {
      input: "{{ steps.pojmenovat.output }}",
      expression: "filter(.potrebuje_doklad)",
    },
  },
  {
    // The loop that is invisible today. This is the step the operator
    // most wants to watch: it is where the run spends its time.
    id: "sbirat",
    type: "foreach",
    needs: ["worklist", "obdobi"],
    foreach: { over: "{{ steps.worklist.output }}", as: "polozka" },
  },
  {
    // Body of the loop — still an agent, because finding the right PDF
    // in a mailbox is exactly what an agent is good at.
    id: "dohledat_doklad",
    type: "agent_run",
    agent_slug: "sberac",
    needs: ["sbirat"],
    prompt: "Skillem 'dohledat-doklad' najdi doklad k položce {{ polozka }}.",
  },
  {
    id: "drive_nahrat",
    type: "http",
    needs: ["dohledat_doklad"],
    http: {
      method: "POST",
      url: "https://mcp.crewship.local/googledrive/files.upload",
    },
  },
  {
    // scripts/verify.py — also already exists.
    id: "verify",
    type: "script",
    needs: ["drive_nahrat", "parse_vypis"],
    code: { runtime: "scripts/verify.py" },
  },
  {
    id: "kontrolni_soucet",
    type: "transform",
    needs: ["verify"],
    transform: {
      input: "{{ steps.verify.output }}",
      expression: "sum(.vydaje) == .souhrn.vydaje_celkem",
    },
  },
  {
    // The one place an LLM still earns its turn on the closing path:
    // explaining WHY the numbers disagree, when they disagree.
    id: "vysvetlit_rozdil",
    type: "agent_run",
    agent_slug: "kontrolor",
    needs: ["kontrolni_soucet"],
    prompt: "Pokud součet nesedí, vysvětli které doklady chybí a proč.",
  },
  {
    id: "oznamit",
    type: "notify",
    needs: ["vysvetlit_rozdil"],
  },
  {
    id: "schvaleni",
    type: "wait",
    needs: ["oznamit"],
    wait: {
      kind: "approval",
      approval_prompt: "Účetní podklady — zkontroluj a schval",
    },
  },
]

export const GRANULAR_DSL: PipelineDSL = { steps: GRANULAR_STEPS }

/** The two fidelities the preview toggles between. */
export type Fidelity = "today" | "granular"

export const DSL_BY_FIDELITY: Record<Fidelity, PipelineDSL> = {
  today: TODAY_DSL,
  granular: GRANULAR_DSL,
}

/**
 * Share of steps that are agent black boxes, as a whole percentage.
 *
 * This is the headline number of the whole preview: an `agent_run` is
 * a step whose behaviour the operator cannot predict from the
 * definition, only audit after the fact. Every other kind does exactly
 * what it says. Returns 0 for an empty routine rather than NaN.
 */
export function opacityOf(dsl: PipelineDSL): number {
  const steps = dsl.steps ?? []
  if (steps.length === 0) return 0
  const opaque = steps.filter((s) => s.type === "agent_run").length
  return Math.round((opaque / steps.length) * 100)
}

/* ------------------------------------------------------------------ *
 *  C. Definition mode — a run that never ran                          *
 * ------------------------------------------------------------------ */

/**
 * A synthetic run that paints every step `pending`.
 *
 * buildTraceGraph derives step status from the run (step_outputs →
 * success, current_step_id → running, …). For a DEFINITION we want none
 * of that: no outputs, no current step, no failure, and a non-terminal
 * status so the catastrophic-failure fallback stays quiet. The result
 * is the same canvas Activity draws, showing shape instead of outcome.
 *
 * A fresh object per call, because React Flow keys work off run.id and
 * a shared mutable literal would let one surface's node drags leak into
 * another's.
 */
export function definitionRun(): PipelineRun {
  return {
    id: "definition",
    pipeline_id: "preview",
    pipeline_slug: "mesicni-ucetni-podklady",
    pipeline_name: "Měsíční účetní podklady",
    status: "queued",
    mode: "definition",
    started_at: "",
    ended_at: "",
    current_step_id: "",
    step_outputs: null,
    sub_spans: null,
    cost_usd: 0,
    duration_ms: 0,
    triggered_via: "schedule",
    triggered_by_id: "",
    invoking_crew_id: "",
    invoking_agent_id: "",
    invoking_user_id: "",
    error_message: "",
    failed_at_step: "",
    issue_identifier: "",
  }
}

/* ------------------------------------------------------------------ *
 *  D. The dependency summary — "what does this thing reach"           *
 * ------------------------------------------------------------------ */

export type DependencyKind =
  | "integrations"
  | "notifications"
  | "credentials"
  | "agents"
  | "egress"

export interface DependencyItem {
  name: string
  detail: string
  /** Amber marks reach that a reviewer should look at twice. */
  risk?: boolean
}

export interface DependencyGroup {
  kind: DependencyKind
  title: string
  /** One line explaining what this group answers. */
  question: string
  items: DependencyItem[]
}

export const DEPENDENCY_SUMMARY: DependencyGroup[] = [
  {
    kind: "integrations",
    title: "Integrace (MCP)",
    question: "Ke kterým cizím službám se dostane?",
    items: [
      { name: "Gmail", detail: "hledá výpis, stahuje přílohy", risk: true },
      { name: "Google Drive", detail: "zakládá složky, nahrává doklady", risk: true },
    ],
  },
  {
    kind: "notifications",
    title: "Notifikace",
    question: "Komu se ozve a pod jakou kategorií?",
    items: [
      { name: "routines.completed", detail: "inbox karta → spouštěč běhu" },
      { name: "wait: approval", detail: "čeká na člověka, běh je zaparkovaný" },
    ],
  },
  {
    kind: "credentials",
    title: "Credentials",
    question: "Jaké tajemství drží?",
    items: [
      { name: "anthropic", detail: "klíč pro agentní kroky" },
      {
        name: "žádné přímé v DSL",
        detail: "Gmail i Drive jdou přes MCP bránu, ne přes {{ secrets.* }}",
      },
    ],
  },
  {
    kind: "agents",
    title: "Agenti",
    question: "Kdo za ni rozhoduje?",
    items: [
      { name: "kontrolor", detail: "období, pojmenování, rekonciliace" },
      { name: "sberac", detail: "dohledávání dokladů v loopu" },
    ],
  },
  {
    kind: "egress",
    title: "Egress",
    question: "Kam ven posílá data?",
    items: [
      { name: "gmail.googleapis.com", detail: "čtení schránky", risk: true },
      { name: "www.googleapis.com", detail: "zápis na Drive", risk: true },
    ],
  },
]

/* ------------------------------------------------------------------ *
 *  E. Run history — the bridge to /activity                           *
 * ------------------------------------------------------------------ */

export interface PreviewRun {
  id: string
  started_at: string
  status: "completed" | "failed" | "waiting"
  duration_ms: number
  cost_usd: number
  trigger: "schedule" | "manual"
  /** Human summary of the outcome, the thing a list of ids never says. */
  summary: string
}

/** Newest first — the card renders in array order and never re-sorts. */
export const RUN_HISTORY: PreviewRun[] = [
  {
    id: "run_cms7notoy0001cd68ffc4",
    started_at: "2026-08-01T06:05:00Z",
    status: "waiting",
    duration_ms: 812_000,
    cost_usd: 1.42,
    trigger: "schedule",
    summary: "18 dokladů nahráno · čeká na schválení",
  },
  {
    id: "run_cms6jklmn0002ab31de90",
    started_at: "2026-07-01T06:05:00Z",
    status: "completed",
    duration_ms: 744_000,
    cost_usd: 1.28,
    trigger: "schedule",
    summary: "22 dokladů · součet sedí",
  },
  {
    id: "run_cms5abcde0003ff77aa21",
    started_at: "2026-06-03T09:41:00Z",
    status: "failed",
    duration_ms: 96_000,
    cost_usd: 0.11,
    trigger: "manual",
    summary: "výpis za období nenalezen ve schránce",
  },
  {
    id: "run_cms4zzxxy0004bb12cc33",
    started_at: "2026-06-01T06:05:00Z",
    status: "completed",
    duration_ms: 690_000,
    cost_usd: 1.19,
    trigger: "schedule",
    summary: "19 dokladů · 2 chybějící dohledány ručně",
  },
]

/* ------------------------------------------------------------------ *
 *  F. The DSL as the editor pane shows it                             *
 * ------------------------------------------------------------------ */

/**
 * Pretty-printed definition for the code pane.
 *
 * Derived from the same fixture the canvas draws, so the two halves of
 * the split can never disagree — which is the entire point of the
 * design being previewed.
 */
export function dslSource(fidelity: Fidelity): string {
  const dsl = DSL_BY_FIDELITY[fidelity]
  return JSON.stringify(
    {
      dsl_version: "1.0",
      name: "mesicni-ucetni-podklady",
      display_name: "Měsíční účetní podklady",
      description:
        "Stáhne bankovní výpis z Gmailu, dohledá doklady, založí je na Drive, " +
        "zrekonciluje součty a nechá člověka schválit.",
      inputs: [
        { name: "obdobi", type: "string", required: false },
        { name: "ucetnictvi_root", type: "string", required: true },
        { name: "vypis_odesilatel", type: "string", required: true },
      ],
      integrations_required: ["gmail", "googledrive"],
      max_cost_usd: 5,
      steps: dsl.steps,
    },
    null,
    2,
  )
}
