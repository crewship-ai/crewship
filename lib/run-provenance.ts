// How a run started, said correctly.
//
// The trap this module exists for: `triggered_via` cannot distinguish an
// automation from a cron schedule. Every DEFERRED run is fired by the same
// dispatcher (internal/pipeline/pending_dispatcher.go) and stored with
// triggered_via="schedule", automations included. A UI that prints the enum
// verbatim tells the reader a schedule did something a rule did — and on a
// page whose whole job is "why did this run", that is the one error that
// matters.
//
// The rule's identity survives in the run's metadata, which the API lifts onto
// the row as automation_name. Presence of that name is the test.
//
// This is the same decision cmd/crewship/cmd_routine_records.go makes for its
// TRIGGER column, deliberately: the CLI and the dashboard must not disagree
// about the same run.

/** The subset of a run record this module needs. */
export interface RunProvenanceFields {
  triggered_via?: string
  triggered_by_id?: string
  automation_name?: string
  chain_depth?: number
}

export interface RunProvenance {
  /** How it started: "automation", "schedule", "manual", "issue", … */
  label: string
  /** Which rule / schedule / issue, when the row names one. */
  source?: string
  /**
   * Composed hops from a human action, present only when > 0. A run somebody
   * started is not "depth 0" to a reader — it is just a run, and saying
   * otherwise puts chain chrome on every row in the workspace.
   */
  chainDepth?: number
}

export function runProvenance(run: RunProvenanceFields): RunProvenance {
  const automated = !!run.automation_name
  const depth = run.chain_depth
  return {
    label: automated ? "automation" : run.triggered_via || "manual",
    source: automated ? run.automation_name : run.triggered_by_id || undefined,
    chainDepth: typeof depth === "number" && depth > 0 ? depth : undefined,
  }
}

/** True when this run was composed rather than started directly. */
export function isComposedRun(run: RunProvenanceFields): boolean {
  return runProvenance(run).chainDepth !== undefined
}
