import { beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetch }))
import { RoutineFixtureTest } from "../routine-fixture-test"

const recipe = {
  name: "fixture",
  inputs: [{ name: "count", type: "integer", default: 0 }],
  steps: [
    { id: "fetch", type: "http", http: { method: "POST", url: "https://example.com" } },
    {
      id: "extract",
      type: "transform",
      transform: { input: "{{ steps.fetch.output }}", expression: ".count" },
    },
  ],
}
beforeEach(() => {
  cleanup()
  h.fetch.mockReset()
  h.fetch.mockResolvedValue({
    ok: true,
    json: async () => ({
      execution_mode: "fixtures",
      step_id: "fetch",
      output_source: "fixture",
      output: "",
      valid: true,
      validation_declared: true,
      definition_hash: "definition",
      fixture_hash: "fixture",
      limitations: ["No model call"],
    }),
  })
})
describe("fixture test editor", () => {
  it("requires explicit replacement and never calls a live test or publication endpoint", async () => {
    render(<RoutineFixtureTest workspaceId="ws" definition={recipe} />)
    fireEvent.change(screen.getByLabelText("Step to test"), { target: { value: "fetch" } })
    expect(screen.getByRole("button", { name: "Test with fixtures" })).toBeDisabled()
    fireEvent.click(screen.getByLabelText("Replace this step with the output below"))
    fireEvent.click(screen.getByRole("button", { name: "Test with fixtures" }))
    await screen.findByText("Fixture output passed structural validation.")
    expect(h.fetch).toHaveBeenCalledTimes(1)
    expect(h.fetch.mock.calls[0][0]).toBe("/api/v1/workspaces/ws/pipelines/fixture_test")
    expect(JSON.parse(h.fetch.mock.calls[0][1].body)).toMatchObject({
      step_id: "fetch",
      fixture_output: "",
      inputs: { count: 0 },
    })
  })
  it("keeps the output's evidence but marks it stale after the recipe changes", async () => {
    const view = render(<RoutineFixtureTest workspaceId="ws" definition={recipe} />)
    fireEvent.change(screen.getByLabelText("Step to test"), { target: { value: "extract" } })
    fireEvent.change(screen.getByLabelText("Captured upstream outputs · JSON"), {
      target: { value: '{"fetch":{"count":42}}' },
    })
    fireEvent.click(screen.getByRole("button", { name: "Test with fixtures" }))
    await screen.findByText("Fixture output passed structural validation.")
    expect(JSON.parse(h.fetch.mock.calls[0][1].body).step_outputs).toEqual({
      fetch: '{"count":42}',
    })
    view.rerender(
      <RoutineFixtureTest workspaceId="ws" definition={{ ...recipe, description: "Changed" }} />,
    )
    expect(screen.getByText("The recipe has changed since this test.")).toBeInTheDocument()
  })
  it("does not present a live response as a fixture result", async () => {
    h.fetch.mockResolvedValue({
      ok: true,
      json: async () => ({ execution_mode: "live", valid: true }),
    })
    render(<RoutineFixtureTest workspaceId="ws" definition={recipe} />)
    fireEvent.change(screen.getByLabelText("Step to test"), { target: { value: "extract" } })
    fireEvent.click(screen.getByRole("button", { name: "Test with fixtures" }))
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent("did not confirm fixture execution"),
    )
    expect(
      screen.queryByText("Fixture output passed structural validation."),
    ).not.toBeInTheDocument()
  })
})
