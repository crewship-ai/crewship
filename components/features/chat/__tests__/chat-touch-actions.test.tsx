import { describe, it, expect, vi } from "vitest"
import React from "react"
import { render, screen } from "@testing-library/react"
import type { ChatTurn } from "@/hooks/use-chat"

// A reply's Copy and feedback buttons and a message's Edit & resend appear on
// hover. A touch screen has no hover, so on a coarse pointer they must always
// show, or a phone user taps blank space without knowing anything is there.

vi.mock("@/hooks/use-smooth-text", () => ({ useSmoothText: (text: string) => text }))
vi.mock("@/hooks/use-auth", () => ({
  useSession: () => ({ data: { user: { id: "u", name: "U", email: "u@e.com" }, expires: "" }, status: "authenticated" as const }),
}))
vi.mock("../reactions/turn-reactions", () => ({ TurnReactions: () => null }))

import { AssistantTurn } from "../assistant-turn"
import { EditableUserMessage } from "../messages/editable-user-message"

const reply: ChatTurn = {
  id: "t-1", role: "assistant", isStreaming: false, timestamp: new Date("2026-10-20T00:00:00Z"),
  parts: [{ id: "p-1", type: "text", content: "Answer", timestamp: new Date("2026-10-20T00:00:00Z") }],
}

describe("chat actions on a touch screen", () => {
  it("always shows a reply's actions on a coarse pointer", () => {
    render(<AssistantTurn turn={reply} onCopy={async () => true} onFileClick={vi.fn()} />)
    const row = screen.getByRole("button", { name: "Copy" }).closest(".opacity-0")
    expect(row).not.toBeNull()
    expect(row!.className).toContain("pointer-coarse:opacity-100")
  })

  it("shows Edit & resend on a coarse pointer and while focused", () => {
    render(<EditableUserMessage text="Hello" timestamp={new Date("2026-10-20T00:00:00Z")} onSave={vi.fn()} />)
    const row = screen.getByRole("button", { name: "Edit & resend" }).closest(".opacity-0")
    expect(row).not.toBeNull()
    expect(row!.className).toContain("pointer-coarse:opacity-100")
    expect(row!.className).toContain("focus-within:opacity-100")
  })
})
