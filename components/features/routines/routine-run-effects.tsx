import { routineEffects } from "@/lib/routine-effects"

export function RoutineRunEffects({
  definition,
}: {
  definition?: Record<string, unknown> | null
}) {
  const effects = routineEffects(definition)
  return (
    <section aria-label="Run effects" className="space-y-2 rounded-xl border p-3 text-sm">
      <h3 className="font-medium">What this run can do</h3>
      {!definition ? (
        <p>
          Effect details are unavailable. This is a real run and can repeat external
          actions.
        </p>
      ) : (
        <>
          {!!effects.agents.length && (
            <p>
              Agents: {effects.agents.join(", ")}. Agent work can use tools, change
              connected systems and incur costs.
            </p>
          )}
          {effects.http && <p>HTTP requests can send data and change remote systems.</p>}
          {!!effects.hosts.length && <p>Declared hosts: {effects.hosts.join(", ")}</p>}
          {!!effects.credentials.length && (
            <p>
              Credential types: {effects.credentials.join(", ")}. Values are resolved at
              runtime.
            </p>
          )}
          {effects.indirect && (
            <p>
              Scripts, tools, notifications or called routines can perform additional
              actions.
            </p>
          )}
          {!effects.agents.length && !effects.http && !effects.indirect && (
            <p>No agent, HTTP or code actions were found in this recipe’s steps.</p>
          )}
          <p className="text-xs text-muted-foreground">
            This summary covers the recipe, not actions chosen dynamically by agents or
            called routines. Cancelling does not undo completed effects.
          </p>
        </>
      )}
    </section>
  )
}
