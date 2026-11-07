import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { ChatTurn } from "@/hooks/use-chat"
import { TurnRenderer } from "../turn-renderer"

function userTurn(metadata?: Record<string, unknown>): ChatTurn {
  return {
    id: "u1",
    role: "user",
    parts: [{ id: "p1", type: "text", content: "Rewrite the merger summary", timestamp: new Date() }],
    isStreaming: false,
    timestamp: new Date("2026-04-17T12:34:00Z"),
    metadata,
  }
}

function renderTurn(turn: ChatTurn) {
  return render(<TurnRenderer turn={turn} onCopy={async () => true} onFileClick={vi.fn()} onEditUserMessage={vi.fn()} />)
}

describe("TurnRenderer — saved native Craft instruction (#5009)", () => {
  it("offers no edit and resend", () => {
    renderTurn(userTurn({ native_craft_instruction: true }))
    expect(screen.getByText("Rewrite the merger summary")).toBeTruthy()
    expect(screen.queryByRole("button", { name: /edit/i })).toBeNull()
  })

  it("keeps it on an ordinary message", () => {
    renderTurn(userTurn())
    expect(screen.getByRole("button", { name: /edit/i })).toBeTruthy()
  })
})
