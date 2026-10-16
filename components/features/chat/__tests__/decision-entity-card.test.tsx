import React from "react"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import pending from "@/internal/api/testdata/chat-e2/decision-waitpoint-pending.json"
import rejected from "@/internal/api/testdata/chat-e2/decision-waitpoint-rejected.json"
import { DecisionRefSchema } from "@/lib/chat-e2-cards"
import { DecisionEntityCard } from "../decision-entity-card"
vi.mock("../issue-entity-card", () => ({ EntityCardFrame: ({ children }: { children: React.ReactNode }) => <section>{children}</section> }))
afterEach(cleanup)
it("preserves frozen action identity and allows denial without required approval fields", async () => {
  const onAnswer = vi.fn().mockResolvedValue("saved")
  render(<DecisionEntityCard reference={DecisionRefSchema.parse(pending)} onAnswer={onAnswer} onRecheck={vi.fn()} />)
  fireEvent.click(screen.getByRole("button", { name: "Ship" }))
  expect(screen.getByRole("alert")).toHaveTextContent("Required")
  expect(onAnswer).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "Stop" }))
  await waitFor(() => expect(onAnswer).toHaveBeenCalledTimes(1))
  expect(onAnswer.mock.calls[0][0]).toEqual({ source_id: "source-1", expected_version: "1", action: "answer", action_id: "stop", values: {} })
})
it("retries an uncertain outcome with exactly the captured body and operation key", async () => {
  const onAnswer = vi.fn().mockResolvedValueOnce("unknown").mockResolvedValueOnce("saved")
  render(<DecisionEntityCard reference={DecisionRefSchema.parse(pending)} onAnswer={onAnswer} onRecheck={vi.fn()} />)
  fireEvent.change(screen.getByLabelText(/Run now/), { target: { value: "later" } })
  fireEvent.click(screen.getByRole("button", { name: "Ship" }))
  await screen.findByText("The outcome is unknown. Your response is kept.")
  expect(screen.getByLabelText(/Run now/)).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Retry the same response" }))
  await waitFor(() => expect(onAnswer).toHaveBeenCalledTimes(2))
  expect(onAnswer.mock.calls[1]).toEqual(onAnswer.mock.calls[0])
})
it("renders authoritative terminal receipts without an answer form after reload", () => {
  const onAnswer = vi.fn()
  render(<DecisionEntityCard reference={DecisionRefSchema.parse(rejected)} onAnswer={onAnswer} onRecheck={vi.fn()} />)
  expect(screen.getByRole("status")).toHaveTextContent("rejected")
  expect(screen.queryByRole("button")).toBeNull()
  expect(screen.queryByRole("combobox")).toBeNull()
})
it("makes view-only decisions and rechecking snapshots unanswerable", () => {
  const reference = DecisionRefSchema.parse(pending)
  reference.decision.allowed_actions = ["view"]
  render(<DecisionEntityCard reference={reference} onAnswer={vi.fn()} onRecheck={vi.fn()} rechecking />)
  expect(screen.queryByRole("button")).toBeNull()
  expect(screen.getByLabelText(/Run now/)).toBeDisabled()
})
it("requires explicit review when the source changes while a person is drafting", async () => {
  const onAnswer = vi.fn().mockResolvedValue("saved")
  const reference = DecisionRefSchema.parse(pending)
  const view = render(<DecisionEntityCard reference={reference} onAnswer={onAnswer} onRecheck={vi.fn()} />)
  fireEvent.change(screen.getByLabelText(/Run now/), { target: { value: "later" } })
  const next = structuredClone(reference)
  next.decision.source.version = "2"
  view.rerender(<DecisionEntityCard reference={next} onAnswer={onAnswer} onRecheck={vi.fn()} />)
  expect(screen.getByRole("button", { name: "Ship" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Use reviewed version; keep my response" }))
  fireEvent.click(screen.getByRole("button", { name: "Ship" }))
  await waitFor(() => expect(onAnswer).toHaveBeenCalled())
  expect(onAnswer.mock.calls[0][0]).toMatchObject({ expected_version: "2", values: { timing: "later" } })
})
it("restores an uncertain reply without auto-submitting and retries its captured key", async () => {
  const onAnswer = vi.fn().mockResolvedValue("saved")
  const recovery = { answer: { source_id: "source-1", expected_version: "1", action: "answer" as const, action_id: "ship", values: { timing: "later" } }, key: "restored-key" }
  render(<DecisionEntityCard reference={DecisionRefSchema.parse(pending)} recovery={recovery} onAnswer={onAnswer} onRecheck={vi.fn()} />)
  expect(onAnswer).not.toHaveBeenCalled()
  expect(screen.getByLabelText(/Run now/)).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Retry the same response" }))
  await waitFor(() => expect(onAnswer).toHaveBeenCalledWith(recovery.answer, recovery.key))
})
