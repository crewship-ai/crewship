import { beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetch }))
import { RoutinePresetForm } from "../routine-preset-form"

const definition = {
  inputs: [
    { name: "count", type: "integer", label: "Count", required: true, default: 7 },
    { name: "enabled", type: "boolean", label: "Enabled", default: true },
    { name: "region", type: "string", label: "Region", options: ["EU", "US"], default: "EU" },
  ],
}
beforeEach(() => {
  cleanup()
  h.fetch.mockReset()
  h.fetch.mockResolvedValue({ ok: true, json: async () => ({ definition }) })
})
describe("recurring preset inputs", () => {
  it("uses the shared start conversion and preserves zero, false, and legacy keys", async () => {
    const save = vi.fn()
    render(
      <RoutinePresetForm
        workspaceId="ws"
        slug="daily"
        initialInputs={{ count: 0, enabled: false, opaque: { id: "legacy-private-value" } }}
        onCancel={vi.fn()}
        onSave={save}
      />,
    )
    await screen.findByRole("button", { name: "Save inputs" })
    expect(document.body.textContent).not.toContain("legacy-private-value")
    fireEvent.click(screen.getByRole("button", { name: "Save inputs" }))
    expect(save).toHaveBeenCalledWith({
      count: 0,
      enabled: false,
      region: "EU",
      opaque: { id: "legacy-private-value" },
    })
  })
  it("does not load HEAD or allow submission when a pinned archive is unavailable", async () => {
    h.fetch.mockResolvedValue({ ok: false, status: 404 })
    const cancel = vi.fn()
    render(
      <RoutinePresetForm
        workspaceId="ws"
        slug="daily"
        version={3}
        allowDraft
        onCancel={cancel}
        onSave={vi.fn()}
      />,
    )
    await screen.findByRole("alert")
    expect(h.fetch).toHaveBeenCalledTimes(1)
    expect(h.fetch.mock.calls[0][0]).toBe("/api/v1/workspaces/ws/pipelines/daily/versions/3")
    expect(screen.queryByRole("button", { name: "Save inputs" })).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Input schema")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(cancel).toHaveBeenCalledTimes(1)
  })
  it("explicitly loads the saved draft when repairing a publication conflict", async () => {
    const save = vi.fn()
    h.fetch.mockImplementation(async (url: string) => ({
      ok: true,
      json: async () =>
        url.endsWith("/draft")
          ? {
              id: "draft1",
              revision: 8,
              document: {
                definition: { inputs: [{ name: "country", type: "string", default: "CZ" }] },
              },
            }
          : { definition },
    }))
    render(
      <RoutinePresetForm
        workspaceId="ws"
        slug="daily"
        allowDraft
        onCancel={vi.fn()}
        onSave={save}
      />,
    )
    await screen.findByRole("button", { name: "Save inputs" })
    fireEvent.change(screen.getByLabelText("Input schema"), { target: { value: "draft" } })
    await screen.findByText(/Draft revision 8/)
    fireEvent.click(screen.getByRole("button", { name: "Save inputs" }))
    await waitFor(() => expect(save).toHaveBeenCalledWith({ country: "CZ" }))
  })
})
