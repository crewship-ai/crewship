/**
 * Turn a raw User-Agent into something a person can recognise.
 *
 * Used by Settings → Profile → Sessions, whose entire job is letting
 * someone scan a list and notice a login that isn't theirs. That framing
 * drives every decision here:
 *
 *  · A confidently WRONG label is worse than an honest unknown. "Safari on
 *    Windows" talks the reader out of investigating; the raw string makes
 *    them look closer. So anything unrecognised is shown verbatim.
 *  · No dependency. UA parsing libraries carry hundreds of regexes and a
 *    release cadence we would have to track, to resolve a handful of
 *    browsers on a screen that shows a few rows.
 *
 * Order is load-bearing. Every one of these strings lies about being
 * something else — Edge says Chrome, Chrome says Safari, Opera says both,
 * Android says Linux. The checks run most-specific first, and the tests
 * pin each individual lie.
 */

export type DeviceKind = "desktop" | "mobile" | "cli" | "unknown"

export interface DeviceDescription {
  /** Human label, e.g. "Chrome on macOS". Never empty. */
  label: string
  /** Form factor, for picking the row icon. */
  kind: DeviceKind
}

/** Longest raw string we will print before eliding. */
const MAX_RAW = 64

function osName(ua: string): string | null {
  // Android before Linux: an Android UA contains "Linux".
  if (/Android/i.test(ua)) return "Android"
  if (/iPhone/i.test(ua)) return "iPhone"
  if (/iPad/i.test(ua)) return "iPad"
  if (/CrOS/i.test(ua)) return "ChromeOS"
  if (/Windows NT|Win64|Windows/i.test(ua)) return "Windows"
  if (/Macintosh|Mac OS X|darwin/i.test(ua)) return "macOS"
  if (/Linux|X11/i.test(ua)) return "Linux"
  return null
}

function browserName(ua: string): string | null {
  // Most-specific first. Edg/ and OPR/ both also carry Chrome/; Chrome
  // also carries Safari/.
  if (/\bEdg(?:e|A|iOS)?\//i.test(ua)) return "Edge"
  if (/\bOPR\/|\bOpera\//i.test(ua)) return "Opera"
  if (/\bFirefox\/|\bFxiOS\//i.test(ua)) return "Firefox"
  if (/\bChrome\/|\bCriOS\//i.test(ua)) return "Chrome"
  if (/\bSafari\//i.test(ua)) return "Safari"
  return null
}

export function describeUserAgent(raw: string | null | undefined): DeviceDescription {
  const ua = (raw ?? "").trim()
  if (ua === "") return { label: "Unknown device", kind: "unknown" }

  const os = osName(ua)

  // Our own CLI identifies itself as `crewship/<version> (<goos>/<goarch>)`.
  // Matched by name, never inferred: a bare `Go-http-client/2.0` is very
  // probably us too, but "very probably" is not something to assert as fact
  // on a screen someone uses to decide whether they have been breached.
  if (/^crewship\//i.test(ua)) {
    return { label: os ? `Crewship CLI on ${os}` : "Crewship CLI", kind: "cli" }
  }

  const browser = browserName(ua)
  if (browser && os) {
    const kind: DeviceKind =
      os === "iPhone" || os === "iPad" || os === "Android" ? "mobile" : "desktop"
    return { label: `${browser} on ${os}`, kind }
  }

  // Recognised one half but not the other — still better than the blob.
  if (browser) return { label: browser, kind: "desktop" }
  if (os) return { label: os, kind: os === "Android" ? "mobile" : "desktop" }

  return {
    label: ua.length > MAX_RAW ? `${ua.slice(0, MAX_RAW - 1)}…` : ua,
    kind: "unknown",
  }
}
