/**
 * Waits for a Sheet being made from the message's workbook: `ready` says whether the message can go
 * as it should now (no Sheet in the making, and the Sheet's choice settled). Polls every 100 ms; after `limitMs` it gives
 * up waiting (Infinity: never), and an aborted `signal` ends the wait as the person's cancel.
 */
export function waitForSheet(ready: () => boolean, limitMs: number, signal?: AbortSignal): Promise<"ready" | "timed_out" | "cancelled"> {
  if (signal?.aborted) return Promise.resolve("cancelled")
  if (ready()) return Promise.resolve("ready")
  const ends = Date.now() + limitMs
  return new Promise(resolve => {
    const tick = () => {
      if (signal?.aborted) { resolve("cancelled"); return }
      if (ready()) { resolve("ready"); return }
      if (Date.now() >= ends) { resolve("timed_out"); return }
      setTimeout(tick, 100)
    }
    setTimeout(tick, 100)
  })
}

/**
 * Whether the composer's message may go now: "wait" while a Sheet of it is being made or its choice
 * is settling, "failed" while a Sheet of it could not be made and the person has not chosen to send the file as it is.
 * A failed Sheet never lets the workbook go to the agent as a plain file by itself.
 */
export function sheetSendGate(s: { making: number; checking: boolean; sinceMadeMs: number; ridesAlong: boolean; failed: number; /** The Sheet the message should carry waits only for its version (its editor is opening). */ opening?: boolean }): "ready" | "wait" | "failed" {
  if (s.making > 0 || s.checking || (s.sinceMadeMs <= 1000 && !s.ridesAlong) || (s.opening && !s.ridesAlong)) return "wait"
  return s.failed > 0 ? "failed" : "ready"
}
