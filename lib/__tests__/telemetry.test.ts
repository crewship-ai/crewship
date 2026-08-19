import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  CHAT_EVENT_NAMES,
  CHAT_EVENT_SCHEMA,
  drainChatEvents,
  emitChatEvent,
  emitChatEventOnce,
  hashedId,
  mimeKind,
  peekChatEvents,
  resetChatTelemetry,
  setChatTelemetrySink,
  uploadFailureReason,
  type ChatEvent,
  type ChatEventName,
} from "@/lib/telemetry"

/* ------------------------------------------------------------------ *
 *  The vocabulary is the contract. These two lists are duplicated on
 *  purpose: the module owns the names, the test owns a written-down
 *  copy, and renaming an event in one place without the other is the
 *  failure this catches. Adding an event is a deliberate edit here.
 * ------------------------------------------------------------------ */
const EXPECTED_NAMES = [
  "ask_chip_shown",
  "ask_chip_clicked",
  "ask_form_opened",
  "ask_form_submitted",
  "ask_form_abandoned",
  "attachment_uploaded",
  "attachment_upload_failed",
  "chat_session_created",
  "chat_session_titled",
  "conversation_search_run",
  "conversation_search_result_opened",
  "chat_approval_shown",
  "chat_approval_decided",
] as const

/**
 * Payload keys that would carry, or strongly hint at, something a person
 * typed. Asserted over the WHOLE vocabulary rather than one example: the
 * point is that a future event cannot quietly introduce `message_text`.
 */
const CONTENT_KEY_PATTERN =
  /(^|_)(text|body|content|message|msg|value|values|answer|answers|filename|file|path|title|name|prompt|output|email|url|note|comment|label|summary|query|term|search)(_|$)/

/** Strings that must never survive into a sink payload, whatever the key. */
const POISON = [
  "please wire the invoice to IBAN CZ65 0800",
  "Acme Q3 invoice.pdf",
  "jana@example.com",
  "my secret answer",
]

function sinkSpy() {
  const seen: ChatEvent[] = []
  setChatTelemetrySink((e) => {
    seen.push(e)
  })
  return seen
}

beforeEach(() => {
  resetChatTelemetry()
})

describe("event vocabulary", () => {
  it("is exactly the documented list, in a stable order", () => {
    expect([...CHAT_EVENT_NAMES]).toEqual([...EXPECTED_NAMES])
  })

  it("names are boring: lowercase snake_case, no version suffixes", () => {
    for (const name of CHAT_EVENT_NAMES) {
      expect(name).toMatch(/^[a-z][a-z0-9]*(_[a-z0-9]+)*$/)
      expect(name).not.toMatch(/_v\d+$/)
    }
  })

  it("every name has a schema and every schema has a name", () => {
    expect(Object.keys(CHAT_EVENT_SCHEMA).sort()).toEqual([...CHAT_EVENT_NAMES].sort())
  })

  it("declares no free-form string field anywhere", () => {
    // The privacy guarantee is structural, not editorial: a payload value
    // can only be a number, a boolean, a member of a declared enum, or an
    // id-shaped token. There is deliberately no "string" field kind.
    for (const [name, spec] of Object.entries(CHAT_EVENT_SCHEMA)) {
      for (const [key, field] of Object.entries(spec)) {
        expect(["id", "num", "bool", "enum"], `${name}.${key}`).toContain(field.kind)
        if (field.kind === "enum") {
          expect(field.values.length, `${name}.${key}`).toBeGreaterThan(0)
        }
      }
    }
  })

  it("no payload key reads like user content", () => {
    const offenders: string[] = []
    for (const [name, spec] of Object.entries(CHAT_EVENT_SCHEMA)) {
      for (const key of Object.keys(spec)) {
        if (CONTENT_KEY_PATTERN.test(key)) offenders.push(`${name}.${key}`)
      }
    }
    expect(offenders).toEqual([])
  })
})

describe("emit", () => {
  it("emits exactly one event with the given name and payload", () => {
    const seen = sinkSpy()
    emitChatEvent("ask_chip_clicked", {
      chip_id: "chip_invoice",
      chip_kind: "form",
      position: 2,
      source: "pack",
    })
    expect(seen).toHaveLength(1)
    expect(seen[0].name).toBe("ask_chip_clicked")
    expect(seen[0].payload).toEqual({
      chip_id: "chip_invoice",
      chip_kind: "form",
      position: 2,
      source: "pack",
    })
    expect(typeof seen[0].ts).toBe("number")
  })

  it("drops keys the schema does not declare", () => {
    const seen = sinkSpy()
    emitChatEvent("ask_chip_clicked", {
      chip_id: "chip_invoice",
      chip_kind: "question",
      position: 0,
      source: "pack",
      // @ts-expect-error — undeclared key, must not reach the sink
      chip_text: "What did we agree with Acme?",
    })
    expect(seen[0].payload).not.toHaveProperty("chip_text")
  })

  it("drops a declared key whose value is the wrong shape", () => {
    const seen = sinkSpy()
    emitChatEvent("ask_form_submitted", {
      template_id: "tpl_invoice",
      field_count: 4,
      filled_count: 4,
      attachment_count: 0,
      // @ts-expect-error — a number field handed a string
      duration_ms: "quite a while",
    })
    expect(seen[0].payload).not.toHaveProperty("duration_ms")
    expect(seen[0].payload.template_id).toBe("tpl_invoice")
  })

  it("rejects an id that is not id-shaped (spaces, or longer than 64)", () => {
    const seen = sinkSpy()
    emitChatEvent("ask_form_abandoned", {
      template_id: "tpl invoice with words",
      field_count: 3,
      filled_count: 1,
      last_field_id: "x".repeat(65),
      reason: "dismissed",
    })
    expect(seen[0].payload).not.toHaveProperty("template_id")
    expect(seen[0].payload).not.toHaveProperty("last_field_id")
    expect(seen[0].payload.reason).toBe("dismissed")
  })

  it("rejects an enum value outside the declared set", () => {
    const seen = sinkSpy()
    emitChatEvent("ask_chip_shown", {
      chip_id: "c1",
      // @ts-expect-error — not a declared chip kind
      chip_kind: "essay",
      position: 0,
      source: "pack",
    })
    expect(seen[0].payload).not.toHaveProperty("chip_kind")
  })

  it("records into a bounded buffer even with no sink registered", () => {
    resetChatTelemetry()
    emitChatEvent("chat_session_created", { session_id: "s1", source: "sidebar" })
    const drained = drainChatEvents()
    expect(drained.map((e) => e.name)).toEqual(["chat_session_created"])
    expect(drainChatEvents()).toEqual([])
  })

  it("caps the buffer so a long session cannot grow memory without bound", () => {
    for (let i = 0; i < 700; i++) {
      emitChatEvent("ask_chip_shown", {
        chip_id: `c${i}`,
        chip_kind: "question",
        position: i,
        source: "pack",
      })
    }
    const buffered = peekChatEvents()
    expect(buffered.length).toBeLessThanOrEqual(500)
    // Oldest dropped, newest kept.
    expect(buffered[buffered.length - 1].payload.chip_id).toBe("c699")
  })
})

describe("emit never breaks the interaction", () => {
  it("swallows a throwing sink", () => {
    setChatTelemetrySink(() => {
      throw new Error("sink exploded")
    })
    expect(() =>
      emitChatEvent("chat_session_created", { session_id: "s1", source: "sidebar" }),
    ).not.toThrow()
  })

  it("keeps working after a sink has thrown", () => {
    let calls = 0
    setChatTelemetrySink(() => {
      calls++
      throw new Error("sink exploded")
    })
    emitChatEvent("chat_session_created", { session_id: "s1", source: "sidebar" })
    emitChatEvent("chat_session_created", { session_id: "s2", source: "sidebar" })
    expect(calls).toBe(2)
  })

  it("survives a payload that is not an object at all", () => {
    const seen = sinkSpy()
    expect(() =>
      // @ts-expect-error — defensive: JS callers exist at the edges
      emitChatEvent("chat_session_created", null),
    ).not.toThrow()
    expect(seen).toHaveLength(1)
    expect(seen[0].payload).toEqual({})
  })

  it("survives an unknown event name without emitting it", () => {
    const seen = sinkSpy()
    expect(() =>
      // @ts-expect-error — not in the vocabulary
      emitChatEvent("ask_chip_hovered", { chip_id: "c1" }),
    ).not.toThrow()
    expect(seen).toHaveLength(0)
  })
})

describe("emitChatEventOnce", () => {
  it("emits an impression once per dedupe key", () => {
    const seen = sinkSpy()
    for (let i = 0; i < 5; i++) {
      emitChatEventOnce("s1:chip_invoice", "ask_chip_shown", {
        chip_id: "chip_invoice",
        chip_kind: "form",
        position: 0,
        source: "pack",
      })
    }
    expect(seen).toHaveLength(1)
  })

  it("forgets old keys rather than growing forever", () => {
    const seen = sinkSpy()
    for (let i = 0; i < 2100; i++) {
      emitChatEventOnce(`k${i}`, "ask_chip_shown", {
        chip_id: "c",
        chip_kind: "form",
        position: 0,
        source: "pack",
      })
    }
    expect(seen).toHaveLength(2100)
    // The very first key has been forgotten by now, so it emits again. That is
    // the intended trade: a double-count after thousands of impressions beats
    // an unbounded Set in a tab left open overnight.
    emitChatEventOnce("k0", "ask_chip_shown", {
      chip_id: "c",
      chip_kind: "form",
      position: 0,
      source: "pack",
    })
    expect(seen).toHaveLength(2101)
  })

  it("treats a different key as a different impression", () => {
    const seen = sinkSpy()
    emitChatEventOnce("s1:a", "ask_chip_shown", {
      chip_id: "a",
      chip_kind: "form",
      position: 0,
      source: "pack",
    })
    emitChatEventOnce("s2:a", "ask_chip_shown", {
      chip_id: "a",
      chip_kind: "form",
      position: 0,
      source: "pack",
    })
    expect(seen).toHaveLength(2)
  })
})

describe("privacy: no event can carry user content", () => {
  // Sweeps the ENTIRE vocabulary. For every event, every declared key is
  // handed a poisoned value and a set of undeclared content-bearing keys
  // is added on top. Nothing recognisable may reach the sink.
  it("cannot smuggle content through any declared key of any event", () => {
    for (const name of CHAT_EVENT_NAMES) {
      resetChatTelemetry()
      const seen = sinkSpy()
      const spec = CHAT_EVENT_SCHEMA[name]
      for (const poison of POISON) {
        const payload: Record<string, unknown> = {}
        for (const key of Object.keys(spec)) payload[key] = poison
        payload.message_text = poison
        payload.file_name = poison
        payload.answers = { q1: poison }
        emitChatEvent(name as ChatEventName, payload as never)
      }
      const serialized = JSON.stringify(seen)
      for (const poison of POISON) {
        expect(serialized, `${name} leaked ${JSON.stringify(poison)}`).not.toContain(poison)
      }
      expect(serialized).not.toContain("message_text")
      expect(serialized).not.toContain("file_name")
      expect(serialized).not.toContain("answers")
    }
  })

  it("cannot smuggle content through an enum key by casing or padding", () => {
    const seen = sinkSpy()
    emitChatEvent("attachment_upload_failed", {
      // @ts-expect-error — deliberate abuse
      reason: " http_error /home/jana/invoice.pdf",
      mime_kind: "pdf",
      size_bytes: 1024,
      source: "picker",
    })
    expect(JSON.stringify(seen)).not.toContain("invoice.pdf")
  })

  it("keeps every emitted value primitive — no nested objects reach a sink", () => {
    const seen = sinkSpy()
    for (const name of CHAT_EVENT_NAMES) {
      emitChatEvent(name as ChatEventName, { nested: { a: 1 } } as never)
    }
    for (const e of seen) {
      for (const v of Object.values(e.payload)) {
        expect(["string", "number", "boolean"]).toContain(typeof v)
      }
    }
  })
})

describe("hashedId", () => {
  it("produces an id-shaped token that does not contain the text", () => {
    const id = hashedId("q", "What is still unpaid at Vodafone?")
    expect(id).toMatch(/^q_[0-9a-f]{8}$/)
    expect(id).not.toContain("Vodafone")
  })

  it("is stable for the same text and different for different text", () => {
    expect(hashedId("q", "same")).toBe(hashedId("q", "same"))
    expect(hashedId("q", "same")).not.toBe(hashedId("q", "other"))
  })

  it("survives an id field's validation, so a fingerprint always lands", () => {
    const seen = sinkSpy()
    emitChatEvent("ask_chip_clicked", {
      chip_id: hashedId("q", "please wire the invoice to IBAN CZ65 0800 — urgent!"),
      chip_kind: "question",
      position: 0,
      source: "pack",
    })
    expect(seen[0].payload.chip_id).toMatch(/^q_[0-9a-f]{8}$/)
  })
})

describe("mimeKind", () => {
  it("classifies without ever consulting a filename", () => {
    expect(mimeKind("image/heic")).toBe("image")
    expect(mimeKind("application/pdf")).toBe("pdf")
    expect(mimeKind("text/csv")).toBe("text")
    expect(mimeKind("application/json")).toBe("text")
    expect(mimeKind("application/zip")).toBe("archive")
    expect(mimeKind("audio/mpeg")).toBe("audio")
    expect(mimeKind("application/x-whatever")).toBe("other")
    expect(mimeKind(undefined)).toBe("other")
  })
})

describe("uploadFailureReason", () => {
  it("maps the statuses the upload endpoint actually answers with", () => {
    expect(uploadFailureReason(413)).toBe("too_large")
    expect(uploadFailureReason(415)).toBe("unsupported_type")
    expect(uploadFailureReason(429)).toBe("rate_limited")
    expect(uploadFailureReason(500)).toBe("http_error")
    expect(uploadFailureReason(404)).toBe("http_error")
    expect(uploadFailureReason(undefined)).toBe("network")
    expect(uploadFailureReason(0)).toBe("network")
  })
})

describe("sink lifecycle", () => {
  it("resetChatTelemetry removes the sink and clears dedupe state", () => {
    const spy = vi.fn()
    setChatTelemetrySink(spy)
    emitChatEventOnce("k", "chat_session_titled", { session_id: "s1", source: "auto" })
    resetChatTelemetry()
    const seen = sinkSpy()
    emitChatEventOnce("k", "chat_session_titled", { session_id: "s1", source: "auto" })
    expect(spy).toHaveBeenCalledTimes(1)
    expect(seen).toHaveLength(1)
  })
})
