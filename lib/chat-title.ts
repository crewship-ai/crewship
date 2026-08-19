/**
 * Deriving a chat session's title from its first message.
 *
 * PRD `chat-as-a-primary-surface`, Step 2. `chats.title` has existed since the
 * first migration and nothing ever wrote it, so every conversation in every
 * list reads "Untitled session" — which makes a list of conversations unusable
 * at any size. The client derives the opening title from the first message and
 * PATCHes it (`PATCH /api/v1/agents/{agentId}/chats/{chatId}`); a person can
 * correct it afterwards, and a corrected title is never overwritten.
 *
 * This module is the derivation and nothing else: no React, no fetch. The
 * edges — an empty message, a paste full of newlines, a 3 kB single token, a
 * message that is only a photo — are decided here so they can be decided in a
 * table.
 *
 * Normalisation deliberately mirrors `normalizeChatTitle` in
 * `internal/api/agent_chats_rename.go`, so the string we send is the string
 * that comes back and the sidebar never flickers between two spellings of the
 * same title. The server remains the authority: callers render the `title` on
 * the PATCH response, not the one they derived.
 */

/** Soft cap on a derived title, in code points.
 *
 *  60 is a label: it fits a 240px sidebar row at the second line of an
 *  ellipsis, a notification subject and a command-palette hit. The server's cap
 *  is 200 runes, so a human editing a title afterwards has room this derivation
 *  never uses. */
export const SESSION_TITLE_MAX_CHARS = 60

/** U+200D ZERO WIDTH JOINER. Category Cf like the invisible characters the
 *  normaliser drops, but it is the glue inside emoji sequences (a family emoji
 *  is people joined by these), so it is exempted by name — exactly as the
 *  server exempts it. */
const ZERO_WIDTH_JOINER = "‍"

/** Whitespace, matching Go's `unicode.IsSpace` (the White_Space property).
 *  JavaScript's `\s` additionally matches U+FEFF, which Go classifies as Cf
 *  and therefore *drops* rather than folding to a space — so it is excluded
 *  here and falls through to the format-character branch below. */
const isSpace = (ch: string): boolean => /\s/u.test(ch) && ch !== "﻿"

/** Control (Cc) and invisible format (Cf) characters. Cf is what carries the
 *  RIGHT-TO-LEFT OVERRIDE that makes `safe<U+202E>gnp.exe` render as
 *  `safeexe.png` in a sidebar row, and the zero-width padding that makes two
 *  different titles look identical. */
const isControlOrFormat = (ch: string): boolean => /[\p{Cc}\p{Cf}]/u.test(ch)

/** A title has to say something. At least one letter, digit or pictograph —
 *  otherwise the candidate is punctuation, marks or separators, and naming a
 *  thread `???` or `...` is worse than leaving it untitled: it looks like a
 *  name and carries none of the information one. */
const hasSubstance = (s: string): boolean => /[\p{L}\p{N}\p{Extended_Pictographic}]/u.test(s)

/**
 * Fold a raw string into the single line a title is: every whitespace run
 * (including the newlines and tabs a paste brings along) becomes one space,
 * control and format characters are dropped, the result is trimmed.
 *
 * Exported for callers that need to compare a value against what the server
 * will store; the derivation below applies it first.
 */
export function normalizeTitleText(raw: string): string {
  let out = ""
  let pendingSpace = false
  for (const ch of raw) {
    if (isSpace(ch)) {
      // Fold, don't emit — a leading run is dropped, an interior run becomes
      // exactly one space, a trailing run never gets flushed.
      pendingSpace = out.length > 0
      continue
    }
    if (ch !== ZERO_WIDTH_JOINER && isControlOrFormat(ch)) continue
    if (pendingSpace) {
      out += " "
      pendingSpace = false
    }
    out += ch
  }
  return out
}

/**
 * Cut to the cap at a word boundary. The ellipsis is appended only when
 * something was actually cut.
 *
 * A single token longer than the cap has no boundary to cut at and is the one
 * case that breaks a word. Both alternatives are worse: keeping it whole makes
 * a base64 paste the title (and, past 200 runes, a 400 from the server), and
 * dropping it leaves a real message untitled.
 */
function truncateAtWordBoundary(text: string): string {
  const chars = Array.from(text)
  if (chars.length <= SESSION_TITLE_MAX_CHARS) return text
  const window = chars.slice(0, SESSION_TITLE_MAX_CHARS).join("")
  const lastSpace = window.lastIndexOf(" ")
  if (lastSpace > 0) {
    const head = window.slice(0, lastSpace).trimEnd()
    if (head) return head + "…"
  }
  return window + "…"
}

/** What a send offers the deriver: what was typed, and what was attached. */
export interface SessionTitleSource {
  /** The message text as sent. */
  text?: string | null
  /** File names of the message's attachments, in the order they were added.
   *  Only consulted when there is no usable text — a message that is only a
   *  photo is still a message, and the file name is the only thing it said. */
  attachmentNames?: (string | null | undefined)[]
}

/**
 * Derive a session title, or null when the message yields nothing usable.
 *
 * Null is a real answer and callers must honour it: leaving a session untitled
 * is better than titling it `…`.
 */
export function deriveSessionTitle(source: SessionTitleSource): string | null {
  const candidates: string[] = [source.text ?? "", ...(source.attachmentNames ?? []).map((n) => n ?? "")]
  for (const candidate of candidates) {
    const normalized = normalizeTitleText(candidate)
    if (!normalized || !hasSubstance(normalized)) continue
    return truncateAtWordBoundary(normalized)
  }
  return null
}
