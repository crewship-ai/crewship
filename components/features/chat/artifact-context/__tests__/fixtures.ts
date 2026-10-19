import type { WorkCardModel } from "./types"

const base: WorkCardModel = {
  id: "work-1", runState: "applied", saved: { kind: "saved", revision: 2 }, freshness: "current", problem: null, load: "ready",
  access: { status: "readable", title: "Quarterly plan", typeLabel: "Document" },
  executor: "Ada", requester: "you",
  can: { cancel: true, retry: true, compare: true, open: true },
}
export const workCard = (patch: Partial<WorkCardModel> = {}): WorkCardModel => ({ ...base, ...patch })
