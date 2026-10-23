import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

const api = vi.fn()
const backfill = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("@/lib/agent-avatar-persist", () => ({ backfillAgentAvatars: (...a: unknown[]) => backfill(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { useApplyAvatarStyle } from "../use-apply-avatar-style"

const CREW = { id: "c1", name: "Engineering", avatar_style: "lorelei" }

function Harness({ onChanged }: { onChanged: () => void }) {
  const { applyAvatarStyle, avatarStyleDialog } = useApplyAvatarStyle({ crew: CREW, agentCount: 3, workspaceId: "w1", onChanged })
  return (
    <>
      <button type="button" onClick={() => void applyAvatarStyle(false)}>Apply to all</button>
      <button type="button" onClick={() => void applyAvatarStyle(true)}>Reset overrides</button>
      {avatarStyleDialog}
    </>
  )
}

beforeEach(() => {
  api.mockReset().mockResolvedValue({ ok: true, status: 200 })
  backfill.mockReset().mockResolvedValue(undefined)
})

// Crew settings: both buttons repaint every agent, so both ask first (#3342).
describe("crew avatar style", () => {
  it("asks before applying, and Cancel posts nothing", async () => {
    const onChanged = vi.fn()
    render(<Harness onChanged={onChanged} />)
    fireEvent.click(screen.getByRole("button", { name: "Apply to all" }))
    const dialog = await screen.findByRole("alertdialog", { name: "Apply avatar style to Engineering?" })
    expect(dialog).toHaveTextContent("3 agents get new faces")
    expect(dialog).toHaveTextContent("Avatars set on individual agents stay")
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())
    expect(api).not.toHaveBeenCalled()
    expect(onChanged).not.toHaveBeenCalled()
  })

  it("applies on confirm", async () => {
    const onChanged = vi.fn()
    render(<Harness onChanged={onChanged} />)
    fireEvent.click(screen.getByRole("button", { name: "Apply to all" }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Apply to all" }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(api).toHaveBeenCalledWith("/api/v1/crews/c1/apply-avatar-style?workspace_id=w1", { method: "POST" })
  })

  it("says Reset clears per-agent avatars and sends reset_overrides on confirm", async () => {
    render(<Harness onChanged={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: "Reset overrides" }))
    const dialog = await screen.findByRole("alertdialog", { name: "Reset avatars in Engineering?" })
    expect(dialog).toHaveTextContent("Avatars set on individual agents are cleared")
    fireEvent.click(within(dialog).getByRole("button", { name: "Reset overrides" }))
    await waitFor(() => expect(api).toHaveBeenCalledWith("/api/v1/crews/c1/apply-avatar-style?workspace_id=w1&reset_overrides=true", { method: "POST" }))
  })
})
