import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { RoutineFixtureImport } from "../routine-fixture-import"
import { RoutineFixtureTest } from "../routine-fixture-test"
const h = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetch }))
const run = {
  id: "r",
  workspace_id: "ws",
  definition_status: "available",
  definition: { name: "old" },
  definition_hash: "hash",
  step_outputs_available: true,
  step_outputs: { send: "captured" },
  inputs: { count: 0, enabled: false },
  status: "failed",
}
const load = () => {
  fireEvent.change(screen.getByLabelText("Use captured run data"), { target: { value: "r" } })
  fireEvent.click(screen.getByRole("button", { name: "Load captured data" }))
}
describe("fixture import", () => {
  it("loads only on request and discards a response from the previous workspace", async () => {
    let resolve!: (r: unknown) => void
    h.fetch.mockReset().mockImplementation(
      () =>
        new Promise((r) => {
          resolve = r
        }),
    )
    const imported = vi.fn()
    const view = render(<RoutineFixtureImport workspaceId="ws" onImport={imported} />)
    expect(h.fetch).not.toHaveBeenCalled()
    load()
    view.rerender(<RoutineFixtureImport workspaceId="other" onImport={imported} />)
    resolve({ ok: true, json: async () => run })
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Load captured data" })).toBeDisabled(),
    )
    expect(imported).not.toHaveBeenCalled()
  })
  it("surfaces failed reads without overwriting the current fixture", async () => {
    h.fetch.mockReset().mockResolvedValue({
      ok: true,
      json: async () => ({ ...run, step_outputs_available: false }),
    })
    const imported = vi.fn()
    render(<RoutineFixtureImport workspaceId="ws" onImport={imported} />)
    load()
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent("outputs are unavailable"),
    )
    expect(imported).not.toHaveBeenCalled()
  })
  it("uses captured typed inputs but replaces an effectful step only on explicit choice", async () => {
    h.fetch
      .mockReset()
      .mockResolvedValueOnce({ ok: true, json: async () => run })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          execution_mode: "fixtures",
          step_id: "send",
          valid: true,
          limitations: [],
        }),
      })
    render(
      <RoutineFixtureTest
        workspaceId="ws"
        definition={{
          name: "new",
          inputs: [
            { name: "count", type: "integer", default: 99 },
            { name: "enabled", type: "boolean", default: true },
          ],
          steps: [{ id: "send", type: "http" }],
        }}
      />,
    )
    load()
    const requestedPath = h.fetch.mock.calls[0][0] as string
    const route = requestedPath
      .replace("/workspaces/ws/", "/workspaces/{workspaceId}/")
      .replace(/\/r$/, "/{runId}")
    const spec = JSON.parse(
      readFileSync(resolve(process.cwd(), "internal/api/openapi.gen.json"), "utf8"),
    )
    expect(
      spec.paths[route]?.get,
      `GET ${requestedPath} must exist in the server contract`,
    ).toBeDefined()
    await screen.findByText(/Source run r/)
    fireEvent.change(screen.getByLabelText("Step to test"), { target: { value: "send" } })
    expect(screen.getByRole("button", { name: "Test with fixtures" })).toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Use captured output for this step" }))
    fireEvent.click(screen.getByRole("button", { name: "Test with fixtures" }))
    await waitFor(() => expect(h.fetch).toHaveBeenCalledTimes(2))
    const body = JSON.parse(h.fetch.mock.calls[1][1].body)
    expect(body.inputs).toEqual({ count: 0, enabled: false })
    expect(body.fixture_output).toBe("captured")
    expect(h.fetch.mock.calls[1][0]).toMatch(/fixture_test$/)
  })
})
