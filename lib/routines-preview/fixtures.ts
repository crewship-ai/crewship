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
    id: "period",
    type: "agent_run",
    agent_slug: "auditor",
    prompt:
      "Determine the accounting PERIOD. If '{{ inputs.period }}' is non-empty, return it. " +
      "Otherwise run in the container: date -d \"$(date +%Y-%m-01) -1 day\" +%Y-%m",
  },
  {
    id: "parse",
    type: "agent_run",
    agent_slug: "auditor",
    needs: ["period"],
    prompt:
      "Statement source = Gmail. Find the statement from '{{ inputs.statement_sender }}' for " +
      "{{ steps.period.output }}, download the attachment and parse the transactions.",
  },
  {
    id: "plan",
    type: "agent_run",
    agent_slug: "auditor",
    needs: ["parse"],
    prompt:
      "Using the 'invoice-naming' skill, derive supplier_slug and type for every outgoing " +
      "transaction. Build a worklist of everything still missing its document.",
  },
  {
    id: "collect",
    type: "agent_run",
    agent_slug: "collector",
    needs: ["plan", "period"],
    prompt:
      "For EVERY worklist item: use the 'find-invoice' skill to locate the document and " +
      "upload it to {year}/{Month}/Received/ on the shared drive.",
  },
  {
    id: "verify",
    type: "agent_run",
    agent_slug: "auditor",
    needs: ["collect", "parse"],
    prompt:
      "You are the crew lead. Verify DETERMINISTICALLY with a script, not by judgement: " +
      "write the parse output to /tmp/parsed.json and run verify.py.",
  },
  {
    id: "reconcile",
    type: "agent_run",
    agent_slug: "auditor",
    needs: ["parse", "verify", "period"],
    prompt:
      "Using the 'reconcile' skill, check the outgoing total against summary.total_out " +
      "and close the month.",
  },
  {
    id: "notify",
    type: "wait",
    needs: ["reconcile"],
    wait: {
      kind: "approval",
      approval_prompt: "Accounting pack — review and approve",
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
    id: "period",
    type: "transform",
    transform: {
      input: "{{ inputs.period }}",
      expression: "default(previous_month())",
    },
  },
  {
    // The MCP gateway call the prompt described in prose.
    id: "gmail_search",
    type: "http",
    needs: ["period"],
    http: {
      method: "POST",
      url: "https://mcp.crewship.local/gmail/messages.search",
      body: '{"from":"{{ inputs.statement_sender }}","after":"{{ steps.obdobi.output }}"}',
    },
  },
  {
    id: "download_statement",
    type: "http",
    needs: ["gmail_search"],
    http: {
      method: "GET",
      url: "https://mcp.crewship.local/gmail/attachment",
    },
  },
  {
    // scripts/parse_vypis.py — it already exists in the project.
    id: "parse_statement",
    type: "script",
    needs: ["download_statement"],
    script: { path: "scripts/parse_vypis.py" },
  },
  {
    // Genuinely a judgement call: naming conventions, supplier matching.
    id: "name_documents",
    type: "agent_run",
    agent_slug: "auditor",
    needs: ["parse_statement"],
    prompt: "Using the 'invoice-naming' skill, derive supplier_slug and type for each expense.",
  },
  {
    id: "worklist",
    type: "transform",
    needs: ["name_documents"],
    transform: {
      input: "{{ steps.name_documents.output }}",
      expression: "filter(.potrebuje_doklad)",
    },
  },
  {
    // The loop that is invisible today. This is the step the operator
    // most wants to watch: it is where the run spends its time.
    id: "collect",
    type: "foreach",
    needs: ["worklist", "period"],
    foreach: { items: "{{ steps.worklist.output }}", as: "item" },
  },
  {
    // Body of the loop — still an agent, because finding the right PDF
    // in a mailbox is exactly what an agent is good at.
    id: "find_document",
    type: "agent_run",
    agent_slug: "collector",
    needs: ["collect"],
    prompt: "Using the 'find-invoice' skill, locate the document for {{ item }}.",
  },
  {
    id: "drive_upload",
    type: "http",
    needs: ["find_document"],
    http: {
      method: "POST",
      url: "https://mcp.crewship.local/googledrive/files.upload",
    },
  },
  {
    // scripts/verify.py — also already exists.
    id: "verify",
    type: "script",
    needs: ["drive_upload", "parse_statement"],
    script: { path: "scripts/verify.py" },
  },
  {
    id: "checksum",
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
    id: "explain_gap",
    type: "agent_run",
    agent_slug: "auditor",
    needs: ["checksum"],
    prompt: "If the totals disagree, explain which documents are missing and why.",
  },
  {
    id: "notify_owner",
    type: "notify",
    needs: ["explain_gap"],
    notify: { to: "trigger", title: "Accounting pack is ready", category: "routines.completed" },
  },
  {
    id: "approval",
    type: "wait",
    needs: ["notify_owner"],
    wait: {
      kind: "approval",
      approval_prompt: "Accounting pack — review and approve",
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
    pipeline_name: "Monthly accounting pack",
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
    title: "Integrations (MCP)",
    question: "Which third-party services can it reach?",
    items: [
      { name: "Gmail", detail: "searches for the statement, downloads attachments", risk: true },
      { name: "Google Drive", detail: "creates folders, uploads documents", risk: true },
    ],
  },
  {
    kind: "notifications",
    title: "Notifications",
    question: "Who does it tell, and under which category?",
    items: [
      { name: "routines.completed", detail: "inbox card → whoever triggered the run" },
      { name: "wait: approval", detail: "parks the run until a human decides" },
    ],
  },
  {
    kind: "credentials",
    title: "Credentials",
    question: "Which secrets does it hold?",
    items: [
      { name: "anthropic", detail: "key for the agent steps" },
      {
        name: "none referenced in the DSL",
        detail: "Gmail and Drive go through the MCP gateway, not {{ secrets.* }}",
      },
    ],
  },
  {
    kind: "agents",
    title: "Agents",
    question: "Who makes the judgement calls?",
    items: [
      { name: "auditor", detail: "period, naming, reconciliation" },
      { name: "collector", detail: "finding documents inside the loop" },
    ],
  },
  {
    kind: "egress",
    title: "Egress",
    question: "Where does data leave to?",
    items: [
      { name: "gmail.googleapis.com", detail: "reading the mailbox", risk: true },
      { name: "www.googleapis.com", detail: "writing to Drive", risk: true },
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
    summary: "18 documents filed · waiting for approval",
  },
  {
    id: "run_cms6jklmn0002ab31de90",
    started_at: "2026-07-01T06:05:00Z",
    status: "completed",
    duration_ms: 744_000,
    cost_usd: 1.28,
    trigger: "schedule",
    summary: "22 documents · totals reconcile",
  },
  {
    id: "run_cms5abcde0003ff77aa21",
    started_at: "2026-06-03T09:41:00Z",
    status: "failed",
    duration_ms: 96_000,
    cost_usd: 0.11,
    trigger: "manual",
    summary: "no statement for that period in the mailbox",
  },
  {
    id: "run_cms4zzxxy0004bb12cc33",
    started_at: "2026-06-01T06:05:00Z",
    status: "completed",
    duration_ms: 690_000,
    cost_usd: 1.19,
    trigger: "schedule",
    summary: "19 documents · 2 gaps closed by hand",
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
      display_name: "Monthly accounting pack",
      description:
        "Pulls the bank statement out of Gmail, finds every matching invoice, files them " +
        "on Drive, reconciles the totals and parks on a human approval.",
      inputs: [
        { name: "period", type: "string", required: false },
        { name: "accounting_root", type: "string", required: true },
        { name: "statement_sender", type: "string", required: true },
      ],
      integrations_required: ["gmail", "googledrive"],
      max_cost_usd: 5,
      steps: dsl.steps,
    },
    null,
    2,
  )
}
