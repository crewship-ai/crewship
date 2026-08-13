import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"
import type { ChatTurn } from "@/hooks/use-chat"

// =============================================================================
// A form submit is an ORDINARY message — nothing on the wire distinguishes it
// from something typed. That is deliberate, and it leaves the transcript
// showing words the user never wrote as if they had. The `via <form>` line is
// the client-side note that fixes it.
// =============================================================================

vi.mock("../../assistant-turn", () => ({
  AssistantTurn: () => <div data-testid="assistant-turn-mock" />,
}))

import { TurnRenderer } from "../../turn-renderer"
import {
  lookupAskProvenance,
  recordAskProvenance,
  resetAskProvenance,
} from "../ask-provenance"

function userTurn(text: string, authorUserId?: string): ChatTurn {
  return {
    id: "u1",
    role: "user",
    parts: [
      {
        id: "p1",
        type: "text",
        content: text,
        timestamp: new Date("2026-08-12T12:00:00Z"),
      },
    ],
    isStreaming: false,
    timestamp: new Date("2026-08-12T12:00:00Z"),
    authorUserId,
  }
}

function renderTurn(turn: ChatTurn, sessionId = "sess-1") {
  return render(
    <TurnRenderer
      turn={turn}
      onCopy={() => {}}
      onFileClick={() => {}}
      resolveAskProvenance={(content) => lookupAskProvenance(sessionId, content)}
    />,
  )
}

describe("ask provenance", () => {
  beforeEach(() => resetAskProvenance())

  it("is scoped to the session and keyed on the content that went out", () => {
    recordAskProvenance("sess-1", "Zaúčtuj fakturu od Vodafone", "Add a receipt")

    expect(lookupAskProvenance("sess-1", "Zaúčtuj fakturu od Vodafone")).toBe("Add a receipt")
    // Trimming matches useChat, which stores `content.trim()` on the turn.
    expect(lookupAskProvenance("sess-1", "  Zaúčtuj fakturu od Vodafone  ")).toBe("Add a receipt")
    // A different session, or different text, is a different message.
    expect(lookupAskProvenance("sess-2", "Zaúčtuj fakturu od Vodafone")).toBeNull()
    expect(lookupAskProvenance("sess-1", "something else")).toBeNull()
  })

  it("renders `via <form>` above a bubble that came out of a form", () => {
    recordAskProvenance("sess-1", "Zaúčtuj fakturu od Vodafone", "Add a receipt")
    renderTurn(userTurn("Zaúčtuj fakturu od Vodafone"))

    expect(screen.getByTestId("ask-provenance").textContent).toContain("via Add a receipt")
  })

  it("says nothing above a message the user typed", () => {
    renderTurn(userTurn("just a question"))
    expect(screen.queryByTestId("ask-provenance")).toBeNull()
  })

  it("never claims a teammate's message came out of this client's form", () => {
    recordAskProvenance("sess-1", "Zaúčtuj fakturu od Vodafone", "Add a receipt")
    renderTurn(userTurn("Zaúčtuj fakturu od Vodafone", "other-user"))
    expect(screen.queryByTestId("ask-provenance")).toBeNull()
  })

  it("is bounded — a long session does not accumulate every message ever sent", () => {
    for (let i = 0; i < 200; i++) {
      recordAskProvenance("sess-1", `message ${i}`, "Add a receipt")
    }
    // The newest survives; the oldest has been evicted.
    expect(lookupAskProvenance("sess-1", "message 199")).toBe("Add a receipt")
    expect(lookupAskProvenance("sess-1", "message 0")).toBeNull()
  })
})
