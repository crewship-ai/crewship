/**
 * Turning a composer draft + its uploaded attachments into the one string
 * that actually goes over the wire.
 *
 * Why a string at all: the upload endpoint puts the file in the agent's
 * container at `/output/<slug>/attachments/<chatId>/<filename>` and hands the
 * browser back `path` (`attachments/<chatId>/<filename>`) and `agent_path`
 * (the absolute one). Nothing else ever told the agent the file existed — the
 * WS `send_message` frame carried the user's text and nothing more, so an
 * attached document was uploaded, stored, and never mentioned. Uploading a
 * file the recipient is never told about is worse than refusing the upload.
 *
 * The payload is an ORDINARY USER MESSAGE, per
 * docs/prd/agent-ask-packs-and-document-intake.md §7: that is the only shape
 * that works across every CLI adapter without the agent being trained for a
 * new envelope. So the file references are appended to the message text.
 *
 * The RELATIVE path is what gets named, not the absolute one. The agent's
 * working directory IS its output directory — `preparePreflightDirs` sets
 * `workDir = /output/<slug>` (internal/orchestrator/orchestrator_run.go) — so
 * `attachments/<chatId>/<file>` opens with no further guessing, it is the form
 * the PRD fixed (§7.4), and it keeps the crew slug out of the user's own
 * transcript.
 *
 * Pure and dependency-free on purpose: the exact bytes the agent receives are
 * worth pinning in a table-driven test, and worth NOT rebuilding inline in a
 * component where the next edit quietly changes them.
 */

/** An attachment as the composer store knows it, narrowed to what the wire
 *  format needs. `path` is the server-assigned relative path; it only exists
 *  once the upload has come back. */
export interface OutgoingAttachment {
  name: string
  /** `attachments/<chatId>/<filename>`, from the upload response. */
  path?: string
  status?: "uploading" | "ready" | "error"
}

/** Control characters (C0 + DEL) are stripped from a path before it is named.
 *  A filename is a sanitised basename by the time the server returns it
 *  (internal/api/proxy_attachments.go), but a newline in one would split a
 *  list entry in two and invent a file that does not exist. Mirrors the PRD's
 *  rule for rendered values (§7.3). */
const CONTROL_CHARS = /[\u0000-\u001F\u007F]/g

function cleanPath(path: string): string {
  return path.replace(CONTROL_CHARS, "").trim()
}

/** The attachments that can actually be named in a message: the upload
 *  finished and the server gave us a path. An upload still in flight has no
 *  path yet, and a failed one has no file behind it. */
export function sendableAttachments(attachments: OutgoingAttachment[]): OutgoingAttachment[] {
  return attachments.filter((a) => a.status !== "error" && !!a.path && !!cleanPath(a.path))
}

/** True while at least one upload is still running. The composer refuses to
 *  send in that window rather than sending a message that names some of the
 *  files the user attached — which is the same defect this module fixes,
 *  just narrower. */
export function hasPendingUploads(attachments: OutgoingAttachment[]): boolean {
  return attachments.some((a) => a.status === "uploading")
}

/**
 * The appended block. One shape, two lead-ins (singular/plural).
 *
 * Wording is part of the fix, so it is pinned here rather than left to a
 * template literal in a component:
 *
 *   · It is first person and past tense — a human saying what they just did.
 *     The recipient is an agent reading a user turn; anything in the
 *     imperative ("Read the following file", "<attachments>…") reads as an
 *     injected system directive, and an adapter that has been prompt-hardened
 *     against exactly that shape is entitled to ignore it.
 *   · It states the path and says what the path is relative to, because the
 *     agent's CWD is the one fact it cannot infer from the string.
 *   · One path per line, unquoted and unescaped. Spaces, quotes and brackets
 *     in a filename are common and the line break is the only delimiter that
 *     none of them can forge.
 *   · The block is separated from the user's own words by a blank line, so
 *     the transcript reads as a message with a postscript rather than one
 *     run-on paragraph.
 */
function attachmentBlock(paths: string[]): string {
  const lead =
    paths.length === 1
      ? "I've attached a file to this message. The path is relative to your working directory:"
      : `I've attached ${paths.length} files to this message. The paths are relative to your working directory:`
  return `${lead}\n\n${paths.map((p) => `- ${p}`).join("\n")}`
}

/**
 * The content the WS frame carries: the user's text, plus a block naming
 * every uploaded attachment by its agent-visible path.
 *
 * With no sendable attachments the text is returned unchanged — byte for
 * byte, no stray block, no trailing newline. That is the overwhelmingly
 * common case and it must stay indistinguishable from before this existed.
 *
 * With attachments and no text (a photo with no caption) the block stands
 * alone: an attachment-only message is a real message, and dropping it was
 * the second half of the same bug.
 */
export function composeMessageWithAttachments(
  text: string,
  attachments: OutgoingAttachment[],
): string {
  const paths = sendableAttachments(attachments).map((a) => cleanPath(a.path!))
  if (paths.length === 0) return text
  const block = attachmentBlock(paths)
  const body = text.trim()
  return body ? `${body}\n\n${block}` : block
}
