import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"

// =============================================================================
// The chip rail is where the ask-packs success metric is either measurable or
// it is not. "≥ 35 % of sessions start from a chip" needs two numbers the
// product never produced: how often a chip was SHOWN, and how often one was
// CLICKED — split by kind, because a question chip and a form chip are
// different offers and folding them together is what makes the number useless.
//
// What is pinned here:
//   · one impression per visible chip, and only per VISIBLE chip;
//   · one click event per click, carrying the kind;
//   · the chip's label never appears in an event — a suggested question is
//     text somebody wrote, and this channel does not carry text;
//   · a telemetry sink that throws cannot stop the chip from doing its job.
// =============================================================================

import {
  drainChatEvents,
  resetChatTelemetry,
  setChatTelemetrySink,
  type ChatEvent,
} from "@/lib/telemetry"

import { AskRail } from "../ask-rail"
import type { AskForm } from "../types"

const receipt: AskForm = {
  id: "receipt",
  label: "Add a receipt",
  template: "Zaúčtuj {{supplier}}",
  attachment: "required",
  fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }],
}

const close: AskForm = {
  id: "close",
  label: "Monthly close",
  template: "Close {{month}}",
  attachment: "none",
  fields: [{ name: "month", label: "Month", type: "month" }],
}

const QUESTION = "What is still unpaid at Vodafone?"

const onPickQuestion = vi.fn()
const onPickForm = vi.fn()

let events: ChatEvent[]

function renderRail(props: Partial<React.ComponentProps<typeof AskRail>> = {}) {
  return render(
    <AskRail
      questions={[QUESTION]}
      forms={[receipt]}
      limit={6}
      sessionId="sess-1"
      agentId="agent-1"
      onPickQuestion={onPickQuestion}
      onPickForm={onPickForm}
      {...props}
    />,
  )
}

const named = (name: string) => events.filter((e) => e.name === name)

beforeEach(() => {
  onPickQuestion.mockClear()
  onPickForm.mockClear()
  resetChatTelemetry()
  events = []
  setChatTelemetrySink((e) => events.push(e))
})

afterEach(cleanup)

describe("chip impressions", () => {
  it("records one impression per visible chip, with its kind and position", () => {
    renderRail()

    const shown = named("ask_chip_shown")
    expect(shown).toHaveLength(2)
    expect(shown.map((e) => e.payload.chip_kind)).toEqual(["form", "question"])
    expect(shown.map((e) => e.payload.position)).toEqual([0, 1])
    expect(shown[0].payload.chip_id).toBe("receipt")
    expect(shown[0].payload.session_id).toBe("sess-1")
    expect(shown[0].payload.agent_id).toBe("agent-1")
  })

  it("does not re-record an impression when the rail re-renders", () => {
    const { rerender } = renderRail()
    rerender(
      <AskRail
        questions={[QUESTION]}
        forms={[receipt]}
        limit={6}
        sessionId="sess-1"
        agentId="agent-1"
        onPickQuestion={onPickQuestion}
        onPickForm={onPickForm}
      />,
    )
    expect(named("ask_chip_shown")).toHaveLength(2)
  })

  it("does not record chips that overflowed into +N — they were not shown", () => {
    renderRail({ forms: [receipt, close], questions: [QUESTION], limit: 1 })

    const shown = named("ask_chip_shown")
    expect(shown).toHaveLength(1)
    expect(shown[0].payload.chip_id).toBe("receipt")
  })

  it("records the overflowed chips once the catalogue is opened", () => {
    renderRail({ forms: [receipt, close], questions: [QUESTION], limit: 1 })
    fireEvent.click(screen.getByTestId("ask-rail-more"))

    const ids = named("ask_chip_shown").map((e) => e.payload.chip_id)
    expect(ids).toContain("receipt")
    expect(ids).toContain("close")
    expect(ids).toHaveLength(3)
  })

  it("marks follow-up chips as a different source from cold-start chips", () => {
    renderRail({ chipSource: "followup" })
    expect(named("ask_chip_shown").every((e) => e.payload.source === "followup")).toBe(true)
  })
})

describe("chip clicks", () => {
  it("records exactly one click for a form chip, and still opens the form", () => {
    renderRail()
    fireEvent.click(screen.getByTestId("ask-chip-form-receipt"))

    const clicked = named("ask_chip_clicked")
    expect(clicked).toHaveLength(1)
    expect(clicked[0].payload.chip_kind).toBe("form")
    expect(clicked[0].payload.chip_id).toBe("receipt")
    expect(onPickForm).toHaveBeenCalledWith(receipt)
  })

  it("records exactly one click for a question chip, and still sends it", () => {
    renderRail()
    fireEvent.click(screen.getByTestId("ask-chip-question-0"))

    const clicked = named("ask_chip_clicked")
    expect(clicked).toHaveLength(1)
    expect(clicked[0].payload.chip_kind).toBe("question")
    expect(onPickQuestion).toHaveBeenCalledWith(QUESTION)
  })

  it("records a click made from the +N catalogue too", () => {
    renderRail({ forms: [receipt, close], questions: [], limit: 1 })
    fireEvent.click(screen.getByTestId("ask-rail-more"))
    const overflow = screen.getByTestId("ask-rail-overflow")
    fireEvent.click(within(overflow).getByText("Monthly close"))

    const clicked = named("ask_chip_clicked")
    expect(clicked).toHaveLength(1)
    expect(clicked[0].payload.chip_id).toBe("close")
  })

  it("gives a question chip a stable id that is not its text", () => {
    renderRail()
    fireEvent.click(screen.getByTestId("ask-chip-question-0"))
    const id = String(named("ask_chip_clicked")[0].payload.chip_id)

    expect(id).not.toContain("Vodafone")
    expect(id).toMatch(/^q_[0-9a-f]+$/)

    // Stable across renders: the same question is the same chip tomorrow.
    cleanup()
    resetChatTelemetry()
    events = []
    setChatTelemetrySink((e) => events.push(e))
    renderRail()
    fireEvent.click(screen.getByTestId("ask-chip-question-0"))
    expect(named("ask_chip_clicked")[0].payload.chip_id).toBe(id)
  })
})

describe("the rail carries no text into telemetry", () => {
  it("never emits a chip label, a question or a form template", () => {
    renderRail({ forms: [receipt, close], questions: [QUESTION], limit: 6 })
    fireEvent.click(screen.getByTestId("ask-chip-question-0"))
    fireEvent.click(screen.getByTestId("ask-chip-form-receipt"))

    const serialized = JSON.stringify(events)
    expect(serialized).not.toContain("Vodafone")
    expect(serialized).not.toContain("Add a receipt")
    expect(serialized).not.toContain("Monthly close")
    expect(serialized).not.toContain("Zaúčtuj")
  })
})

describe("telemetry cannot break the rail", () => {
  it("a throwing sink does not stop a chip from opening its form", () => {
    setChatTelemetrySink(() => {
      throw new Error("sink exploded")
    })
    expect(() => renderRail()).not.toThrow()
    expect(() => fireEvent.click(screen.getByTestId("ask-chip-form-receipt"))).not.toThrow()
    expect(onPickForm).toHaveBeenCalledTimes(1)
  })

  it("buffers events with no sink registered at all", () => {
    resetChatTelemetry()
    renderRail()
    expect(drainChatEvents().map((e) => e.name)).toEqual(["ask_chip_shown", "ask_chip_shown"])
  })
})
