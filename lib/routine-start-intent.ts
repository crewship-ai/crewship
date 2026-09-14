import { nanoid } from "nanoid"

/** One manual start, including retries whose response may have been lost.
 * Keep this object in a ref: React state alone cannot guard two submissions
 * before the next render. The server remains the authority for deduplication.
 */
export class RoutineStartIntent {
  private pending = new Map<string, { key: string; busy: boolean }>()

  begin(url: string, body: unknown) {
    const signature = JSON.stringify([url, body])
    let intent = this.pending.get(signature)
    if (intent?.busy) return null
    if (!intent) {
      intent = { key: nanoid(), busy: false }
      this.pending.set(signature, intent)
    }
    intent.busy = true
    const current = intent
    let finished = false
    return {
      key: current.key,
      finish: (accepted: boolean) => {
        if (finished) return
        finished = true
        current.busy = false
        // Only a confirmed run ID closes the logical start. A retry after a
        // transport error or unreadable response must recover the same run.
        if (accepted) this.pending.delete(signature)
      },
    }
  }
}
