import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import type { ChatTurn } from "@/hooks/use-chat"
import { ExportDialog } from "../export-dialog"

vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const at = new Date("2026-10-21T01:00:00Z")
const user = (content: string): ChatTurn => ({ id: `u-${content}`, role: "user", timestamp: at, parts: [{ type: "text", content }] }) as ChatTurn
const reply = (id: string, content: string, turnState?: ChatTurn["turnState"]): ChatTurn =>
  ({ id, role: "assistant", timestamp: at, parts: content ? [{ type: "text", content }] : [], turnState }) as ChatTurn

async function exported(turns: ChatTurn[]) {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
  render(<ExportDialog turns={turns} agentName="Riley" open onOpenChange={() => {}} />)
  fireEvent.click(screen.getByRole("button", { name: /Copy to clipboard/ }))
  await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
  return writeText.mock.calls[0][0] as string
}

describe("chat export", () => {
  it("says a reply was stopped before any text instead of leaving an empty heading", async () => {
    const md = await exported([user("Plan the launch"), reply("a1", "", "cancelled")])
    expect(md).toContain("## Riley\n_Stopped before any reply._\n")
  })
  it("marks a partial reply as partial, as the chat does", async () => {
    const md = await exported([user("Plan the launch"), reply("a1", "Step one is", "cancelled")])
    expect(md).toContain("## Riley\nStep one is\n_Stopped. The reply above is partial._\n")
  })
  it("adds no note to a completed reply", async () => {
    const md = await exported([user("Hi"), reply("a1", "Hello", "completed")])
    expect(md).toMatch(/## Riley\nHello\n$/)
    expect(md).not.toMatch(/_Stopped|_Interrupted|_The run failed/)
  })
})
