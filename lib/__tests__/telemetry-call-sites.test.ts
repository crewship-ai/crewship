import { readFileSync, readdirSync, statSync } from "node:fs"
import { resolve, join, extname } from "node:path"

import { describe, expect, it } from "vitest"

import { CHAT_EVENT_NAMES } from "@/lib/telemetry"

/**
 * Instrumentation that is declared but never emitted is a lie told in a table.
 *
 * This test walks the UI source and works out which events actually have a
 * call site. Every name is either WIRED or explicitly listed as pending — and
 * the pending list has to state why, so a gap has to be argued for rather than
 * inherited. Wiring an event without moving it off this list fails here, which
 * is the point: the docs table and the code cannot drift.
 */

const ROOTS = ["components", "app", "hooks"]

/**
 * Events with no call site yet, and the reason.
 *
 * The two session events came off this list when their files came free:
 * `chat_session_created` fires from chat-panel.tsx (`ensureSession`, source
 * `composer`) and from chat-page-client.tsx (`handleNewSession` → `sidebar`,
 * `openInitialSession` → `deeplink`); `chat_session_titled` fires from
 * `autoTitleSession` once the server accepts the PATCH. What is left is not a
 * to-do about ownership but a surface that does not exist.
 */
const PENDING_WIRING: Record<string, string> = {
  chat_approval_shown:
    "the chat surface has no interactive approval card today: AskUserQuestion renders " +
    "deliberately inert (assistant-turn.test.tsx pins that), and approvals are decided " +
    "on /approvals. Declared so the card that lands emits the agreed name",
  chat_approval_decided: "same — no approve/deny handler exists inside chat yet",
}

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === "__tests__") continue
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      sourceFiles(full, out)
      continue
    }
    if (![".ts", ".tsx"].includes(extname(entry))) continue
    if (entry.includes(".test.")) continue
    out.push(full)
  }
  return out
}

const corpus = (() => {
  const cwd = process.cwd()
  const parts: string[] = []
  for (const root of ROOTS) parts.push(...sourceFiles(resolve(cwd, root)).map((f) => readFileSync(f, "utf8")))
  return parts.join("\n")
})()

const isWired = (name: string) => corpus.includes(`"${name}"`)

describe("every declared event is emitted, or says why not", () => {
  it("has no event that is neither wired nor listed as pending", () => {
    const orphans = CHAT_EVENT_NAMES.filter((n) => !isWired(n) && !(n in PENDING_WIRING))
    expect(orphans, "declared but never emitted, and not on the pending list").toEqual([])
  })

  it("has no pending entry that is actually wired now", () => {
    const stale = Object.keys(PENDING_WIRING).filter((n) => isWired(n))
    expect(
      stale,
      "wired at last — take it off PENDING_WIRING here and off the pending table in docs/guides/chat-telemetry.mdx",
    ).toEqual([])
  })

  it("has no pending entry for an event that no longer exists", () => {
    const ghosts = Object.keys(PENDING_WIRING).filter(
      (n) => !(CHAT_EVENT_NAMES as readonly string[]).includes(n),
    )
    expect(ghosts).toEqual([])
  })

  it("keeps the pending list small enough to be a to-do and not a design", () => {
    expect(Object.keys(PENDING_WIRING).length).toBeLessThanOrEqual(2)
  })
})

describe("the guide documents the vocabulary", () => {
  const guide = readFileSync(resolve(process.cwd(), "docs/guides/chat-telemetry.mdx"), "utf8")

  it("names every event", () => {
    const missing = CHAT_EVENT_NAMES.filter((n) => !guide.includes(n))
    expect(missing, "events shipped without a line in the guide").toEqual([])
  })

  it("says how to read an event", () => {
    // A vocabulary nobody can observe is what this page used to document. The
    // reader is the answer, so the page has to name it.
    expect(guide).toContain("__CREWSHIP_CHAT_TELEMETRY__")
  })

  it("documents no event that does not exist", () => {
    // Catches a rename that updated the code and left the table behind.
    const documented = [...guide.matchAll(/`(ask_[a-z_]+|attachment_[a-z_]+|chat_[a-z_]+|conversation_[a-z_]+)`/g)]
      .map((m) => m[1])
      .filter((n) => !n.endsWith("_"))
    const unknown = [...new Set(documented)].filter(
      (n) => !(CHAT_EVENT_NAMES as readonly string[]).includes(n),
    )
    expect(unknown).toEqual([])
  })
})
