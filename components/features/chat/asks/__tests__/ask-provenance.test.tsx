import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"
import type { ChatTurn } from "@/hooks/use-chat"

// =============================================================================
// "via Add a receipt" — which form a turn came out of.
//
// A form submit is an ORDINARY message: nothing in the TEXT distinguishes it
// from something typed, and that is deliberate. It used to leave the badge
// nothing to go on but the content itself, which is not an identity — two
// identical submissions collided and a reload lost the lot (audit P0.6).
//
// The badge now reads the submission envelope carried WITH the message, and
// falls back to the old content note only for a turn that has none.
// =============================================================================

vi.mock("../../assistant-turn", () => ({
  AssistantTurn: () => <div data-testid="assistant-turn-mock" />,
}))

import { TurnRenderer } from "../../turn-renderer"
import { ASK_SUBMISSION_METADATA_KEY, type AskSubmissionEnvelope } from "../ask-envelope"
import {
  askProvenanceForTurn,
  forgetAskSubmissionsInMemory,
  lookupAskProvenance,
  recordAskProvenance,
  recordAskSubmission,
  resetAskProvenance,
} from "../ask-provenance"

const SESSION = "sess-1"

function envelope(overrides: Partial<AskSubmissionEnvelope> = {}): AskSubmissionEnvelope {
  return {
    submission_id: "sub_1",
    form_id: "receipt",
    form_label: "Add a receipt",
    form_version: 1,
    values: { supplier: "Vodafone" },
    rendered_text: "Zaúčtuj fakturu od Vodafone",
    ...overrides,
  }
}

function userTurn(
  text: string,
  opts: { authorUserId?: string; metadata?: Record<string, unknown>; id?: string } = {},
): ChatTurn {
  return {
    id: opts.id ?? "u1",
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
    authorUserId: opts.authorUserId,
    // `metadata` on a ChatTurn is typed for the one key that consumes it
    // today (trace_id); the envelope is additive on the same map, and
    // hooks/use-chat.ts widens the type when it starts carrying it.
    ...(opts.metadata ? { metadata: opts.metadata as ChatTurn["metadata"] } : {}),
  }
}

function renderTurn(turn: ChatTurn, sessionId = SESSION) {
  return render(
    <TurnRenderer
      turn={turn}
      chatId={sessionId}
      onCopy={() => {}}
      onFileClick={() => {}}
      resolveAskProvenance={(content) => lookupAskProvenance(sessionId, content)}
    />,
  )
}

describe("ask provenance", () => {
  beforeEach(() => resetAskProvenance())

  it("reads the form off the turn's own envelope", () => {
    renderTurn(
      userTurn("Zaúčtuj fakturu od Vodafone", {
        metadata: { [ASK_SUBMISSION_METADATA_KEY]: envelope() },
      }),
    )
    expect(screen.getByTestId("ask-provenance").textContent).toContain("via Add a receipt")
  })

  // THE COLLISION. Two turns whose text is character-for-character identical,
  // out of two different forms. Keyed by content there is one entry and one of
  // the two badges is a lie; keyed by the envelope on the turn, each says what
  // it came from.
  it("does not confuse two identical messages that came out of different forms", () => {
    const text = "Zaúčtuj fakturu od Vodafone"
    recordAskProvenance(SESSION, text, "Add a receipt")
    recordAskProvenance(SESSION, text, "Log an expense")

    const first = renderTurn(
      userTurn(text, {
        id: "u1",
        metadata: {
          [ASK_SUBMISSION_METADATA_KEY]: envelope({ submission_id: "sub_1" }),
        },
      }),
    )
    expect(screen.getByTestId("ask-provenance").textContent).toContain("via Add a receipt")
    first.unmount()

    renderTurn(
      userTurn(text, {
        id: "u2",
        metadata: {
          [ASK_SUBMISSION_METADATA_KEY]: envelope({
            submission_id: "sub_2",
            form_id: "expense",
            form_label: "Log an expense",
          }),
        },
      }),
    )
    expect(screen.getByTestId("ask-provenance").textContent).toContain("via Log an expense")
  })

  // The same collision, seen from the legacy path that ChatPanel still calls.
  // It cannot resolve the ambiguity — content genuinely does not carry the
  // answer — so it stops answering rather than picking the most recent label.
  // A missing badge is a courtesy not offered; a wrong one is the transcript
  // telling the user something untrue.
  it("refuses to guess when one message was recorded under two forms", () => {
    const text = "Zaúčtuj fakturu od Vodafone"
    recordAskProvenance(SESSION, text, "Add a receipt")
    expect(lookupAskProvenance(SESSION, text)).toBe("Add a receipt")

    recordAskProvenance(SESSION, text, "Log an expense")
    expect(lookupAskProvenance(SESSION, text)).toBeNull()

    renderTurn(userTurn(text))
    expect(screen.queryByTestId("ask-provenance")).toBeNull()
  })

  it("still labels the turn the user is looking at before the envelope is on the wire", () => {
    recordAskProvenance(SESSION, "Zaúčtuj fakturu od Vodafone", "Add a receipt")
    renderTurn(userTurn("Zaúčtuj fakturu od Vodafone"))
    expect(screen.getByTestId("ask-provenance").textContent).toContain("via Add a receipt")
  })

  it("says nothing above a message the user typed", () => {
    renderTurn(userTurn("just a question"))
    expect(screen.queryByTestId("ask-provenance")).toBeNull()
  })

  it("never claims a teammate's message came out of this client's form", () => {
    recordAskProvenance(SESSION, "Zaúčtuj fakturu od Vodafone", "Add a receipt")
    renderTurn(
      userTurn("Zaúčtuj fakturu od Vodafone", {
        authorUserId: "other-user",
        metadata: { [ASK_SUBMISSION_METADATA_KEY]: envelope() },
      }),
    )
    expect(screen.queryByTestId("ask-provenance")).toBeNull()
  })

  it("completes a pointer-only envelope from the local ledger", () => {
    recordAskSubmission(SESSION, envelope({ submission_id: "sub_9" }))
    // A sender that put only the id on the wire — the envelope itself is in
    // the ledger this tab wrote.
    const turn = userTurn("Zaúčtuj fakturu od Vodafone", {
      metadata: {
        [ASK_SUBMISSION_METADATA_KEY]: {
          submission_id: "sub_9",
          form_id: "receipt",
          form_label: "",
          form_version: 1,
          values: {},
          rendered_text: "",
        },
      },
    })
    expect(askProvenanceForTurn(SESSION, turn)).toBe("Add a receipt")
  })

  it("keeps answering after a reload, because the ledger is written down", () => {
    recordAskSubmission(SESSION, envelope({ submission_id: "sub_9" }))
    forgetAskSubmissionsInMemory()

    const turn = userTurn("Zaúčtuj fakturu od Vodafone", {
      metadata: {
        [ASK_SUBMISSION_METADATA_KEY]: {
          submission_id: "sub_9",
          form_id: "receipt",
          form_label: "",
          form_version: 1,
          values: {},
          rendered_text: "",
        },
      },
    })
    expect(askProvenanceForTurn(SESSION, turn)).toBe("Add a receipt")
  })

  it("is bounded — a long session does not accumulate every message ever sent", () => {
    for (let i = 0; i < 200; i++) {
      recordAskProvenance(SESSION, `message ${i}`, "Add a receipt")
    }
    expect(lookupAskProvenance(SESSION, "message 199")).toBe("Add a receipt")
    expect(lookupAskProvenance(SESSION, "message 0")).toBeNull()

    for (let i = 0; i < 100; i++) {
      recordAskSubmission(SESSION, envelope({ submission_id: `sub_${i}` }))
    }
    expect(askProvenanceForTurn(SESSION, userTurn("x", {
      metadata: { [ASK_SUBMISSION_METADATA_KEY]: envelope({ submission_id: "sub_99" }) },
    }))).toBe("Add a receipt")
  })
})
