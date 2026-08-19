/**
 * One random-id generator for the whole client.
 *
 * Three call sites minted uuids by hand — the chat page's draft session,
 * use-chat's turn ids, and the ask envelope's submission id — and all three
 * carried the same fallback: `crypto.randomUUID` when it exists, otherwise a
 * uuid assembled from `Math.random()`. The fallback is not decoration. The dev
 * clones are reached over plain HTTP and `crypto.randomUUID` is gated to
 * secure contexts, so on those hosts the Math.random path is the one that
 * actually runs, and it ran for ids that name a session.
 *
 * `crypto.getRandomValues` has no such gate — it is available in insecure
 * contexts too, and has been in every browser this app supports for a decade.
 * So the fallback that was reaching for Math.random had a CSPRNG sitting next
 * to it the whole time. CodeQL flagged the three sites as
 * js/insecure-randomness; the fix is not a suppression.
 *
 * With no crypto at all this throws instead of degrading. Every environment
 * the app runs in — browsers, jsdom, happy-dom, Node ≥ 19 — has at least
 * getRandomValues, so the throw documents a requirement rather than guarding a
 * case anyone will hit, and a loud failure beats a session id nobody can trust.
 */

const HEX: string[] = Array.from({ length: 256 }, (_, i) => i.toString(16).padStart(2, "0"))

export function randomUUIDv4(): string {
  const c: Crypto | undefined = typeof crypto !== "undefined" ? crypto : undefined

  if (c && typeof c.randomUUID === "function") {
    return c.randomUUID()
  }

  if (!c || typeof c.getRandomValues !== "function") {
    throw new Error("randomUUIDv4: no Web Crypto available (crypto.getRandomValues is required)")
  }

  const b = c.getRandomValues(new Uint8Array(16))
  // RFC 4122 §4.4: version 4 in the high nibble of byte 6, variant 10x in the
  // top bits of byte 8. Everything else stays exactly as the CSPRNG gave it.
  b[6] = (b[6] & 0x0f) | 0x40
  b[8] = (b[8] & 0x3f) | 0x80

  return (
    HEX[b[0]] + HEX[b[1]] + HEX[b[2]] + HEX[b[3]] + "-" +
    HEX[b[4]] + HEX[b[5]] + "-" +
    HEX[b[6]] + HEX[b[7]] + "-" +
    HEX[b[8]] + HEX[b[9]] + "-" +
    HEX[b[10]] + HEX[b[11]] + HEX[b[12]] + HEX[b[13]] + HEX[b[14]] + HEX[b[15]]
  )
}
