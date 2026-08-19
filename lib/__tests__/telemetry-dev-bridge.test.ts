import { readFileSync } from "node:fs"
import { resolve } from "node:path"

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  CHAT_EVENT_NAMES,
  CHAT_EVENT_SCHEMA,
  emitChatEvent,
  installChatTelemetryDevBridge,
  resetChatTelemetry,
  type ChatEventName,
  type ChatTelemetryDevBridge,
} from "@/lib/telemetry"

/**
 * The buffer was write-only.
 *
 * `peekChatEvents()` / `drainChatEvents()` were module exports attached to
 * nothing: no console, no CLI, no UI could read a single event, which was
 * found when somebody tried to demonstrate the funnel and had nowhere to
 * look. A measurement nobody can read answers no question.
 *
 * The reader is a dev-only `window` binding. This file holds it to three
 * things:
 *
 *   · it returns the events that were actually emitted;
 *   · it does not become a second way for user content to escape — the
 *     privacy sweep is run again THROUGH the reader, over the whole
 *     vocabulary, not one example;
 *   · it is absent from a production build, and the module still makes no
 *     network call of any kind.
 */

/** A stand-in for `window`, so the install can be driven without touching the
 *  real one and without relying on which environment the suite runs under. */
type Host = { __CREWSHIP_CHAT_TELEMETRY__?: ChatTelemetryDevBridge }

function freshHost(): Host {
  return {}
}

function bridgeOn(host: Host): ChatTelemetryDevBridge {
  const b = host.__CREWSHIP_CHAT_TELEMETRY__
  if (!b) throw new Error("no bridge installed on host")
  return b
}

/** Same poison list the vocabulary sweep uses — the reader must not widen it. */
const POISON = [
  "please wire the invoice to IBAN CZ65 0800",
  "Acme Q3 invoice.pdf",
  "jana@example.com",
  "my secret answer",
]

beforeEach(() => {
  resetChatTelemetry()
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe("the dev bridge exists and reads the buffer", () => {
  it("installs onto the host and returns true outside production", () => {
    const host = freshHost()
    expect(installChatTelemetryDevBridge(host)).toBe(true)
    expect(host.__CREWSHIP_CHAT_TELEMETRY__).toBeTypeOf("object")
  })

  it("peek returns the events that were emitted, without clearing them", () => {
    const host = freshHost()
    installChatTelemetryDevBridge(host)
    emitChatEvent("chat_session_created", { session_id: "s1", agent_id: "a1", source: "sidebar" })
    emitChatEvent("chat_session_titled", { session_id: "s1", source: "auto" })

    const bridge = bridgeOn(host)
    expect(bridge.peek().map((e) => e.name)).toEqual(["chat_session_created", "chat_session_titled"])
    // Still there — peek is a look, not a take.
    expect(bridge.peek()).toHaveLength(2)
    expect(bridge.peek()[0].payload).toEqual({ session_id: "s1", agent_id: "a1", source: "sidebar" })
  })

  it("drain returns the events and empties the buffer", () => {
    const host = freshHost()
    installChatTelemetryDevBridge(host)
    emitChatEvent("conversation_search_run", { result_count: 3, has_results: true, source: "palette" })

    const bridge = bridgeOn(host)
    expect(bridge.drain().map((e) => e.name)).toEqual(["conversation_search_run"])
    expect(bridge.drain()).toEqual([])
    expect(bridge.peek()).toEqual([])
  })

  it("hands out copies, so a console session cannot write back into the buffer", () => {
    const host = freshHost()
    installChatTelemetryDevBridge(host)
    emitChatEvent("chat_session_created", { session_id: "s1", source: "sidebar" })

    const bridge = bridgeOn(host)
    const taken = bridge.peek()
    // Whatever somebody types at a console prompt, the ring buffer is not it.
    ;(taken[0].payload as Record<string, unknown>).session_id = "please wire the invoice"
    taken.push({ name: "chat_session_titled", payload: {}, ts: 0 })

    expect(bridge.peek()).toHaveLength(1)
    expect(bridge.peek()[0].payload.session_id).toBe("s1")
  })

  it("reset clears the buffer, so a demo can start from nothing", () => {
    const host = freshHost()
    installChatTelemetryDevBridge(host)
    emitChatEvent("chat_session_created", { session_id: "s1", source: "sidebar" })
    const bridge = bridgeOn(host)
    bridge.reset()
    expect(bridge.peek()).toEqual([])
  })

  it("summary counts every declared event, zeros included", () => {
    // The second reader is a maintainer asking whether a surface is used at
    // all. A surface with no events must show as 0 rather than as an absent
    // key, or 'never used' and 'never declared' look the same.
    const host = freshHost()
    installChatTelemetryDevBridge(host)
    emitChatEvent("ask_chip_shown", { chip_id: "c1", chip_kind: "form", position: 0, source: "pack" })
    emitChatEvent("ask_chip_shown", { chip_id: "c2", chip_kind: "form", position: 1, source: "pack" })
    emitChatEvent("ask_chip_clicked", { chip_id: "c1", chip_kind: "form", position: 0, source: "pack" })

    const summary = bridgeOn(host).summary()
    expect(Object.keys(summary)).toEqual([...CHAT_EVENT_NAMES])
    expect(summary.ask_chip_shown).toBe(2)
    expect(summary.ask_chip_clicked).toBe(1)
    expect(summary.chat_approval_shown).toBe(0)
  })

  it("json is pasteable and round-trips the buffer", () => {
    const host = freshHost()
    installChatTelemetryDevBridge(host)
    emitChatEvent("attachment_uploaded", { mime_kind: "pdf", size_bytes: 1024, source: "camera", duration_ms: 900 })

    const parsed = JSON.parse(bridgeOn(host).json())
    expect(parsed).toHaveLength(1)
    expect(parsed[0].name).toBe("attachment_uploaded")
    expect(parsed[0].payload.mime_kind).toBe("pdf")
  })

  it("carries the vocabulary, so the console can say what a field means", () => {
    const host = freshHost()
    installChatTelemetryDevBridge(host)
    const bridge = bridgeOn(host)
    expect([...bridge.names]).toEqual([...CHAT_EVENT_NAMES])
    expect(bridge.schema).toBe(CHAT_EVENT_SCHEMA)
    expect(bridge.help).toContain("__CREWSHIP_CHAT_TELEMETRY__")
  })
})

describe("the bridge is absent from a production build", () => {
  it("installs nothing when NODE_ENV is production", () => {
    vi.stubEnv("NODE_ENV", "production")
    const host = freshHost()
    expect(installChatTelemetryDevBridge(host)).toBe(false)
    expect(host.__CREWSHIP_CHAT_TELEMETRY__).toBeUndefined()
    expect("__CREWSHIP_CHAT_TELEMETRY__" in host).toBe(false)
  })

  it("is guarded by the literal Next inlines, so the branch is dead code in prod", () => {
    // The guard has to be a bare `process.env.NODE_ENV` comparison for the
    // bundler to fold it away. Reading it through a variable, a helper or an
    // env object would keep the bridge — and the whole vocabulary object it
    // closes over — in the shipped bundle.
    const src = readFileSync(resolve(process.cwd(), "lib/telemetry.ts"), "utf8")
    expect(src).toMatch(/process\.env\.NODE_ENV\s*===?\s*"production"/)
  })

  it("does nothing on the server, where there is no window", () => {
    // Client components are also rendered on the server in a Next build; the
    // module-scope install must be a no-op there rather than a crash.
    expect(installChatTelemetryDevBridge(null)).toBe(false)
  })
})

describe("privacy: the reader is not a second way out", () => {
  it("cannot surface content through any declared key of any event", () => {
    // The whole vocabulary, poisoned key by key, read back THROUGH the bridge.
    // The guarantee is structural at emit; this proves the reader did not
    // quietly reopen it (by keeping the raw payload, say, or by re-reading the
    // caller's object).
    for (const name of CHAT_EVENT_NAMES) {
      resetChatTelemetry()
      const host = freshHost()
      installChatTelemetryDevBridge(host)
      const spec = CHAT_EVENT_SCHEMA[name]
      for (const poison of POISON) {
        const payload: Record<string, unknown> = {}
        for (const key of Object.keys(spec)) payload[key] = poison
        payload.message_text = poison
        payload.file_name = poison
        payload.answers = { q1: poison }
        emitChatEvent(name as ChatEventName, payload as never)
      }

      const bridge = bridgeOn(host)
      const readBack = [JSON.stringify(bridge.peek()), bridge.json(), JSON.stringify(bridge.summary())].join("\n")
      for (const poison of POISON) {
        expect(readBack, `${name} leaked ${JSON.stringify(poison)} through the reader`).not.toContain(poison)
      }
      expect(readBack).not.toContain("message_text")
      expect(readBack).not.toContain("file_name")
      expect(readBack).not.toContain("answers")
    }
  })

  it("keeps every value it hands back primitive", () => {
    const host = freshHost()
    installChatTelemetryDevBridge(host)
    for (const name of CHAT_EVENT_NAMES) {
      emitChatEvent(name as ChatEventName, { nested: { a: 1 }, message_text: "hello" } as never)
    }
    for (const e of bridgeOn(host).peek()) {
      for (const v of Object.values(e.payload)) {
        expect(["string", "number", "boolean"]).toContain(typeof v)
      }
    }
  })
})

describe("no network transport, still", () => {
  it("makes no request while emitting the whole vocabulary and reading it back", () => {
    const fetchSpy = vi.fn()
    const sendBeacon = vi.fn()
    const xhrOpen = vi.fn()
    const xhrSend = vi.fn()
    const wsCtor = vi.fn()

    vi.stubGlobal("fetch", fetchSpy)
    vi.stubGlobal("XMLHttpRequest", class {
      open = xhrOpen
      send = xhrSend
      setRequestHeader = vi.fn()
    })
    vi.stubGlobal("WebSocket", wsCtor)
    vi.stubGlobal("navigator", { ...globalThis.navigator, sendBeacon })

    const host = freshHost()
    installChatTelemetryDevBridge(host)
    for (const name of CHAT_EVENT_NAMES) {
      emitChatEvent(name as ChatEventName, { session_id: "s1", position: 0, result_count: 0 } as never)
    }
    const bridge = bridgeOn(host)
    bridge.peek()
    bridge.summary()
    bridge.json()
    bridge.drain()
    bridge.reset()

    expect(fetchSpy).not.toHaveBeenCalled()
    expect(sendBeacon).not.toHaveBeenCalled()
    expect(xhrOpen).not.toHaveBeenCalled()
    expect(xhrSend).not.toHaveBeenCalled()
    expect(wsCtor).not.toHaveBeenCalled()

    vi.unstubAllGlobals()
  })

  it("names no transport anywhere in the module's code", () => {
    // Comments are stripped first: the file's own doc comment says the word
    // "fetch" precisely to promise it never calls one, and a grep that fails
    // on the promise is a grep nobody keeps.
    const raw = readFileSync(resolve(process.cwd(), "lib/telemetry.ts"), "utf8")
    const code = raw.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "")
    for (const forbidden of [
      /\bfetch\s*\(/,
      /XMLHttpRequest/,
      /sendBeacon/,
      /new\s+WebSocket/,
      /EventSource/,
      /\bimport\s*\(/,
    ]) {
      expect(code, `lib/telemetry.ts must contain no ${forbidden}`).not.toMatch(forbidden)
    }
  })
})
