import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { IssueWorkPanel } from "../issue-work-panel"
import type { Mission } from "@/lib/types/mission"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("@/hooks/use-issue-people", () => ({ useIssuePeople: () => ({ people: [{ id: "person", name: "Petra" }], error: false }) }))
const issue = { id: "issue", identifier: "ENG-1", workspace_id: "ws", crew_id: "crew", title: "Goal", status: "TODO", work_mode: "agent", work_revision: 3, delegate: { id: "agent", name: "Jordan" }, owner: { id: "owner", name: "Eva" } } as Mission
const agents = [{ id: "agent", name: "Jordan", slug: "jordan" }]
beforeEach(() => fetchMock.mockReset())
describe("Issue work panel", () => {
 it("takes over with the displayed revision and preserves the accountable owner", async () => {
  fetchMock.mockResolvedValue({ ok: true })
  const changed = vi.fn().mockResolvedValue(undefined)
  render(<IssueWorkPanel issue={issue} agents={agents} editable onChanged={changed} />)
  fireEvent.click(screen.getByRole("button", { name: "Take over" }))
  await waitFor(() => expect(changed).toHaveBeenCalledTimes(1))
  const body = JSON.parse(fetchMock.mock.calls[0][1].body)
  expect(body).toMatchObject({ action: "take_over", revision: 3 })
  expect(body.operation_id).toBeTruthy()
  expect(body).not.toHaveProperty("owner_user_id")
  expect(screen.getByText("Accountable owner: Eva")).toBeInTheDocument()
 })
 it("hands off to a person with a required, retained brief when the revision conflicts", async () => {
  fetchMock.mockResolvedValue({ ok: false, status: 409, json: async () => ({ detail: "Work changed. Refresh before transferring." }) })
  const changed = vi.fn().mockResolvedValue(undefined)
  render(<IssueWorkPanel issue={issue} agents={agents} editable onChanged={changed} />)
  fireEvent.click(screen.getByRole("button", { name: "Hand off" }))
  fireEvent.change(screen.getByLabelText("Next worker"), { target: { value: "user:person" } })
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Please verify the attached report" } })
  fireEvent.click(screen.getByRole("button", { name: "Hand off work" }))
  await screen.findByRole("alert")
  expect(changed).toHaveBeenCalledTimes(1)
  expect(screen.getByRole("textbox")).toHaveValue("Please verify the attached report")
  expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toMatchObject({ action: "handoff_human", target_id: "person", note: "Please verify the attached report" })
 })
 it("blocks handback and submission until the previous agents have stopped", () => {
  render(<IssueWorkPanel issue={{ ...issue, work_mode: "human", worker_name: "Petra", work_stopping: true }} agents={agents} editable onChanged={async () => {}} />)
  expect(screen.getByRole("button", { name: "Hand off" })).toBeDisabled()
  expect(screen.getByRole("button", { name: "Submit result" })).toBeDisabled()
  expect(screen.queryByRole("button", { name: "Take over" })).not.toBeInTheDocument()
 })
 it("keeps a read-only viewer away from mutation controls", () => {
  render(<IssueWorkPanel issue={issue} agents={agents} editable={false} onChanged={async () => {}} />)
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
 })
})
