import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { describe, it, expect, vi } from "vitest"
import { RoutineLiveRunBanner } from "../routine-live-run-banner"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"

const run = (id: string) => ({ id, status: "running" }) as PipelineRunRecord

describe("live run stop confirmation", () => {
  it("stops the originally selected run when realtime updates reorder the list", async () => {
    const onStop = vi.fn()
    const props = { canStop: true, onOpen: vi.fn(), onStop }
    const { rerender } = render(<RoutineLiveRunBanner {...props} runs={[run("first"), run("second")]} />)
    fireEvent.click(screen.getByRole("button", { name: "Stop" }))
    rerender(<RoutineLiveRunBanner {...props} runs={[run("second"), run("first")]} />)
    fireEvent.click(screen.getByRole("button", { name: "Stop run" }))
    await waitFor(() => expect(onStop).toHaveBeenCalledWith("first"))
    expect(onStop).toHaveBeenCalledTimes(1)
  })

  it("dismisses confirmation when that run disappears and does not target its replacement", async () => {
    const onStop = vi.fn()
    const props = { canStop: true, onOpen: vi.fn(), onStop }
    const { rerender } = render(<RoutineLiveRunBanner {...props} runs={[run("first")]} />)
    fireEvent.click(screen.getByRole("button", { name: "Stop" }))
    rerender(<RoutineLiveRunBanner {...props} runs={[run("second")]} />)
    await waitFor(() => expect(screen.queryByRole("button", { name: "Stop run" })).toBeNull())
    expect(onStop).not.toHaveBeenCalled()
  })
  it("dismisses confirmation when stop permission is revoked", async () => {
    const onStop = vi.fn()
    const props = { runs: [run("first")], onOpen: vi.fn(), onStop }
    const { rerender } = render(<RoutineLiveRunBanner {...props} canStop />)
    fireEvent.click(screen.getByRole("button", { name: "Stop" }))
    rerender(<RoutineLiveRunBanner {...props} canStop={false} />)
    await waitFor(() => expect(screen.queryByRole("button", { name: "Stop run" })).toBeNull())
    expect(onStop).not.toHaveBeenCalled()
  })

})
