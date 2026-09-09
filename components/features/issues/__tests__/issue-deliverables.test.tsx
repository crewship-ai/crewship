import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { IssueFilesCard } from "../issue-files-card"
import { ProjectMilestonesCard } from "../project-milestones-card"
import type { Mission } from "@/lib/types/mission"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
const issue = { id: "issue", identifier: "ENG-1", workspace_id: "ws", crew_id: "crew" } as Mission
beforeEach(() => fetchMock.mockReset())

describe("Issue deliverables", () => {
  it("uploads the actual file to the issue and reads back the saved deliverable", async () => {
    fetchMock.mockResolvedValueOnce({ ok: true, json: async () => [] })
      .mockResolvedValueOnce({ ok: true })
      .mockResolvedValueOnce({ ok: true, json: async () => [{ id: "f1", filename: "report.txt", size_bytes: 12 }] })
    render(<IssueFilesCard issue={issue} editable />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const file = new File(["Checked"], "report.txt", { type: "text/plain" })
    fireEvent.change(screen.getByLabelText("Attach a file"), { target: { files: [file] } })
    expect(await screen.findByRole("button", { name: "report.txt" })).toBeInTheDocument()
    const [url, request] = fetchMock.mock.calls[1]
    expect(url).toBe("/api/v1/crews/crew/issues/ENG-1/attachments?workspace_id=ws")
    expect(request.method).toBe("POST")
    expect(request.body.get("file").name).toBe("report.txt")
  })

  it("shows a rejected upload and does not claim that the file was saved", async () => {
    fetchMock.mockResolvedValueOnce({ ok: true, json: async () => [] })
      .mockResolvedValueOnce({ ok: false, json: async () => ({ error: "File exceeds the upload limit" }) })
    render(<IssueFilesCard issue={issue} editable />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    fireEvent.change(screen.getByLabelText("Attach a file"), { target: { files: [new File(["data"], "large.txt")] } })
    expect(await screen.findByRole("alert")).toHaveTextContent("File exceeds the upload limit")
    expect(screen.queryByRole("button", { name: "large.txt" })).not.toBeInTheDocument()
  })

  it("creates a milestone on the existing project with its chosen target date", async () => {
    fetchMock.mockResolvedValueOnce({ ok: true, json: async () => [] })
      .mockResolvedValueOnce({ ok: true })
      .mockResolvedValueOnce({ ok: true, json: async () => [{ id: "m1", name: "Client review", target_date: "2026-09-30", done_count: 0, issue_count: 2 }] })
    render(<ProjectMilestonesCard projectId="project" workspaceId="ws" editable />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    fireEvent.change(screen.getByLabelText("Milestone name"), { target: { value: "Client review" } })
    fireEvent.change(screen.getByLabelText("Milestone target date"), { target: { value: "2026-09-30" } })
    fireEvent.click(screen.getByRole("button", { name: "Add milestone" }))
    expect(await screen.findByText("Client review")).toBeInTheDocument()
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/projects/project/milestones?workspace_id=ws")
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ name: "Client review", target_date: "2026-09-30" })
  })

  it("allows a viewer to read deliverables and milestones without mutation controls", async () => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => [] })
    render(<><IssueFilesCard issue={issue} editable={false} /><ProjectMilestonesCard projectId="project" workspaceId="ws" editable={false} /></>)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(screen.queryByLabelText("Attach a file")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Add milestone" })).not.toBeInTheDocument()
  })
})

it("keeps the uploaded deliverable when an older initial load finishes later", async () => {
  let completeOld!: (value: unknown) => void
  fetchMock.mockReturnValueOnce(new Promise((resolve) => { completeOld = resolve }))
    .mockResolvedValueOnce({ ok: true })
    .mockResolvedValueOnce({ ok: true, json: async () => [{ id: "new", filename: "verified.txt", size_bytes: 5 }] })
  render(<IssueFilesCard issue={issue} editable />)
  fireEvent.change(screen.getByLabelText("Attach a file"), { target: { files: [new File(["check"], "verified.txt")] } })
  await screen.findByRole("button", { name: "verified.txt" })
  await act(async () => completeOld({ ok: true, json: async () => [] }))
  expect(screen.getByRole("button", { name: "verified.txt" })).toBeInTheDocument()
})
