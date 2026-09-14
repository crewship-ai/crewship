"use client"

import { describeStep } from "@/lib/routine-step-describe"

const kinds: Record<string, string> = {
  agent_run: "Agent task",
  script: "Run a script",
  transform: "Prepare data",
  code: "Evaluate an expression",
  http: "Call a service",
  wait: "Wait for a decision or event",
  foreach: "Process each item",
  call_pipeline: "Run another recipe",
  query: "Read workspace data",
  notify: "Send a notification",
}
function text(value: unknown) {
  return typeof value === "string" ? value : JSON.stringify(value, null, 2)
}
export function RoutineStepDefinition({ step }: { step: Record<string, unknown> | undefined }) {
  if (!step)
    return (
      <p className="text-sm text-muted-foreground">
        Select a step to see its task, inputs and expected result.
      </p>
    )
  const type = String(step.type)
  const config = (step[type] ?? {}) as Record<string, unknown>
  const outcomes = step.outcomes as
    | { required?: boolean; criteria?: { name: string; rule: string }[] }
    | undefined
  const retry = step.retry as { max_attempts?: number } | undefined
  return (
    <div className="space-y-4 text-sm">
      <header>
        <h3 className="font-medium">{describeStep(step, 1).title}</h3>
        <span className="font-mono text-xs text-muted-foreground">{String(step.id)}</span>
        <p className="mt-1 text-xs text-muted-foreground">
          {kinds[type] ?? type}
          {step.agent_slug ? ` · ${step.agent_slug}` : ""}
        </p>
      </header>
      {step.prompt ? (
        <section>
          <h4 className="mb-1 text-xs font-medium text-muted-foreground">Task</h4>
          <p className="whitespace-pre-wrap break-words">{String(step.prompt)}</p>
        </section>
      ) : null}
      {type === "script" && (
        <p className="break-all">
          {String(config.path ?? "No script path")}
          {config.interpreter ? ` · ${config.interpreter}` : ""}
        </p>
      )}
      {type === "http" && (
        <p className="break-all">
          {String(config.method ?? "GET")} {String(config.url ?? "")}
        </p>
      )}
      {type === "call_pipeline" && <p>Recipe: {String(step.pipeline_slug ?? "")}</p>}
      {type === "wait" && <p>{String(config.approval_prompt ?? config.kind ?? "Wait")}</p>}
      {step.inputs ? (
        <section>
          <h4 className="mb-1 text-xs font-medium text-muted-foreground">Inputs</h4>
          <pre className="whitespace-pre-wrap break-words text-xs">{text(step.inputs)}</pre>
        </section>
      ) : null}
      {Array.isArray(step.needs) && step.needs.length > 0 && (
        <p className="text-xs text-muted-foreground">After: {step.needs.join(", ")}</p>
      )}
      {outcomes && (
        <section>
          <h4 className="mb-1 text-xs font-medium">
            {outcomes.required ? "Required result checks" : "Result checks"}
          </h4>
          {outcomes.criteria?.map((c) => (
            <p className="mb-1 text-xs" key={c.name}>
              {c.rule}
            </p>
          ))}
        </section>
      )}
      <p className="text-xs text-muted-foreground">
        {retry?.max_attempts
          ? `Up to ${retry.max_attempts} execution attempts.`
          : "No explicit execution retry policy."}
        {step.on_fail ? ` On failure: ${String(step.on_fail).replaceAll("_", " ")}.` : ""}
      </p>
      {type === "foreach" && Array.isArray(config.steps) && (
        <section>
          <h4 className="mb-2 text-xs font-medium">For each item</h4>
          {config.steps.map((child: Record<string, unknown>) => (
            <details key={String(child.id)} className="mb-2 rounded border p-2">
              <summary className="cursor-pointer">{describeStep(child, 1).title}</summary>
              <div className="pt-3">
                <RoutineStepDefinition step={child} />
              </div>
            </details>
          ))}
        </section>
      )}
      <details>
        <summary className="cursor-pointer text-xs text-muted-foreground">
          Technical configuration
        </summary>
        <pre className="mt-2 overflow-auto whitespace-pre-wrap break-words text-xs">
          {JSON.stringify(step, null, 2)}
        </pre>
      </details>
    </div>
  )
}
