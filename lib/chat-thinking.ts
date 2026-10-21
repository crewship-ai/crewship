import type { Thinking } from "@/lib/chat-event-contract"

/** First line of an adapter-approved safe summary, for the collapsed header.
 *  Undefined for redacted/unavailable blocks and while still running, so no
 *  private or unfinished text is ever lifted into the header. */
export function thinkingHeadline(thinking: Thinking): string | undefined {
  if (thinking.visibility !== "summary" || thinking.state === "running") return undefined
  const first = (thinking.summary ?? "").split("\n")[0].trim()
  return first || undefined
}
