import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { WorkResultCard } from "../work-result-card"
import { deriveWorkCardView } from "../work-card-view"
import { workCard } from "../fixtures"
import type { WorkRunState, WorkProblem } from "../types"

afterEach(cleanup)
const ids = (patch: Parameters<typeof workCard>[0]) => deriveWorkCardView(workCard(patch)).actions.map(action => action.id)

it.each<[WorkRunState, string[]]>([
  ["queued", ["cancel"]],
  ["running", ["cancel"]],
  ["waiting", ["cancel"]],
  ["proposal_ready", ["compare"]],
  ["applied", ["open"]],
  ["conflicted", ["open", "compare"]],
  ["failed", ["open", "retry"]],
  ["canceled", ["open", "retry"]],
])("%s offers only its relevant actions", (runState, expected) => {
  expect(ids({ runState, saved: runState === "queued" || runState === "running" || runState === "waiting" || runState === "proposal_ready" ? { kind: "none" } : { kind: "saved", revision: 2 } }).sort()).toEqual([...expected].sort())
})
it("separates run state, saved content and freshness", () => {
  const view = deriveWorkCardView(workCard({ runState: "failed", saved: { kind: "saved", revision: 4 }, freshness: "stale" }))
  expect([view.runLabel, view.contentLabel, view.freshnessLabel]).toEqual(["Failed", "Saved revision 4", "May be out of date"])
  expect(deriveWorkCardView(workCard({ runState: "running", saved: { kind: "none" } })).contentLabel).toBe("Nothing saved yet")
})
it("offers nothing the server did not grant", () => {
  expect(ids({ runState: "running", can: { cancel: false, retry: false, compare: false, open: false } })).toEqual([])
  expect(ids({ runState: "failed", can: { cancel: false, retry: false, compare: false, open: true } })).toEqual(["open"])
})
it.each<[WorkProblem, string, string]>([
  ["revoked", "dismiss", "Access removed"],
  ["disconnected", "reconnect", "Connection lost"],
  ["unknown_result", "check_result", "Result unknown"],
  ["conflict", "compare", "Conflict"],
  ["stopped", "retry", "Run stopped"],
  ["missing_runtime", "change_executor", "No runtime available"],
  ["broken_preview", "retry_preview", "Preview unavailable"],
])("%s has its own state and next action", (problem, action, headline) => {
  const view = deriveWorkCardView(workCard({ problem, runState: problem === "stopped" ? "canceled" : "failed" }))
  expect(view.headline).toBe(headline)
  expect(view.actions.map(item => item.id)).toContain(action)
})
it("does not offer Retry while the result is unknown, and no Open after revoke", () => {
  expect(ids({ problem: "unknown_result", runState: "failed" })).toEqual(["check_result"])
  expect(ids({ problem: "revoked", runState: "applied" })).toEqual(["dismiss"])
  expect(ids({ problem: "disconnected", runState: "running" })).toEqual(["reconnect"])
})
it("shows no private title after revoke or for a restricted link", () => {
  expect(deriveWorkCardView(workCard({ problem: "revoked" })).title).toBe("Restricted item")
  render(<WorkResultCard card={workCard({ access: { status: "restricted" }, runState: "applied" })} onAction={() => {}} />)
  expect(screen.getByLabelText("Restricted item")).toBeTruthy()
  expect(screen.queryByText(/Quarterly|Document|Ada|Content|Freshness/)).toBeNull()
  expect(screen.queryByRole("button", { name: "Open" })).toBeNull()
})
it("disconnected does not read as revoked and keeps the title", () => {
  render(<WorkResultCard card={workCard({ problem: "disconnected", runState: "running" })} onAction={() => {}} />)
  expect(screen.getByText("Quarterly plan")).toBeTruthy()
  expect(screen.getByText(/not a removal of access/)).toBeTruthy()
})
it("reports the action with the card and trigger", () => {
  const onAction = vi.fn()
  const card = workCard()
  render(<WorkResultCard card={card} onAction={onAction} />)
  fireEvent.click(screen.getByRole("button", { name: "Open" }))
  expect(onAction).toHaveBeenCalledWith("open", card, expect.any(HTMLButtonElement))
})
