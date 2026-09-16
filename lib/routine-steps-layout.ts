// routine-steps-layout — the shape of a routine's step list, derived from the
// DSL and nothing else.
//
// The step list used to be ten flat rows, each carrying "Depends on: scan,
// scan_state, …" as raw ids, and five helper transforms as five full rows.
// Real recipes (Docs drift audit: 10 steps, 9 dependencies; the nightly
// matrix: 101 steps) were one long wall. These rules make them readable
// without inventing anything the recipe does not declare:
//
//   1. phases from `needs` — each step sits at 1 + the deepest level of its
//      dependencies; without any `needs` the list keeps plain numbering;
//   2. ≥ 3 consecutive `transform` steps with identical `needs` and no `if`
//      fold into one row ("5 data preparations from Scan");
//   3. a `foreach` is a nested block: the parent row, then its body indented;
//   4. routine hooks (`before_all`, `after_all`, `on_failure`) are muted rows
//      around the run;
//   5. `if`, `needs`, checks, retry, timeout, script path and agent become
//      chips — see `stepChips`;
//   7. a phased recipe with more than 12 top-level steps is "big": the map is
//      the default view and a phase with more than 6 steps collapses into one
//      row that opens into groups (foreach parent → name pattern → same needs).
//
// Pure and framework-free, so it is tested as data.

import { describeStep, isRecord, asString } from "./routine-step-describe"

export type Step = Record<string, unknown>

export const BIG_RECIPE_STEPS = 12
export const COLLAPSE_PHASE_STEPS = 6
export const FOLD_MIN_TRANSFORMS = 3

/** The performer/role word for a step kind, as the prototype names them. */
export const STEP_ROLE_LABEL: Record<string, string> = {
  agent_run: "Agent",
  script: "Script",
  transform: "Prepare data",
  http: "Call a service",
  wait: "A person",
  code: "Run code",
  notify: "Notification",
  query: "Read stored data",
  foreach: "For each item",
  call_pipeline: "Run another routine",
  crewship: "Crewship action",
}

export function stepRoleLabel(step: Step): string {
  const type = asString(step.type)
  if (type === "wait") {
    const wait = isRecord(step.wait) ? step.wait : {}
    return asString(wait.kind) === "approval" || !asString(wait.kind) ? "A person" : "Wait"
  }
  return STEP_ROLE_LABEL[type] ?? "Step"
}

export function stepDisplayName(step: Step, position: number): string {
  return describeStep(step, position).title
}

/** Level of every top-level step: 1 + max level of its dependencies. */
export function levelsOf(steps: Step[]): Record<string, number> {
  const byId = new Map<string, Step>()
  for (const s of steps) byId.set(String(s.id), s)
  const levels: Record<string, number> = {}
  const visiting = new Set<string>()
  const get = (s: Step): number => {
    const id = String(s.id)
    if (levels[id]) return levels[id]
    if (visiting.has(id)) return 1
    visiting.add(id)
    const needs = Array.isArray(s.needs) ? s.needs.filter((n): n is string => typeof n === "string") : []
    let max = 0
    for (const n of needs) {
      const dep = byId.get(n)
      if (dep) max = Math.max(max, get(dep))
    }
    visiting.delete(id)
    levels[id] = 1 + max
    return levels[id]
  }
  steps.forEach(get)
  return levels
}

export function foreachBody(step: Step): Step[] {
  const loop = isRecord(step.foreach) ? step.foreach : null
  return loop && Array.isArray(loop.steps) ? loop.steps.filter(isRecord) : []
}

export function routineHooks(definition: Step): { key: HookKey; step: Step }[] {
  const hooks = isRecord(definition.hooks) ? definition.hooks : {}
  const out: { key: HookKey; step: Step }[] = []
  for (const key of ["before_all", "after_all", "on_failure"] as const) {
    if (isRecord(hooks[key])) out.push({ key, step: hooks[key] as Step })
  }
  return out
}

export const HOOK_LABEL: Record<HookKey, string> = {
  before_all: "Before the run",
  after_all: "After the run",
  on_failure: "If the run fails",
}

export type HookKey = "before_all" | "after_all" | "on_failure"

/** A function from a step id to the name a person reads. */
export type NameOf = (id: string) => string

export function nameLookup(definition: Step): NameOf {
  const all = new Map<string, string>()
  const steps = Array.isArray(definition.steps) ? definition.steps.filter(isRecord) : []
  const visit = (list: Step[]) => {
    list.forEach((s, i) => {
      const id = String(s.id ?? "")
      if (id && !all.has(id)) all.set(id, stepDisplayName(s, i + 1))
      visit(foreachBody(s))
    })
  }
  visit(steps)
  for (const hook of routineHooks(definition)) {
    const id = String(hook.step.id ?? "")
    if (id && !all.has(id)) all.set(id, stepDisplayName(hook.step, 0))
  }
  return (id) => all.get(id) ?? id
}

/** "10 min", "45 s", "1 h 30 min". */
export function describeTimeout(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return ""
  if (seconds < 60) return `${seconds} s`
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} min`
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  return rest ? `${hours} h ${rest} min` : `${hours} h`
}

export interface StepChips {
  only?: string
  after: string[]
  checks?: { count: number; grader?: string }
  attempts?: number
  timeout?: string
  file?: string
  agent?: string
}

/** Everything the row shows as a chip instead of a sentence. */
export function stepChips(step: Step, nameOf: NameOf): StepChips {
  const chips: StepChips = { after: [] }
  if (typeof step.if === "string" && step.if.trim()) chips.only = step.if.trim()
  if (Array.isArray(step.needs))
    chips.after = step.needs.filter((n): n is string => typeof n === "string").map(nameOf)
  const outcomes = isRecord(step.outcomes) ? step.outcomes : null
  const validation = isRecord(step.validation) ? step.validation : null
  if (outcomes && Array.isArray(outcomes.criteria) && outcomes.criteria.length) {
    chips.checks = {
      count: outcomes.criteria.length,
      grader: asString(outcomes.grader_agent_slug) || undefined,
    }
  } else if (validation) {
    let count = 0
    if (Array.isArray(validation.must_contain)) count += validation.must_contain.length
    if (Array.isArray(validation.must_not_contain)) count += validation.must_not_contain.length
    if (validation.min_length != null) count += 1
    if (validation.max_length != null) count += 1
    if (isRecord(validation.schema)) count += 1
    if (count) chips.checks = { count, grader: undefined }
  }
  const retry = isRecord(step.retry) ? step.retry : null
  if (retry && typeof retry.max_attempts === "number" && retry.max_attempts > 1)
    chips.attempts = retry.max_attempts
  if (typeof step.timeout_seconds === "number") chips.timeout = describeTimeout(step.timeout_seconds) || undefined
  const script = isRecord(step.script) ? step.script : null
  if (script && asString(script.path)) chips.file = asString(script.path)
  if (asString(step.agent_slug)) chips.agent = asString(step.agent_slug)
  return chips
}

/** The group a step's name puts it in: "Health on Billing" → Billing. */
export function groupKeyOf(name: string): { key: string; name: string } | null {
  const on = name.indexOf(" on ")
  if (on > 0) {
    const target = name.slice(on + 4).trim()
    if (target) return { key: target, name: target }
  }
  for (const sep of [": ", " — ", " – "]) {
    const at = name.indexOf(sep)
    if (at > 0) {
      const head = name.slice(0, at).trim()
      if (head) return { key: head, name: head }
    }
  }
  return null
}

export interface StepGroup {
  key: string
  name: string
  steps: Step[]
  /** Names of what the whole group follows, when every member shares them. */
  after: string[]
}

export function groupSteps(steps: Step[], nameOf: NameOf): StepGroup[] {
  const groups = new Map<string, StepGroup>()
  steps.forEach((s, i) => {
    const byName = groupKeyOf(stepDisplayName(s, i + 1))
    const needs = Array.isArray(s.needs) ? s.needs.filter((n): n is string => typeof n === "string") : []
    const key = byName ? `name:${byName.key}` : `needs:${JSON.stringify(needs)}`
    const name = byName ? byName.name : needs.length ? `After ${needs.map(nameOf).join(", ")}` : "Other steps"
    const group = groups.get(key) ?? { key, name, steps: [], after: needs.map(nameOf) }
    if (!groups.has(key)) groups.set(key, group)
    group.steps.push(s)
    if (JSON.stringify(group.after) !== JSON.stringify(needs.map(nameOf))) group.after = []
  })
  return [...groups.values()]
}

export interface Phase {
  level: number
  label: string
  steps: Step[]
  groups: StepGroup[]
  /** A big recipe's wide phase: one clickable row instead of its steps. */
  collapsed: boolean
  /** "Script ×64 · Call a service ×32". */
  kinds: [string, number][]
}

export type LayoutRow =
  | {
      kind: "phase"
      key: string
      level: number
      label: string
      count: number
      collapsed: boolean
      open: boolean
      kinds: [string, number][]
      groups: number
    }
  | {
      kind: "step"
      key: string
      step: Step
      id: string
      /** 1-based position in the whole recipe, for the badge without needs. */
      position: number
      level: number
      nested: boolean
      hook?: HookKey
      loop?: { items: string; parallelism: number; count: number }
    }
  | { kind: "fold"; key: string; steps: Step[]; from: string; level: number }
  | { kind: "group"; key: string; name: string; steps: Step[]; level: number; after: string[]; open: boolean }

export interface RoutineStepsLayout {
  hasNeeds: boolean
  big: boolean
  rows: LayoutRow[]
  phases: Phase[]
  topLevel: number
  nested: number
  hookCount: number
  nameOf: NameOf
}

export interface LayoutOptions {
  /** Levels whose collapsed phase row is open. */
  openPhases?: Iterable<number>
  /** Group keys whose steps are listed. */
  openGroups?: Iterable<string>
}

export function layoutRoutineSteps(definition: unknown, options: LayoutOptions = {}): RoutineStepsLayout {
  const dsl = isRecord(definition) ? definition : {}
  const steps = Array.isArray(dsl.steps) ? dsl.steps.filter(isRecord) : []
  const nameOf = nameLookup(dsl)
  const openPhases = new Set(options.openPhases ?? [])
  const openGroups = new Set(options.openGroups ?? [])
  const hasNeeds = steps.some((s) => Array.isArray(s.needs) && s.needs.length > 0)
  // A long recipe without `needs` is a long list, not a wide one: there are
  // no phases to draw as columns and nothing to collapse, so it keeps plain
  // numbering and the row cap.
  const big = hasNeeds && steps.length > BIG_RECIPE_STEPS
  const levels = levelsOf(steps)
  const position = new Map<Step, number>()
  steps.forEach((s, i) => position.set(s, i + 1))

  const byLevel = new Map<number, Step[]>()
  for (const s of steps) {
    const level = levels[String(s.id)] ?? 1
    if (!byLevel.has(level)) byLevel.set(level, [])
    byLevel.get(level)!.push(s)
  }
  const phases: Phase[] = [...byLevel.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([level, group]) => {
      const kinds = new Map<string, number>()
      for (const s of group) kinds.set(stepRoleLabel(s), (kinds.get(stepRoleLabel(s)) ?? 0) + 1)
      const collapsed = big && group.length > COLLAPSE_PHASE_STEPS
      const label = collapsed
        ? `${level === 1 ? "First" : "Then"} · ${group.length} steps in parallel`
        : `${level === 1 ? "First" : "Then"}${group.length > 1 ? ` · ${group.length} in parallel` : ""}`
      return {
        level,
        label,
        steps: group,
        groups: collapsed ? groupSteps(group, nameOf) : [],
        collapsed,
        kinds: [...kinds.entries()],
      }
    })

  const rows: LayoutRow[] = []
  let nested = 0
  const stepRow = (s: Step, level: number, extra: Partial<Extract<LayoutRow, { kind: "step" }>> = {}): LayoutRow => {
    const body = foreachBody(s)
    const loop = isRecord(s.foreach) ? s.foreach : null
    return {
      kind: "step",
      key: `step:${String(s.id ?? position.get(s) ?? "")}`,
      step: s,
      id: String(s.id ?? position.get(s) ?? ""),
      position: position.get(s) ?? 0,
      level,
      nested: false,
      ...(loop
        ? {
            loop: {
              items: asString(loop.items),
              parallelism: typeof loop.parallelism === "number" ? loop.parallelism : 0,
              count: body.length,
            },
          }
        : {}),
      ...extra,
    }
  }
  const pushWithBody = (s: Step, level: number, extra: Partial<Extract<LayoutRow, { kind: "step" }>> = {}) => {
    rows.push(stepRow(s, level, extra))
    for (const child of foreachBody(s)) {
      nested += 1
      rows.push({
        kind: "step",
        key: `step:${String(s.id)}/${String(child.id)}`,
        step: child,
        id: String(child.id ?? ""),
        position: 0,
        level,
        nested: true,
      })
    }
  }

  const hooks = routineHooks(dsl)
  const hookRow = (hook: { key: HookKey; step: Step }) => {
    rows.push({ kind: "phase", key: `phase:${hook.key}`, level: 0, label: HOOK_LABEL[hook.key], count: 1, collapsed: false, open: true, kinds: [], groups: 0 })
    rows.push({
      kind: "step",
      key: `hook:${hook.key}`,
      step: hook.step,
      id: String(hook.step.id ?? hook.key),
      position: 0,
      level: 0,
      nested: false,
      hook: hook.key,
    })
  }
  for (const hook of hooks) if (hook.key === "before_all") hookRow(hook)

  for (const phase of phases) {
    if (phase.collapsed) {
      const open = openPhases.has(phase.level)
      rows.push({
        kind: "phase",
        key: `phase:${phase.level}`,
        level: phase.level,
        label: phase.label,
        count: phase.steps.length,
        collapsed: true,
        open,
        kinds: phase.kinds,
        groups: phase.groups.length,
      })
      if (!open) continue
      for (const group of phase.groups) {
        const groupOpen = openGroups.has(group.key)
        rows.push({ kind: "group", key: `group:${phase.level}:${group.key}`, name: group.name, steps: group.steps, level: phase.level, after: group.after, open: groupOpen })
        if (groupOpen) for (const s of group.steps) pushWithBody(s, phase.level)
      }
      continue
    }
    if (hasNeeds)
      rows.push({ kind: "phase", key: `phase:${phase.level}`, level: phase.level, label: phase.label, count: phase.steps.length, collapsed: false, open: true, kinds: phase.kinds, groups: 0 })
    let i = 0
    const group = phase.steps
    while (i < group.length) {
      const s = group[i]
      const signature = JSON.stringify(Array.isArray(s.needs) ? s.needs : [])
      const run: Step[] = []
      let j = i
      while (
        j < group.length &&
        asString(group[j].type) === "transform" &&
        !group[j].if &&
        JSON.stringify(Array.isArray(group[j].needs) ? group[j].needs : []) === signature
      )
        run.push(group[j++])
      if (hasNeeds && run.length >= FOLD_MIN_TRANSFORMS) {
        const needs = Array.isArray(s.needs) ? s.needs.filter((n): n is string => typeof n === "string") : []
        rows.push({ kind: "fold", key: `fold:${String(s.id)}`, steps: run, from: needs.length ? nameOf(needs[0]) : "the inputs", level: phase.level })
        i = j
        continue
      }
      pushWithBody(s, phase.level)
      i += 1
    }
  }

  for (const hook of hooks) if (hook.key !== "before_all") hookRow(hook)

  return { hasNeeds, big, rows, phases, topLevel: steps.length, nested, hookCount: hooks.length, nameOf }
}

/** Script paths the recipe declares, with the steps that use them. */
export function routineStepFiles(definition: unknown): { path: string; step_ids: string[] }[] {
  const dsl = isRecord(definition) ? definition : {}
  const found = new Map<string, string[]>()
  const visit = (s: Step) => {
    const script = isRecord(s.script) ? s.script : null
    const path = script ? asString(script.path).replace(/^\/crew\/shared\//, "") : ""
    if (path) {
      const ids = found.get(path) ?? []
      const id = String(s.id ?? "")
      if (id && !ids.includes(id)) ids.push(id)
      found.set(path, ids)
    }
    foreachBody(s).forEach(visit)
    const hooks = isRecord(s.hooks) ? s.hooks : {}
    for (const key of ["before", "after"]) if (isRecord(hooks[key])) visit(hooks[key] as Step)
  }
  const steps = Array.isArray(dsl.steps) ? dsl.steps.filter(isRecord) : []
  steps.forEach(visit)
  routineHooks(dsl).forEach((h) => visit(h.step))
  return [...found.entries()].map(([path, step_ids]) => ({ path, step_ids }))
}
