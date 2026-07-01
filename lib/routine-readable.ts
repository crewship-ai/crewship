// routine-readable — turn a routine (pipeline) DSL into a plain-language
// summary so users never have to read raw JSON to understand what a
// routine does.
//
// Pure + framework-free (no React, no DOM, no network). The same
// primitive backs two surfaces:
//   • the describe-first draft preview (a Lead drafts a routine, we show
//     it back in readable form before save), and
//   • the routine Overview tab ("what does this routine actually do?").
//
// The per-step renderer + URL helpers now live in the shared
// lib/routine-step-describe module so the readable summary, the flow
// diagram and the mini-trace all render a step identically. This file
// keeps the routine-level concerns: the trigger line, integrations,
// inputs, and the top-level describeRoutine assembly.
//
// The renderer is intentionally defensive: a half-formed or
// agent-authored DSL must never throw here, because both call sites are
// render paths. Unknown step kinds degrade to a generic line rather
// than crashing.

import {
  describeStep,
  isRecord,
  asString,
  clip,
  type ReadableStep,
  type ReadableStepKind,
} from "@/lib/routine-step-describe"

// Re-export the canonical per-step renderer + its types so existing
// consumers importing them from "@/lib/routine-readable" keep working.
export { describeStep }
export type { ReadableStep, ReadableStepKind }

export interface ReadableInput {
  name: string
  type: string
  required: boolean
}

export interface ReadableRoutine {
  name?: string
  description?: string
  /** One sentence describing what kicks the routine off. */
  trigger: string
  steps: ReadableStep[]
  /** Declared third-party connector slugs (integrations_required), trimmed
   *  + de-duped. Raw slugs — the caller can label them. */
  integrations: string[]
  /** Declared inputs (name + type + required). */
  inputs: ReadableInput[]
}

/* ------------------------------------------------------------------ *
 *  Trigger line                                                       *
 * ------------------------------------------------------------------ */

function describeTrigger(dsl: Record<string, unknown>): string {
  // Routines carry their schedule/webhook wiring outside the typed DSL
  // (separate schedule + webhook entities), but agent-authored or
  // imported manifests often embed a hint. Render whatever is present;
  // otherwise this is a manual / on-demand routine.
  const trigger = dsl["trigger"]
  if (typeof trigger === "string" && trigger.trim()) return clip(trigger, 120)
  if (isRecord(trigger)) {
    const t = asString(trigger["type"]).trim()
    const cron = asString(trigger["cron"]) || asString(trigger["cron_expr"])
    if (t === "schedule" || cron) return cron ? `On schedule (${cron})` : "On a schedule"
    if (t === "webhook") return "When its webhook is called"
    if (t === "event") {
      const ev = asString(trigger["event"]) || asString(trigger["event_type"])
      return ev ? `When the "${ev}" event fires` : "On an event"
    }
    if (t) return clip(t, 120)
  }

  const schedules = dsl["schedules"]
  if (Array.isArray(schedules) && schedules.length > 0) {
    const crons = schedules
      .filter(isRecord)
      .map((s) => asString(s["cron"]) || asString(s["cron_expr"]))
      .filter((c) => c.length > 0)
    if (crons.length > 0) return `On schedule (${crons.join(", ")})`
    return "On a schedule"
  }

  return "Runs manually / on demand"
}

/* ------------------------------------------------------------------ *
 *  Top-level                                                          *
 * ------------------------------------------------------------------ */

export function describeRoutine(dsl: unknown): ReadableRoutine {
  if (!isRecord(dsl)) {
    return {
      trigger: "Runs manually / on demand",
      steps: [],
      integrations: [],
      inputs: [],
    }
  }

  const rawSteps = Array.isArray(dsl["steps"]) ? dsl["steps"] : []
  const steps = rawSteps.map((s, i) => describeStep(s, i + 1))

  const integrations = Array.isArray(dsl["integrations_required"])
    ? Array.from(
        new Set(
          dsl["integrations_required"]
            .filter((s): s is string => typeof s === "string")
            .map((s) => s.trim())
            .filter((s) => s.length > 0),
        ),
      )
    : []

  const inputs = (Array.isArray(dsl["inputs"]) ? dsl["inputs"] : [])
    .filter(isRecord)
    .map((inp) => ({
      name: asString(inp["name"]),
      type: asString(inp["type"]) || "string",
      required: inp["required"] === true,
    }))
    .filter((inp) => inp.name.length > 0)

  return {
    name: asString(dsl["name"]) || undefined,
    description: asString(dsl["description"]) || undefined,
    trigger: describeTrigger(dsl),
    steps,
    integrations,
    inputs,
  }
}
