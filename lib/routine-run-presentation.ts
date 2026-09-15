/** Recorded stop/failure takes precedence over business outcome, then active
 * execution, then the outcome of completed work. Match the explanation below;
 * a retained partial result must not make a cancelled run look active/successful.
 * Never infer success or failure from a primitive output such as false or zero. */
export function routineRunPresentation(run: { status?: string; outcome?: string }) {
  const status = (run.status ?? "").toLowerCase()
  if (["cancelled", "canceled"].includes(status) || run.outcome === "CANCELLED")
    return { label: "Stopped", tone: "default" as const }
  if (["failed", "error", "interrupted"].includes(status))
    return {
      label: status === "interrupted" ? "Interrupted" : "Failed",
      tone: "destructive" as const,
    }
  if (run.outcome === "FAILED") return { label: "Result failed", tone: "destructive" as const }
  if (["waiting", "paused"].includes(status)) return { label: "Waiting", tone: "warn" as const }
  if (["running", "queued"].includes(status))
    return { label: status === "running" ? "Running" : "Queued", tone: "blue" as const }
  if (run.outcome === "NEEDS_HUMAN") return { label: "Needs your attention", tone: "warn" as const }
  if (run.outcome === "PARTIAL") return { label: "Partially completed", tone: "warn" as const }
  if (run.outcome === "NO_CHANGE") return { label: "No change needed", tone: "success" as const }
  if (run.outcome === "WORK_CREATED")
    return { label: "Follow-up work created", tone: "blue" as const }
  if (["completed", "success", "succeeded"].includes(status))
    return { label: "Completed", tone: "success" as const }
  return {
    label: status === "cancelled" ? "Stopped" : status || "Recorded",
    tone: "default" as const,
  }
}

/** The lead-in above the recorded reason must agree with the verdict beside
 * it. A cancelled run already reads "Run stopped" in the header and "Stopped"
 * in the pill, so calling the same step a failed one contradicts the screen
 * twice over — and a cancellation is exactly the case where the reader most
 * needs to know the step did not fail on the merits. */
export function routineStoppingPointLabel(run: { status?: string; outcome?: string }): string {
  const status = (run.status ?? "").toLowerCase()
  if (["cancelled", "canceled"].includes(status) || run.outcome === "CANCELLED")
    return "Stopped at step"
  if (status === "interrupted") return "Interrupted at step"
  return "Failed step"
}

/** Only the saved author's mapping may give a primitive result business meaning. */
export function routineResultLabel(output: string, definition: unknown): string | null {
  if (!definition || typeof definition !== "object") return null
  const specs = (definition as { outputs?: unknown }).outputs
  if (!Array.isArray(specs) || specs.length !== 1) return null
  const spec = specs[0]
  if (
    !spec ||
    typeof spec !== "object" ||
    !spec.value_labels ||
    typeof spec.value_labels !== "object"
  )
    return null
  let value: unknown
  try {
    value = JSON.parse(output)
  } catch {
    value = output
  }
  if (value !== null && typeof value === "object" && typeof spec.name === "string")
    value = (value as Record<string, unknown>)[spec.name]
  if (!["string", "number", "boolean"].includes(typeof value)) return null
  const key = String(value)
  return Object.hasOwn(spec.value_labels, key) && typeof spec.value_labels[key] === "string"
    ? spec.value_labels[key]
    : null
}

/** Explanations use recorded state; never infer success from the existence of text. */
export function routineRunExplanation(
  run: {
    status?: string
    outcome?: string
    error_message?: string
    output?: string
    current_step_id?: string
  },
  waitKind?: string,
) {
  const status = (run.status ?? "").toLowerCase()
  if (["cancelled", "canceled"].includes(status) || run.outcome === "CANCELLED")
    return {
      title: "Run stopped",
      detail:
        "Pending work was cancelled. Recorded results remain available. Stopping does not undo actions that already happened.",
    }
  if (run.outcome === "FAILED" && run.error_message?.trim() === "no outcome reported")
    return {
      title: run.output
        ? "A result was recorded, but completion was not confirmed"
        : "Completion was not confirmed",
      detail:
        "This run remains failed because its required completion signal is missing. Inspect the agent handoff before starting another run. Any recorded work remains available below.",
    }
  if (["failed", "error", "interrupted"].includes(status) || run.outcome === "FAILED")
    return {
      title: status === "interrupted" ? "Run interrupted" : "This run could not finish",
      detail:
        "Review the recorded error and any retained results before starting another run. A new run repeats work; it does not resume this attempt.",
    }
  if (["waiting", "paused"].includes(status) || (status === "running" && !!waitKind)) {
    if (waitKind === "approval")
      return {
        title: "A review is needed",
        detail:
          "Read the request and recorded result before deciding. This is the same decision shown in Inbox.",
      }
    if (waitKind === "event")
      return {
        title: "Waiting for an event",
        detail:
          "The recipe is parked until its configured event arrives. This is not a request for human approval.",
      }
    if (waitKind === "datetime")
      return {
        title: "Waiting until a scheduled time",
        detail:
          "The recipe is parked at a date waitpoint. See the saved step for its configured time.",
      }
    return {
      title: "This run is waiting",
      detail:
        "No human decision has been confirmed here. Check the recorded step and Activity for the reason.",
    }
  }
  if (status === "queued")
    return {
      title: "Waiting to start",
      detail:
        "The run is queued. No active execution is confirmed yet; Activity shows any recorded scheduling or capacity events.",
    }
  if (status === "running")
    return {
      title: "Work is in progress",
      detail: run.current_step_id
        ? `Current step: ${run.current_step_id.replaceAll("_", " ")}. Results below are the work recorded so far.`
        : "The run is active. Results below are the work recorded so far.",
    }
  if (run.outcome === "PARTIAL")
    return {
      title: "Part of the work is complete",
      detail:
        "The recipe reported partial completion. Review the result and outstanding work before relying on it.",
    }
  if (run.outcome === "NO_CHANGE")
    return {
      title: "No change was needed",
      detail:
        "The recipe explicitly reported that no change was needed. See its recorded explanation below.",
    }
  if (run.outcome === "WORK_CREATED")
    return {
      title: "Follow-up work was created",
      detail:
        "The recipe reported that it created further work. That work keeps its own review and completion process.",
    }
  if (run.outcome === "NEEDS_HUMAN")
    return {
      title: "Your attention is needed",
      detail:
        "Review the recorded result. This outcome alone is not an approval request; any available decision is shown separately.",
    }
  return {
    title: routineRunPresentation(run).label,
    detail:
      "Review the recorded result and files below. Completion does not independently prove every factual claim in an agent response.",
  }
}

/** "just now" · "4 min ago" · "2 h ago" · "3 d ago" — minute resolution, so
 * a line that re-renders once a minute is always right and never ticks. */
export function formatAgo(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return ""
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ""
  const minutes = Math.floor(Math.max(0, now - t) / 60_000)
  if (minutes < 1) return "just now"
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} h ago`
  return `${Math.floor(hours / 24)} d ago`
}

/** "23 h 56 min" · "45 min" · "2 d 3 h" · "" once the moment has passed. */
export function formatUntil(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return ""
  const t = new Date(iso).getTime()
  if (Number.isNaN(t) || t <= now) return ""
  const minutes = Math.ceil((t - now) / 60_000)
  if (minutes < 60) return `${minutes} min`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return minutes % 60 ? `${hours} h ${minutes % 60} min` : `${hours} h`
  const days = Math.floor(hours / 24)
  return hours % 24 ? `${days} d ${hours % 24} h` : `${days} d`
}

/** The server's classification of a run that could not finish (contract
 * "Run detail"). Absent on older servers and on runs that ended well. */
export interface RunFailure {
  kind: string
  step_id: string
  step_name: string
  summary: string
  kept_step_ids?: string[]
  not_done_step_ids?: string[]
}

export interface RunBannerStep {
  id: string
  name?: string
  type?: string
  if?: unknown
  notify?: { to?: string }
}

export interface RunBannerInput {
  run: {
    status?: string
    outcome?: string
    error_message?: string
    output?: string
    current_step_id?: string
    failed_at_step?: string
    failure?: RunFailure | null
    step_outputs?: Record<string, unknown> | null
    step_outputs_available?: boolean
  }
  /** Top-level steps of the executed recipe, in recipe order. */
  steps?: RunBannerStep[]
  /** The wait the run is parked on, when active. */
  waitKind?: string
  waiting?: { who?: string; why?: string; expiresAt?: string }
  /** The declared result label, when the recipe maps one. */
  resultLabel?: string | null
  now?: number
}

export interface RunBanner {
  tone: "warn" | "blue" | "destructive" | "success" | "default"
  title: string
  detail: string
  /** Failed runs only: what already happened and what never ran. */
  kept?: string
  notDone?: string
}

const stepTitle = (steps: RunBannerStep[] | undefined, id: string) =>
  steps?.find((s) => s.id === id)?.name?.split(/\r?\n/)[0]?.trim() || id

/** The one banner under the run header: one state, one sentence, in the
 * reader's words. Everything is derived from recorded state; the raw error
 * stays in Technical details. */
export function routineRunBanner(input: RunBannerInput): RunBanner {
  const { run, steps, waitKind, waiting, resultLabel } = input
  const now = input.now ?? Date.now()
  const status = (run.status ?? "").toLowerCase()
  const presentation = routineRunPresentation(run)
  const active = ["queued", "running", "waiting", "paused"].includes(status)
  const explanation = routineRunExplanation(run, active ? waitKind : undefined)

  if (["cancelled", "canceled"].includes(status) || run.outcome === "CANCELLED")
    return { tone: "default", ...explanation }

  if (active && (["waiting", "paused"].includes(status) || waitKind)) {
    if (waitKind === "approval") {
      const expires = formatUntil(waiting?.expiresAt, now)
      return {
        tone: "warn",
        title: `${waiting?.who || "A person"} needs to decide`,
        detail: [
          waiting?.why?.trim(),
          "Nothing after this step has happened yet.",
          "This is the same decision shown in Inbox.",
          expires ? `Expires in ${expires}.` : "",
        ]
          .filter(Boolean)
          .join(" "),
      }
    }
    return { tone: "warn", ...explanation }
  }

  if (status === "running") {
    const total = steps?.length ?? 0
    const index = steps?.findIndex((s) => s.id === run.current_step_id) ?? -1
    const where =
      index >= 0 && total
        ? ` · step ${index + 1} of ${total}, “${stepTitle(steps, run.current_step_id ?? "")}”`
        : ""
    return {
      tone: "blue",
      title: `Work in progress${where}`,
      detail:
        "Results below are the work recorded so far. Stopping does not undo what already happened.",
    }
  }
  if (status === "queued") return { tone: "blue", ...explanation }

  if (presentation.tone === "destructive") {
    const failure = run.failure
    if (failure && failure.summary) {
      const index = steps?.findIndex((s) => s.id === failure.step_id) ?? -1
      const name = failure.step_name || stepTitle(steps, failure.step_id)
      const names = (ids: string[] | undefined) =>
        ids?.length ? ids.map((id) => stepTitle(steps, id)).join(", ") : ""
      return {
        tone: "destructive",
        title: index >= 0 ? `Stopped at step ${index + 1}, “${name}”` : `Stopped at “${name}”`,
        detail: failure.summary,
        kept: names(failure.kept_step_ids) || "nothing was recorded before this step",
        notDone: names(failure.not_done_step_ids) || "nothing — this was the last step",
      }
    }
    return { tone: "destructive", ...explanation }
  }

  if (presentation.tone === "success" && status === "completed") {
    const outputs = run.step_outputs ?? {}
    const recorded = run.step_outputs_available !== false && !!run.step_outputs
    const skipped = recorded
      ? (steps ?? []).filter((s) => s.if != null && s.if !== "" && !Object.hasOwn(outputs, s.id))
      : []
    const notified = recorded
      ? (steps ?? []).filter((s) => s.type === "notify" && Object.hasOwn(outputs, s.id))
      : []
    const parts = [
      skipped.length
        ? `Skipped: ${skipped.map((s) => stepTitle(steps, s.id)).join(", ")} (its condition was not met).`
        : "",
      notified.length
        ? `Notified ${notified.map((s) => s.notify?.to || stepTitle(steps, s.id)).join(", ")}.`
        : "",
    ].filter(Boolean)
    return {
      tone: "success",
      title: `Done · ${resultLabel || presentation.label}`,
      detail: parts.length ? parts.join(" ") : explanation.detail,
    }
  }

  return {
    tone: presentation.tone === "warn" ? "warn" : presentation.tone === "blue" ? "blue" : "default",
    ...explanation,
  }
}
