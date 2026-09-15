import { routineEffects } from "@/lib/routine-effects"
import { describeStep, isRecord } from "@/lib/routine-step-describe"

// What a run will do, in one sentence, derived from the recipe's step types
// (lib/routine-effects.ts + the step list). Nothing here is a promise about
// what an agent chooses at run time; the closing clause says so.

const stepsOf = (definition: Record<string, unknown> | null | undefined) =>
  Array.isArray(definition?.steps) ? definition.steps.filter(isRecord) : []

/** Facts about a run before it starts — the chip row above the inputs. Each
 * one is omitted when the recipe does not declare it; a zero is never shown. */
export interface RunFacts {
  crew?: string
  /** "Finance approvers decide above the limit" — from the wait steps. */
  decides?: string
  /** "≈ $0.05" */
  cost?: string
  /** "≈ 2 min" */
  duration?: string
}

function firstLine(value: unknown): string {
  return typeof value === "string" ? value.split(/\r?\n/)[0].trim() : ""
}

function approvalWord(step: Record<string, unknown>): string {
  const wait = isRecord(step.wait) ? step.wait : null
  // The DSL names no approver (WaitStep has kind, approval_prompt and
  // approval_title); the title is the closest thing to "who".
  const approvers = wait?.approvers ?? wait?.approval_title
  if (Array.isArray(approvers) && approvers.length)
    return approvers.filter((a) => typeof a === "string").join(", ")
  if (typeof approvers === "string" && approvers.trim()) return firstLine(approvers)
  return "A person"
}

function durationWord(seconds: number): string {
  if (seconds < 60) return `≈ ${Math.round(seconds)} s`
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `≈ ${minutes} min`
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  return rest ? `≈ ${hours} h ${rest} min` : `≈ ${hours} h`
}

export function routineRunFacts(
  definition: Record<string, unknown> | null | undefined,
  crewName?: string | null,
): RunFacts {
  const facts: RunFacts = {}
  if (crewName) facts.crew = crewName
  const waits = stepsOf(definition ?? null).filter(
    (s) => s.type === "wait" && (!isRecord(s.wait) || !s.wait.kind || s.wait.kind === "approval"),
  )
  if (waits.length) {
    const who = approvalWord(waits[0])
    const when = firstLine(waits[0].if)
    facts.decides = when ? `${who} decides when ${when}` : `${who} decides before it continues`
  }
  const cost = definition?.estimated_cost_usd
  if (typeof cost === "number" && cost > 0)
    facts.cost = `≈ $${cost < 0.01 ? cost.toFixed(4) : cost.toFixed(2)}`
  const seconds = definition?.estimated_duration_seconds
  if (typeof seconds === "number" && seconds > 0) facts.duration = durationWord(seconds)
  return facts
}

/** "This run will: read the invoice with nora, check it, ask a person, write
 * to erp.example.com, run a script and notify #finance." One clause per kind
 * of step, in recipe order, never more than one clause per kind. */
export function routineRunSentence(
  definition: Record<string, unknown> | null | undefined,
): string | null {
  if (!definition) return null
  const steps = stepsOf(definition)
  const effects = routineEffects(definition)
  const clauses: string[] = []
  const seen = new Set<string>()
  const add = (kind: string, clause: string) => {
    if (seen.has(kind)) return
    seen.add(kind)
    clauses.push(clause)
  }
  const walk = (list: Record<string, unknown>[]) => {
    for (const step of list) {
      const kind = String(step.type ?? "")
      const title = describeStep(step, 1).title
      switch (kind) {
        case "agent_run":
          add(
            "agent",
            effects.agents.length
              ? `ask ${effects.agents.join(" and ")} to do the agent steps`
              : "ask an agent to do the agent steps",
          )
          break
        case "wait": {
          const wait = isRecord(step.wait) ? step.wait : null
          if (!wait?.kind || wait.kind === "approval")
            add("approval", `stop and ask ${approvalWord(step)} to decide`)
          else if (wait.kind === "datetime") add("datetime", "wait until a scheduled time")
          else add("event", "wait for an event")
          break
        }
        case "http":
          add(
            "http",
            effects.hosts.length
              ? `call ${effects.hosts.join(", ")}`
              : "call a service over HTTP",
          )
          break
        case "script":
        case "code":
          add("script", "run a script on the crew's share")
          break
        case "notify": {
          const to = isRecord(step.notify) ? firstLine(step.notify.to) : ""
          add("notify", to ? `notify ${to}` : "send a notification")
          break
        }
        case "call":
        case "call_pipeline":
          add("call", "start another routine")
          break
        case "crewship":
          add("crewship", "change something in this workspace")
          break
        case "query":
          add("query", "read stored data")
          break
        case "foreach":
          if (isRecord(step.foreach) && Array.isArray(step.foreach.steps))
            walk(step.foreach.steps.filter(isRecord))
          break
        case "transform":
          break
        default:
          if (title) add(`other:${kind}`, title.toLowerCase())
      }
    }
  }
  walk(steps)
  if (!clauses.length) return null
  const list =
    clauses.length === 1
      ? clauses[0]
      : `${clauses.slice(0, -1).join(", ")} and ${clauses[clauses.length - 1]}`
  const creds = effects.credentials.length
    ? ` It uses ${effects.credentials.join(", ")} from the vault.`
    : ""
  return `${list}.${creds}`
}

export function RoutineRunEffects({
  definition,
}: {
  definition?: Record<string, unknown> | null
}) {
  const sentence = routineRunSentence(definition)
  return (
    <section
      aria-label="Run effects"
      className="border-t border-border/60 pt-3 text-xs text-muted-foreground"
    >
      {!definition ? (
        <p>
          <span className="font-medium text-foreground">This run will:</span> do what the
          recipe says — its details are unavailable here. This is a real run and can repeat
          external actions. Stopping does not undo what already happened.
        </p>
      ) : (
        <p>
          <span className="font-medium text-foreground">This run will:</span>{" "}
          {sentence ?? "record a result without agents, calls or scripts."} Stopping does not
          undo what already happened.
        </p>
      )}
    </section>
  )
}
