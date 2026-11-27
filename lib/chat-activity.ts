import type { TurnPart } from "@/hooks/use-chat"

/** What a tool step is doing, as the user should read it. `unknown` is the
 *  honest answer for a finished turn with no recorded outcome; it is never
 *  rendered as success. */
export type ActivityState =
  | "running"
  | "awaiting_approval"
  | "succeeded"
  | "failed"
  | "denied"
  | "interrupted"
  | "unknown"

/** User-facing label per state. The frontend owns this text, the server only
 *  sends the state token. */
export const ACTIVITY_STATE_LABEL: Record<ActivityState, string> = {
  running: "Running",
  awaiting_approval: "Awaiting approval",
  succeeded: "Completed",
  failed: "Failed",
  denied: "Denied",
  interrupted: "Interrupted",
  unknown: "Outcome unknown",
}

/** A tool step: the call (or typed activity part) plus its paired result and
 *  any correlated children. Structurally the same as turn-grouping's ToolNode. */
export interface ToolStep {
  call: TurnPart
  result?: TurnPart
  children?: ToolStep[]
}

/** Correlation id of a part: the typed `tool_call_id`, else `metadata.tool_id`,
 *  else `metadata.tool_use_id` (what legacy live results carry). */
export function toolIdOf(p: TurnPart): string | undefined {
  if (p.tool_call_id) return p.tool_call_id
  const m = p.metadata
  if (typeof m?.tool_id === "string" && m.tool_id) return m.tool_id
  if (typeof m?.tool_use_id === "string" && m.tool_use_id) return m.tool_use_id
  return undefined
}

/** Parent correlation id: the typed `parent_tool_call_id`, else the legacy
 *  `metadata.parent_tool_use_id` / `parent_tool_call_id`. */
export function parentToolIdOf(p: TurnPart): string | undefined {
  if (p.parent_tool_call_id) return p.parent_tool_call_id
  const m = p.metadata
  if (typeof m?.parent_tool_use_id === "string" && m.parent_tool_use_id) return m.parent_tool_use_id
  if (typeof m?.parent_tool_call_id === "string" && m.parent_tool_call_id) return m.parent_tool_call_id
  return undefined
}

/** Resolve the state of one tool step. Order: the typed activity state, then
 *  the paired result (is_error / denied), then whether the turn is still live. */
export function toolState(step: Pick<ToolStep, "call" | "result">, turnStreaming: boolean): ActivityState {
  const typed = step.call.activity?.state
  if (typed) {
    // A step still "running" on a turn that is no longer streaming has no
    // live source left; it is unknown, as for a call with no recorded result.
    if (typed === "input_streaming" || typed === "running") return turnStreaming ? "running" : "unknown"
    // A token this build does not know must not read as success or failure.
    return Object.hasOwn(ACTIVITY_STATE_LABEL, typed) ? typed : "unknown"
  }
  // A result with no call in this turn classifies itself.
  if (step.call.type === "tool_result" && !step.result) {
    const m = step.call.metadata
    if (m?.denied === true) return "denied"
    return m?.is_error === true ? "failed" : "succeeded"
  }
  if (step.result) {
    const r = step.result.metadata
    if (r?.denied === true) return "denied"
    if (r?.is_error === true) return "failed"
    return "succeeded"
  }
  return turnStreaming ? "running" : "unknown"
}

/** True for a call that is waiting on a human and must stay visible outside
 *  any collapsed group. */
export function needsAttention(part: TurnPart): boolean {
  return part.activity?.state === "awaiting_approval"
}

export function flattenSteps(steps: ToolStep[]): ToolStep[] {
  return steps.flatMap((s) => [s, ...flattenSteps(s.children ?? [])])
}

/** `neutral`: finished with steps that have no recorded result. That can be a
 *  turn rebuilt mid-run, so it is neither success nor an alarm. */
export type GroupTone = "running" | "ok" | "attention" | "neutral"

export interface GroupSummary {
  tone: GroupTone
  /** Headline word: Working / Worked / Needs approval / Activity incomplete. */
  title: string
  /** Counts of non-success outcomes, e.g. "1 failed, 1 denied". */
  detail: string
}

/** Summarise a group. Success is only claimed when EVERY step succeeded; one
 *  failed, denied, interrupted or unknown step makes the whole group read as
 *  needing attention. */
export function summarizeGroup(states: ActivityState[]): GroupSummary {
  const count = (s: ActivityState) => states.filter((x) => x === s).length
  const parts = (["failed", "denied", "interrupted", "unknown"] as const)
    .map((s) => [s, count(s)] as const)
    .filter(([, n]) => n > 0)
    .map(([s, n]) => `${n} ${s === "unknown" ? "unknown" : s}`)
  const detail = parts.join(", ")
  const hard = count("failed") + count("denied") + count("interrupted")
  if (count("awaiting_approval") > 0) return { tone: "attention", title: "Needs approval", detail }
  if (hard === 0 && count("unknown") > 0 && count("running") === 0) {
    return { tone: "neutral", title: "Finished", detail: `${count("unknown")} without a recorded result` }
  }
  if (parts.length > 0) {
    const settled = count("running") === 0
    // A step that failed in a turn that otherwise ran to its end is a failure
    // to read, never "Worked" and not an unfinished turn; only a denied or interrupted step makes
    // the activity incomplete.
    const onlyFailed = count("denied") + count("interrupted") === 0 && count("unknown") === 0
    if (settled && onlyFailed) return { tone: "attention", title: "Finished", detail }
    return { tone: "attention", title: settled ? "Activity incomplete" : "Working", detail }
  }
  if (count("running") > 0) return { tone: "running", title: "Working", detail }
  return { tone: "ok", title: "Worked", detail }
}

/** Name of the tool a step called, as the stream recorded it. */
export function toolNameOf(step: Pick<ToolStep, "call">): string {
  const c = step.call
  if (c.activity?.name) return c.activity.name
  if (typeof c.metadata?.tool_name === "string" && c.metadata.tool_name) return c.metadata.tool_name
  return c.content ?? ""
}

/** The input a step was called with, as recorded. */
function inputOf(step: Pick<ToolStep, "call">): unknown {
  const c = step.call
  return c.activity?.parameters ?? c.metadata?.input
}

const FILE_TOOLS = new Set(["Edit", "MultiEdit", "Write", "Read", "NotebookEdit"])

/** What a step acted on: the file for file tools, otherwise the whole input.
 *  Two steps are the same attempt only when tool and target are the same. */
function targetOf(step: Pick<ToolStep, "call">): string | undefined {
  const input = inputOf(step)
  if (input === undefined || input === null) return undefined
  const name = toolNameOf(step)
  if (FILE_TOOLS.has(name) && typeof input === "object") {
    const o = input as Record<string, unknown>
    const path = o.file_path ?? o.path ?? o.notebook_path
    return typeof path === "string" && path ? `file:${path}` : undefined
  }
  const stable = stableStringify(input)
  return stable === undefined ? undefined : `input:${stable}`
}

/** JSON with every object's keys sorted, at every depth, so equal inputs give
 *  equal text. Undefined for something that cannot be serialised. */
export function stableStringify(value: unknown): string | undefined {
  const norm = (v: unknown): unknown => {
    if (Array.isArray(v)) return v.map(norm)
    if (v && typeof v === "object") {
      return Object.fromEntries(Object.keys(v as object).sort().map((k) => [k, norm((v as Record<string, unknown>)[k])]))
    }
    return v
  }
  try {
    return JSON.stringify(norm(value))
  } catch {
    return undefined
  }
}

/** A step that failed and was then done again successfully, by the same tool
 *  on the same target (file path for file tools, otherwise identical input), is
 *  a retry. Anything looser would hide a failure: "npm test" failing and "ls"
 *  succeeding are not the same attempt. A retry is a call made AFTER the failed
 *  step's result came back; parallel calls are not retries of each other. The
 *  failure stays visible in the group's detail as "retried". */
export function settleRetries(
  steps: ToolStep[],
  states: ActivityState[],
  /** Position of a part in the turn. Without it nothing is settled. */
  order?: (part: TurnPart) => number | undefined,
): { states: ActivityState[]; retried: number } {
  const settled = [...states]
  let retried = 0
  for (let i = 0; i < steps.length; i++) {
    if (states[i] !== "failed") continue
    const name = toolNameOf(steps[i])
    const target = targetOf(steps[i])
    const failedAt = order && steps[i].result ? order(steps[i].result as TurnPart) : undefined
    if (!name || target === undefined || failedAt === undefined) continue
    for (let j = i + 1; j < steps.length; j++) {
      const retryAt = order?.(steps[j].call)
      if (
        states[j] === "succeeded" && toolNameOf(steps[j]) === name && targetOf(steps[j]) === target &&
        retryAt !== undefined && retryAt > failedAt
      ) {
        settled[i] = "succeeded"
        retried++
        break
      }
    }
  }
  return { states: settled, retried }
}

/** Past tense for a finished group, present tense while a step runs. */
const TOOL_PHRASE: Record<string, { done: string; doing: string }> = {
  Bash: { done: "ran commands", doing: "running commands" },
  Read: { done: "read files", doing: "reading files" },
  Write: { done: "wrote files", doing: "writing files" },
  Edit: { done: "edited files", doing: "editing files" },
  MultiEdit: { done: "edited files", doing: "editing files" },
  Glob: { done: "searched files", doing: "searching files" },
  Grep: { done: "searched files", doing: "searching files" },
  WebFetch: { done: "fetched pages", doing: "fetching pages" },
  WebSearch: { done: "searched the web", doing: "searching the web" },
  Task: { done: "used a sub-agent", doing: "using a sub-agent" },
  TodoWrite: { done: "updated the plan", doing: "updating the plan" },
}

/** A tool without a phrase, named for a person: an MCP tool
 *  (mcp__server__name) reads as just its name. */
export function plainToolName(name: string): string {
  const m = /^mcp__[^_].*?__(.+)$/.exec(name)
  return m ? m[1] : name
}

const MAX_OTHER_TOOLS = 3

/** What the steps did in words a person reads, never raw commands:
 *  "ran commands, read files". Tools without a phrase are listed by name, at
 *  most three, the rest as "+N". */
export function humanToolSummary(steps: ToolStep[]): string {
  const phrases: string[] = []
  const other: string[] = []
  for (const step of steps) {
    const name = toolNameOf(step)
    const phrase = TOOL_PHRASE[name]?.done
    if (phrase) {
      if (!phrases.includes(phrase)) phrases.push(phrase)
    } else if (name) {
      const plain = plainToolName(name)
      if (!other.includes(plain)) other.push(plain)
    }
  }
  const shown = other.slice(0, MAX_OTHER_TOOLS).map((n) => `used ${n}`)
  if (other.length > MAX_OTHER_TOOLS) shown.push(`+${other.length - MAX_OTHER_TOOLS}`)
  return [...phrases, ...shown].join(", ")
}

type Tense = "doing" | "done"
const say = (tense: Tense, doing: string, done: string) => (tense === "doing" ? doing : done)

const basename = (path: string) => path.split(/[\\/]/).filter(Boolean).pop() ?? path
const WORKBOOK = /\.(xlsx|xlsm|xls|ods|csv)$/i

const text = (v: unknown): string | undefined => (typeof v === "string" && v ? v : undefined)

/** A sheet and range as Excel writes it: 'L'!B8:B577. Only string fields are
 *  read: an object never turns into text. */
function sheetRef(params: Record<string, unknown>): string | undefined {
  const where = text(params.range) ?? text(params.cell)
  if (!where) return undefined
  const sheet = text(params.sheet)
  return sheet ? `'${sheet.replace(/'/g, "''")}'!${where}` : where
}

/** Cells in an A1 range ("B8:B577" is 570), or undefined when it is not one. */
function cellsIn(range: string): number | undefined {
  const m = /^\$?([A-Z]{1,3})\$?(\d{1,7})(?::\$?([A-Z]{1,3})\$?(\d{1,7}))?$/i.exec(range.trim())
  if (!m) return undefined
  const col = (s: string) => [...s.toUpperCase()].reduce((n, c) => n * 26 + c.charCodeAt(0) - 64, 0)
  if (!m[3]) return 1
  return (Math.abs(col(m[3]) - col(m[1])) + 1) * (Math.abs(Number(m[4]) - Number(m[2])) + 1)
}

/** How many cells one write command touches, from its own input. */
function cellsOf(command: Record<string, unknown>): number | undefined {
  const params = record(command.params)
  if (command.command === "cell.set") return text(params.cell) ? 1 : undefined
  const range = text(params.range)
  const counted = range ? cellsIn(range) : undefined
  if (counted !== undefined) return counted
  const values = parsed(params.values)
  return Array.isArray(values) ? values.reduce((n: number, row) => n + (Array.isArray(row) ? row.length : 0), 0) || undefined : undefined
}

const cellCount = (n: number) => `${n.toLocaleString("en-US")} ${n === 1 ? "cell" : "cells"}`

/** A typed activity carries its parameters as strings; a JSON one is read as JSON. */
const parsed = (v: unknown): unknown => {
  if (typeof v !== "string" || !/^\s*[[{]/.test(v)) return v
  try { return JSON.parse(v) } catch { return v }
}
const record = (v: unknown): Record<string, unknown> => {
  const value = parsed(v)
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function sheetReadPhrase(input: Record<string, unknown>, tense: Tense): string {
  const query = record(input.query)
  const params = record(query.params)
  const header = text(params.header)
  if (query.command === "range.columnByHeader" && header) {
    return say(tense, `Looking for the column “${header}”`, `Looked for the column “${header}”`)
  }
  if (query.command === "document.inspect") return say(tense, "Looking at the Sheet", "Looked at the Sheet")
  if (query.command === "sheet.read" || query.command === "cell.get") {
    const ref = sheetRef(params)
    if (ref) return say(tense, `Reading ${ref}`, `Read ${ref}`)
    const sheet = text(params.sheet)
    if (sheet) return say(tense, `Reading sheet ${sheet}`, `Read sheet ${sheet}`)
  }
  return say(tense, "Reading the Sheet", "Read the Sheet")
}

const SHEET_WRITES = new Set(["cell.set", "range.setValues", "range.clear"])

/** Where a change lands and how many cells it touches, from the commands. */
function sheetChange(input: Record<string, unknown>) {
  const commands = parsed(input.commands)
  const writes = (Array.isArray(commands) ? commands : []).map(record).filter((c) => SHEET_WRITES.has(String(c.command)))
  const refs = [...new Set(writes.map((c) => sheetRef(record(c.params))).filter((r): r is string => !!r))]
  const counts = writes.map(cellsOf)
  const cells = counts.every((n) => n !== undefined) && counts.length ? counts.reduce((a, b) => a! + b!, 0) : undefined
  return { writes, refs, cells }
}

function sheetApplyPhrase(input: Record<string, unknown>, tense: Tense): string {
  const { writes, refs, cells } = sheetChange(input)
  const many = cells !== undefined && cells > 1 ? ` (${cellCount(cells)})` : ""
  if (refs.length === 1) {
    return writes.every((c) => c.command === "range.clear")
      ? say(tense, `Clearing ${refs[0]}${many}`, `Cleared ${refs[0]}${many}`)
      : say(tense, `Changing ${refs[0]}${many}`, `Changed ${refs[0]}${many}`)
  }
  if (refs.length > 1) return say(tense, `Changing ${refs.length} ranges in the Sheet${many}`, `Changed ${refs.length} ranges in the Sheet${many}`)
  return say(tense, "Changing the Sheet", "Changed the Sheet")
}

function sheetProposePhrase(input: Record<string, unknown>, tense: Tense): string {
  const { refs } = sheetChange(input)
  const where = refs.length === 1 ? refs[0] : "the Sheet"
  return say(tense, `Preparing a proposed change to ${where}`, `Proposed a change to ${where}`)
}

function fileName(input: Record<string, unknown>): string | undefined {
  const path = input.file_path ?? input.path ?? input.notebook_path
  return typeof path === "string" && path ? basename(path) : undefined
}

function hostOf(url: unknown): string | undefined {
  if (typeof url !== "string") return undefined
  try { return new URL(url).host || undefined } catch { return undefined }
}

/** What one step does (or did), in words a person reads: never a raw command
 *  or a full path. A Sheet step names the sheet and the range it touches. */
/** A typed activity's kind, read as the tool it stands for. */
const KIND_TOOL: Record<string, string> = { shell: "Bash", read: "Read", edit: "Edit", write: "Write", search: "Grep", fetch: "WebFetch" }

export function stepPhrase(step: Pick<ToolStep, "call">, tense: Tense): string {
  // A typed activity is named by itself only, never by a legacy content field.
  const a = step.call.activity
  const name = plainToolName(a ? a.name || KIND_TOOL[a.kind] || a.kind || "" : toolNameOf(step))
  const input = record(inputOf(step))
  switch (name) {
    case "Bash": return say(tense, "Running a command", "Ran a command")
    case "Read": {
      const file = fileName(input)
      if (file && WORKBOOK.test(file)) return say(tense, "Reading the workbook", "Read the workbook")
      return file ? say(tense, `Reading ${file}`, `Read ${file}`) : say(tense, "Reading a file", "Read a file")
    }
    case "Write": {
      const file = fileName(input)
      return file ? say(tense, `Writing ${file}`, `Wrote ${file}`) : say(tense, "Writing a file", "Wrote a file")
    }
    case "Edit": case "MultiEdit": case "NotebookEdit": {
      const file = fileName(input)
      return file ? say(tense, `Editing ${file}`, `Edited ${file}`) : say(tense, "Editing a file", "Edited a file")
    }
    case "Glob": case "Grep": return say(tense, "Searching files", "Searched files")
    case "WebFetch": {
      const host = hostOf(input.url)
      return host ? say(tense, `Opening ${host}`, `Opened ${host}`) : say(tense, "Opening a page", "Opened a page")
    }
    case "WebSearch": return say(tense, "Searching the web", "Searched the web")
    case "Task": return say(tense, "Working with a sub-agent", "Worked with a sub-agent")
    case "TodoWrite": return say(tense, "Updating the plan", "Updated the plan")
    case "craft_sheet_read": return sheetReadPhrase(input, tense)
    case "craft_sheet_apply": return sheetApplyPhrase(input, tense)
    case "craft_sheet_propose": return sheetProposePhrase(input, tense)
    case "craft_sheet_acquire": return say(tense, "Taking over editing of the Sheet", "Took over editing of the Sheet")
    case "craft_sheet_renew": return say(tense, "Keeping the Sheet open for editing", "Kept the Sheet open for editing")
    case "craft_sheet_release": return say(tense, "Handing the Sheet back", "Handed the Sheet back")
  }
  if (name.startsWith("craft_sheet_")) return say(tense, "Working on the Sheet", "Worked on the Sheet")
  if (step.call.type === "tool_result" && !name) return say(tense, "Finishing a step", "Finished a step")
  return name ? say(tense, `Using ${name}`, `Used ${name}`) : say(tense, "Working", "Worked on a step")
}

/** The sentence to show while the agent works: the last running step, in the
 *  present tense, as "Reading the workbook…". */
export function currentStepLabel(steps: ToolStep[], states: ActivityState[]): string | undefined {
  for (let i = steps.length - 1; i >= 0; i--) {
    if (states[i] !== "running") continue
    return `${stepPhrase(steps[i], "doing")}…`
  }
  return undefined
}

/** Elapsed time as a person reads it: "41 s", "2 min 5 s". */
export function formatElapsed(ms: number): string {
  const s = Math.max(1, Math.round(ms / 1000))
  if (s < 60) return `${s} s`
  const m = Math.floor(s / 60)
  return s % 60 ? `${m} min ${s % 60} s` : `${m} min`
}

/** How a run ended, as the turn's line names it. Only these override the steps. */
const RUN_END: Partial<Record<string, string>> = { failed: "Failed", cancelled: "Stopped", interrupted: "Interrupted" }

/** The turn's one activity line. Built on summarizeGroup, so it claims success
 *  ("Done") only when every step succeeded (retries settled) and the run itself
 *  did not fail, stop or get interrupted. */
export function turnActivitySummary(
  states: ActivityState[],
  { retried = 0, turnState, streaming = false }: { retried?: number; turnState?: string; streaming?: boolean },
): GroupSummary {
  const group = summarizeGroup(states)
  const detail = [group.detail, retried > 0 ? `${retried} retried` : ""].filter(Boolean).join(", ")
  const ended = !streaming && turnState ? RUN_END[turnState] : undefined
  if (ended) return { tone: "attention", title: ended, detail }
  if (group.tone === "ok") return { tone: "ok", title: "Done", detail }
  return { ...group, detail }
}

const SENSITIVE_KEY_RE = /(?:api[_-]?key|token|secret|password|authorization|auth|cookie|private[_-]?key|credential)/i

/** A tool input with every value under a secret-looking key replaced. */
export function redactSensitiveKeys(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(redactSensitiveKeys)
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([k, v]) => [
        k, SENSITIVE_KEY_RE.test(k) ? "[REDACTED]" : redactSensitiveKeys(v),
      ])
    )
  }
  return value
}
