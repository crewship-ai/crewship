import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { WorkResultCard } from "../work-result-card"
import { deriveWorkCardView } from "../work-card-view"
import { workCard } from "./fixtures"
import type { WorkRunState, WorkProblem } from "../types"

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })
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
it("stopped has its own action regardless of run state, and only when granted", () => {
  expect(ids({ problem: "stopped", runState: "applied" })).toEqual(["retry", "open"])
  expect(ids({ problem: "stopped", runState: "applied", can: { cancel: false, retry: false, compare: false, open: true } })).toEqual(["open"])
})
it("exact action sets for conflict, broken preview and missing runtime", () => {
  expect(ids({ problem: "conflict", runState: "conflicted" })).toEqual(["open", "compare"])
  expect(ids({ problem: "broken_preview", runState: "applied" })).toEqual(["retry_preview"])
  expect(ids({ problem: "missing_runtime", runState: "failed" })).toEqual(["change_executor", "open"])
})
it("keeps Open for a confirmed revision while disconnected or unknown, but never Cancel or Retry", () => {
  expect(ids({ problem: "disconnected", runState: "running" })).toEqual(["reconnect", "open"])
  expect(ids({ problem: "unknown_result", runState: "failed" })).toEqual(["check_result", "open"])
})
it("temporary unavailability is distinct from revoked: keeps the title, offers Try again", () => {
  const view = deriveWorkCardView(workCard({ load: "unavailable" }))
  expect(view.headline).toBe("Temporarily unavailable")
  expect(view.title).toBe("Quarterly plan")
  expect(view.actions.map(a => a.id)).toEqual(["refresh", "open"])
})
it("an auth denial drops cached content like a revoke; loading shows no state or actions", () => {
  const denied = deriveWorkCardView(workCard({ load: "denied" }))
  expect([denied.restricted, denied.title, denied.actions.map(a => a.id)]).toEqual([true, "Restricted item", ["dismiss"]])
  const loading = deriveWorkCardView(workCard({ load: "loading" }))
  expect([loading.headline, loading.actions]).toEqual(["Loading", []])
  render(<WorkResultCard card={workCard({ load: "loading" })} onAction={() => {}} />)
  expect(screen.getByRole("article").getAttribute("aria-busy")).toBe("true")
})
it("restriction comes from the model: a readable item named Restricted item is still readable", () => {
  const view = deriveWorkCardView(workCard({ access: { status: "readable", title: "Restricted item", typeLabel: "Document" } }))
  expect(view.restricted).toBe(false)
  render(<WorkResultCard card={workCard({ access: { status: "readable", title: "Restricted item", typeLabel: "Document" } })} onAction={() => {}} />)
  expect(screen.getByText("Document · Ada · On behalf of you")).toBeTruthy()
})
it("only changing cards are live regions", () => {
  render(<><WorkResultCard card={workCard({ runState: "applied" })} onAction={() => {}} /><WorkResultCard card={workCard({ runState: "running", saved: { kind: "none" } })} onAction={() => {}} /></>)
  expect(screen.getAllByRole("status")).toHaveLength(1)
})
it("renders nothing when the feature flag is off", () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "")
  const { container } = render(<WorkResultCard card={workCard()} onAction={() => {}} />)
  expect(container.innerHTML).toBe("")
})
it("does not offer Retry while the result is unknown, and no Open after revoke", () => {
  expect(ids({ problem: "unknown_result", runState: "failed" })).toEqual(["check_result", "open"])
  expect(ids({ problem: "revoked", runState: "applied" })).toEqual(["dismiss"])
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

it("a restricted card also masks the problem state and run state attributes", () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true")
  const { container } = render(<WorkResultCard card={workCard({ access: { status: "restricted" }, problem: "missing_runtime", runState: "failed" })} onAction={() => {}} />)
  const card = container.querySelector("article")!
  expect(card.hasAttribute("data-problem")).toBe(false)
  expect(card.hasAttribute("data-run-state")).toBe(false)
  expect(card.textContent).not.toMatch(/executor|runtime/i)
})
