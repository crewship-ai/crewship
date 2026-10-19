import type { WorkCardAction, WorkCardModel, WorkCardView, WorkProblem, WorkRunState } from "./types"

const RUN_LABEL: Record<WorkRunState, string> = {
  queued: "Queued",
  running: "Running",
  waiting: "Waiting for input",
  proposal_ready: "Proposal ready",
  applied: "Applied",
  conflicted: "Conflicted",
  failed: "Failed",
  canceled: "Canceled",
}

const PROBLEM: Record<WorkProblem, { headline: string; detail: string }> = {
  revoked: { headline: "Access removed", detail: "You no longer have access to this result." },
  disconnected: { headline: "Connection lost", detail: "Live updates stopped. This is not a removal of access; the state shown may be out of date." },
  unknown_result: { headline: "Result unknown", detail: "The server did not confirm whether this work finished. Check the result before running it again." },
  conflict: { headline: "Conflict", detail: "The content changed while the agent worked. Your draft was kept." },
  stopped: { headline: "Run stopped", detail: "The run was stopped. Content that was already saved is unchanged." },
  missing_runtime: { headline: "No runtime available", detail: "The executor has no runtime to run on. Choose another executor." },
  broken_preview: { headline: "Preview unavailable", detail: "The saved content exists but could not be previewed." },
}

const ACTIVE: WorkRunState[] = ["queued", "running", "waiting"]

/** Pure projection of a server card into labels and the actions that make sense for it. */
export function deriveWorkCardView(card: WorkCardModel): WorkCardView {
  const restricted = card.access.status === "restricted"
  const problem = card.problem ? PROBLEM[card.problem] : null
  const actions: WorkCardAction[] = []
  const add = (id: WorkCardAction["id"], label: string) => { if (!actions.some(action => action.id === id)) actions.push({ id, label }) }

  const hasSaved = card.saved.kind === "saved"
  switch (card.problem) {
    case "revoked": add("dismiss", "Remove from view"); break
    case "disconnected": add("reconnect", "Reconnect"); break
    case "unknown_result": add("check_result", "Check result"); break
    case "missing_runtime": add("change_executor", "Choose executor"); break
    case "broken_preview": add("retry_preview", "Retry preview"); break
    case "conflict": if (card.can.compare && !restricted) add("compare", "Compare"); break
    default: break
  }
  const blocked = card.problem === "revoked" || card.problem === "unknown_result" || card.problem === "disconnected"
  if (!blocked) {
    if (!restricted && hasSaved && card.can.open && card.problem !== "broken_preview") add("open", "Open")
    if ((card.runState === "proposal_ready" || card.runState === "conflicted") && card.can.compare && !restricted) add("compare", "Compare")
    if (ACTIVE.includes(card.runState) && card.can.cancel) add("cancel", "Cancel")
    if ((card.runState === "failed" || card.runState === "canceled") && card.can.retry && card.problem !== "missing_runtime") add("retry", "Retry")
  }

  return {
    title: card.access.status === "readable" && card.problem !== "revoked" ? card.access.title : "Restricted item",
    headline: problem?.headline ?? RUN_LABEL[card.runState],
    runLabel: RUN_LABEL[card.runState],
    contentLabel: hasSaved && card.saved.kind === "saved" ? `Saved revision ${card.saved.revision}` : "Nothing saved yet",
    freshnessLabel: card.freshness === "current" ? "Up to date" : card.freshness === "stale" ? "May be out of date" : "Freshness unknown",
    detail: problem?.detail ?? null,
    actions,
  }
}
