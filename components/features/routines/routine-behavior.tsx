import type { RoutineBehavior, RoutineStepBehavior } from "@/lib/routine-behavior"

export function RoutineBehaviorSummary({ behavior }: { behavior?: RoutineBehavior }) {
  if (!behavior) return null
  return (
    <details className="rounded-xl border border-border/60 bg-card px-4 py-3 text-xs">
      <summary className="cursor-pointer font-medium">
        How this routine works · checks and recovery
      </summary>
      <div className="mt-3 space-y-3">
        <p className="text-muted-foreground">{behavior.scope}</p>
        <p>{behavior.cost}</p>
        <ul className="space-y-3">
          {behavior.steps.map((step) => (
            <li key={step.id} className="min-w-0 break-words border-t border-border/50 pt-3">
              <p className="font-medium">
                {step.name} · {step.performer}
              </p>
              <RoutineStepChecks behavior={step} />
            </li>
          ))}
        </ul>
      </div>
    </details>
  )
}

export function RoutineStepChecks({ behavior }: { behavior?: RoutineStepBehavior }) {
  if (!behavior) return null
  return (
    <div className="mt-2 space-y-2 text-xs" data-testid="routine-step-checks">
      <p className="font-medium">Configured checks</p>
      <ul className="list-disc space-y-1 pl-4 text-muted-foreground">
        {behavior.checks.map((check, index) => (
          <li key={index}>{check}</li>
        ))}
      </ul>
      <p>
        <span className="font-medium">On a problem: </span>
        {behavior.failure}
      </p>
      <p className="text-muted-foreground">
        {behavior.attempts} {behavior.timeout}
      </p>
    </div>
  )
}
