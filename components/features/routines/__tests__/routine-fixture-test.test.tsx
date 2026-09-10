import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

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
  it("ignores an old step test that finishes after selecting another step", async () => {
    let finish!: (response: Response) => void
    h.fetch.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    const view = render(
      <RoutineFixtureTest workspaceId="ws" definition={recipe} selectedStepId="extract" />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Test this step" }))
    await waitFor(() => expect(h.fetch).toHaveBeenCalledTimes(1))
    view.rerender(
      <RoutineFixtureTest workspaceId="ws" definition={recipe} selectedStepId="fetch" />,
    )
    await act(async () =>
      finish(
        new Response(
          JSON.stringify({
            execution_mode: "fixtures",
            step_id: "extract",
            output: "old result",
            valid: true,
          }),
        ),
      ),
    )
    expect(screen.queryByText("old result")).not.toBeInTheDocument()
    expect(screen.queryByText("Sample result passed its checks.")).not.toBeInTheDocument()
    expect(screen.getByLabelText("Replace this step with the output below")).toBeEnabled()
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument()
  })
  it("requires explicit replacement and never calls a live test or publication endpoint", async () => {
    render(<RoutineFixtureTest workspaceId="ws" definition={recipe} />)
    fireEvent.change(screen.getByLabelText("Step to test"), { target: { value: "fetch" } })
    expect(screen.getByRole("button", { name: "Test this step" })).toBeDisabled()
    fireEvent.click(screen.getByLabelText("Replace this step with the output below"))
    fireEvent.click(screen.getByRole("button", { name: "Test this step" }))
    await screen.findByText("Sample result passed its checks.")
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
    fireEvent.change(screen.getByLabelText("Sample results · JSON"), {
      target: { value: '{"fetch":{"count":42}}' },
    })
    fireEvent.click(screen.getByRole("button", { name: "Test this step" }))
    await screen.findByText("Sample result passed its checks.")
    expect(JSON.parse(h.fetch.mock.calls[0][1].body).step_outputs).toEqual({
      fetch: '{"count":42}',
    })
    view.rerender(
      <RoutineFixtureTest
        workspaceId="ws"
        definition={{ ...recipe, description: "Changed" }}
      />,
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
    fireEvent.click(screen.getByRole("button", { name: "Test this step" }))
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "did not confirm a test with sample data",
      ),
    )
    expect(screen.queryByText("Sample result passed its checks.")).not.toBeInTheDocument()
  })
})

it("submits current defaults after the recipe input schema changes", async () => {
  const view = render(<RoutineFixtureTest workspaceId="ws" definition={recipe} />)
  fireEvent.change(screen.getByLabelText("Step to test"), { target: { value: "extract" } })
  fireEvent.click(screen.getByRole("button", { name: "Test this step" }))
  await screen.findByText("Sample result passed its checks.")
  expect(JSON.parse(h.fetch.mock.calls[0][1].body).inputs).toEqual({ count: 0 })
  view.rerender(
    <RoutineFixtureTest
      workspaceId="ws"
      definition={{
        ...recipe,
        inputs: [
          { name: "count", type: "integer", default: 7 },
          { name: "enabled", type: "boolean", default: false },
        ],
      }}
    />,
  )
  fireEvent.click(screen.getByRole("button", { name: "Test this step" }))
  await waitFor(() => expect(h.fetch).toHaveBeenCalledTimes(2))
  expect(JSON.parse(h.fetch.mock.calls[1][1].body).inputs).toEqual({ count: 7, enabled: false })
})
