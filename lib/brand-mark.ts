/**
 * Crewship brand mark — path data and the split into its three sails.
 *
 * SINGLE SOURCE OF TRUTH for the mark's geometry. `SAIL_PATH` used to live
 * in components/branding/crewship-logo.tsx; it moved here so that the
 * animated mark can derive from the same string the static logo renders,
 * instead of a second copy that drifts the first time the logo is redrawn.
 * The component re-exports it, so existing importers are unaffected.
 *
 * The same `<path d="...">` is also baked into the static assets:
 *   - app/icon.svg                — favicon (navy squircle + white sail)
 *   - public/logo.svg             — currentColor-only silhouette
 *   - public/brand/*.svg          — 4 color variants for marketing reach
 *
 * If the mark ever changes, edit the path in ALL of those locations so the
 * favicon, inline UI logo, and external assets stay in lockstep.
 */

export const SAIL_PATH = "M114.2 2.1c3.4 4.7 16.2 32 21.2 44.9l3.2 7.5c.7 1.6 2.3 5.9 3.5 9.5 1.1 3.6 2.7 7.8 3.4 9.5.7 1.6 2.1 5.9 3 9.5s2 7.2 2.5 8.1 1.8 5.6 3 10.5c1.3 4.9 2.6 9.3 3 9.9s1.7 6.6 2.9 13.5c1.3 6.9 2.6 13.4 3.1 14.5a160 160 0 0 1 4.1 22.5c.6 4.1 1.5 8.4 2 9.5a545 545 0 0 1 4.9 50.1c0 2.8.6 5.6 1.3 6.3 1.7 1.7 1.8 90.6 0 94.2-.6 1.3-1.6 9.8-2.2 18.9-1.4 19.9-3 33.8-4.1 36.5-.4 1.1-1.8 9.2-3 18-1.3 8.8-2.7 16.7-3.1 17.5s-1.7 6.8-2.9 13.3a92 92 0 0 1-3 13.5c-.5 1-1.9 6.2-3.1 11.6a71 71 0 0 1-3.6 12.5c-.7 1.4-1.3 3.3-1.3 4.1 0 2.5-2.9 13.2-4.5 16.6-.8 1.7-2 5.5-2.6 8.5s-2.1 7.6-3.3 10.4a63 63 0 0 0-3.6 10 33 33 0 0 1-2 6c-.4.5-1.8 4.1-3 8a60 60 0 0 1-3 8c-.4.5-1.7 3.9-3 7.5-1.2 3.6-2.6 7-3.1 7.6a32 32 0 0 0-2.8 6.5c-1 3-2.5 6.7-3.4 8.4-3.7 7.4-8.7 18.4-8.7 19.2a628 628 0 0 1-27.5 52.8l-10 17-8.5 14c-.9 1.6-3.7 5.9-6.1 9.5l-5.9 9.2a90 90 0 0 1-6.1 9 206 206 0 0 1-13.8 19.6c-4.5 5.7-8.1 10.6-8.1 10.9 0 .8-8.2 10.4-14.9 17.6-3 3.1-5.2 5.7-4.9 5.7 1.2 0 6.5-2.7 10.5-5.3l10-5.9 10.3-5.6 12.5-6.7 11-5.8c1.7-.9 6.2-3.1 10-4.8l11-5c2.2-1 6.5-2.6 9.5-3.6a42 42 0 0 0 7.5-2.9c1.1-.7 4.9-2 8.5-3s7-2 7.5-2.5a76 76 0 0 1 10-2.9c5-1.3 9.5-2.6 10-3s6.4-1.8 13-3 12.9-2.6 14-3.1c2.4-1.1 27.6-4.9 32.5-4.9 2 0 4-.4 4.5-.9 4-3.7 101.6-3.8 110-.1 1.1.4 4.7 1.1 8 1.4a211 211 0 0 1 37.5 7.2c4.1.9 8.9 2.2 10.5 2.9 1.7.7 6.4 2.2 10.5 3.5a70 70 0 0 1 15 6c10.1 4.2 20 9.3 20 10.3q0 .9.7.4c.9-1 1.3-5.7.4-5.7-.4 0-1.3-10.7-2-23.8-.7-13-1.7-24.2-2.1-24.7s-.8-4-.8-7.7c0-3.6-.3-7.1-.7-7.7s-.6-3.9-.5-7.4c.1-3.4 0-6.9-.3-7.7a209 209 0 0 1-1.8-17c-.6-8.5-1.6-16-2.1-16.7a8 8 0 0 1-.7-4.5c.1-1.8-.1-4-.4-4.8a235 235 0 0 1-2.2-18c-1-9.1-2.2-17.1-2.7-17.8s-.8-2.8-.5-4.7.2-3.5-.2-3.5a76 76 0 0 1-2.6-14.8 91 91 0 0 0-2.7-15.9 6 6 0 0 1-.7-3.5c.2-1.3.1-2.8-.3-3.3-.3-.6-1.3-6.1-2.2-12.3s-2.1-12.1-2.6-13-1.8-7.8-3-15.2c-1.3-7.4-2.6-14.2-3.1-15s-1.8-6.9-3-13.5-2.6-12.5-2.9-13c-.4-.6-1.8-6-3-12a93 93 0 0 0-3-12c-.4-.6-1.8-5.3-3-10.5s-2.5-10-2.9-10.5a70 70 0 0 1-3-10c-1.2-5-2.9-10.4-3.6-12-.7-1.7-2.1-5.9-3.1-9.5s-2.1-7-2.4-7.5a78 78 0 0 1-3-8.5c-1.2-4.1-2.5-8-3-8.5s-1.7-4.2-3-8a44 44 0 0 0-3.1-8.1c-.5-.6-1.8-4-2.9-7.5a90 90 0 0 0-3.4-9.4 52 52 0 0 1-4.6-12q-.1-1.4-.8-1.5c-.4 0-1.4-1.7-2.1-3.8s-2.6-6.6-4.3-10.2l-5.6-12.4a736 736 0 0 0-39.2-72.7c0-.5-2.3-4.2-5-8.3s-5-7.7-5-8c0-.8-6.5-10.1-9.7-13.9a12 12 0 0 1-2.3-3.9 54 54 0 0 0-6-9c-3.3-4.3-6-8.3-6-8.8q0-1-1-1-.9-.1-1-1.1c0-.5-3.6-5.5-8-11s-8-10.3-8-10.8-1.3-2.1-3-3.4c-1.6-1.4-3-2.9-3-3.4-.2-4.6-77.1-81.4-84.5-84.3-1.1-.4-3.1-2-4.5-3.5-3.7-4-7.1-5.7-4.8-2.4m166.6 1.3C282 5 283 6.9 283 7.7s1.1 3 2.3 5.1c1.3 2 3.4 5.7 4.5 8.2l3.1 6a508 508 0 0 1 15.6 34.5c.9 2.7 2 5.4 2.4 6 .5.5 1.9 3.9 3.1 7.5s2.6 7 3.1 7.6 1.8 4.2 3 8 2.5 7.3 2.9 7.9c.4.5 1.8 4.4 3 8.5s2.6 7.9 3 8.5a79 79 0 0 1 2.9 9c1.3 4.4 2.9 9.3 3.6 11s2.1 5.9 3 9.5 2 6.9 2.5 7.5 1.8 5 3 10a70 70 0 0 0 3 10c.4.5 1.7 5 2.9 10s2.6 9.7 3 10.5c.5.9 1.8 6 3 11.5s2.7 10.9 3.2 12c1.1 2.2 4.5 15.8 8.6 34.5a158 158 0 0 0 3.3 13.1c.4.9 1.8 6.5 3 12.5s2.5 11.8 3 12.9a82 82 0 0 1 2.5 10.5 127 127 0 0 0 2.6 12.4 93 93 0 0 1 3.4 15.1c.9 5.5 1.9 10.7 2.2 11.5s.5 2.1.4 2.8q-.1 1.4.7 2.5c.6.7 1.5 5.4 2.2 10.5a34 34 0 0 0 2.1 9.7q.6.6.2 2.5c-.3 1.1-.2 2.8.2 3.7s.9 2.7 1 3.8.4 2.7.7 3.5c.4.8 1.2 5.1 1.8 9.5a72 72 0 0 0 2.6 12.5 574 574 0 0 1 5.8 36.1 154 154 0 0 1 3.1 19.9c1 8.5 2.2 16 2.7 16.7s.9 2.5.7 4c-.1 1.5.1 3.5.4 4.3s1.3 9.4 2.2 19 2.1 18.4 2.6 19.5c.8 2 1.9 11.6 4 35.5.5 6.9 1.4 13.2 1.8 14s1.9 15.1 3.2 31.7c2.4 30.2 2.4 30.2 8.4 30.7L460 633a116 116 0 0 1 20.4 2.1c.6.5 2.4.9 4.2.9 4.2 0 26.3 3.7 28.9 4.9 1.1.5 6.5 1.8 12 3s10.9 2.6 12 3.1c1.8.8 12.4 4.6 15 5.4.6.1 2.7 1 4.9 1.9 2.1.9 4.1 1.7 4.5 1.7.9 0 25.9 12.7 34.4 17.4 6.8 3.8 6.8 3.8 6.2-2.5-.4-3.5-1-7.3-1.5-8.4s-1.8-10.1-3-20-2.5-18.9-3-20-1.8-9.6-3.1-19c-1.2-9.3-2.5-17.7-3-18.5s-1.8-8.5-3-17-2.5-16-2.9-16.5-1.5-5.7-2.4-11.5a128 128 0 0 0-2.3-12c-.3-.8-.5-2.3-.4-3.3s-.2-2.4-.7-3-1.5-4.6-2.2-8.7a82 82 0 0 0-2.1-11.4q.1-1-.5-2.5a180 180 0 0 0-5.1-20.1c-.3-.8-.5-1.9-.4-2.4q.1-1-.5-2.5c-.4-.9-.9-2.3-1-3.1-1.1-5.5-3.6-14.8-4.4-16.3q-1-1.7-.4-2.8c.3-.5 0-1.6-.6-2.4q-1-1.3-.5-1.5t-.2-1.3c-.5-.6-1.6-3.9-2.3-7.2s-1.8-6.6-2.3-7.3q-.8-1-.2-1.2t-.2-1.3c-.5-.6-1.8-4.8-2.9-9.2s-2.3-8.3-2.8-8.6q-.7-.7-.2-1.7t-.3-1.6c-.5-.3-1.7-3.8-2.6-7.8s-2.2-7.9-2.8-8.6q-.8-1-.2-1.2t-.3-1.3c-.5-.6-1.8-4.1-2.7-7.7s-2.1-7-2.6-7.5a42 42 0 0 1-2.4-7c-1-3.3-2.3-7.1-3.1-8.5-.7-1.4-2.3-5.7-3.5-9.5a50 50 0 0 0-3-8 50 50 0 0 1-2.8-7.5 77 77 0 0 0-4.1-10.1c-1.1-2-2-4.2-2-4.8 0-.7-1.1-3.6-2.4-6.4l-5-10.8-2.6-6.2a536 536 0 0 0-30.3-59.9c-.3-1-2.2-4.3-4.2-7.3a56 56 0 0 1-3.5-6.1c0-.2-1.6-2.8-3.5-5.7a25 25 0 0 1-3.5-6.4q-.1-1.2-1-1.3c-.6 0-1.5-1.2-2.1-2.7a41 41 0 0 0-4.4-7.4c-1.9-2.5-3.5-5-3.5-5.4s-2.7-4.5-6-9.1-6-8.6-6-9.1-1.3-2.4-2.8-4.3a322 322 0 0 1-14.6-19.5A241 241 0 0 0 411 139a333 333 0 0 0-22.7-28.4A236 236 0 0 1 374 93.1c0-.5-2.2-2.9-5-5.5s-5-5.1-5-5.7c0-1.5-56.6-57.9-58.1-57.9a35 35 0 0 1-7.9-6.3C287.7 8 278.2.1 280.8 3.4m163.1 24.8a168 168 0 0 1 10 18.8 639 639 0 0 1 24.3 52.5c1 1.6 1.8 3.6 1.8 4.4 0 .7 1 3.4 2.3 6a90 90 0 0 1 4.2 10.6 59 59 0 0 0 3 8c.7 1.1 2.1 4.5 3 7.5 1 3 2.1 5.9 2.5 6.5s1.7 4.1 3 8c1.2 3.8 2.5 7.4 3 8s1.8 4.4 3 8.5 2.6 7.9 3 8.5 1.7 4.1 2.9 8c1.2 3.8 2.9 8.3 3.6 10s2.1 5.9 3.1 9.5 2.1 7 2.5 7.5c.7 1 6.1 16.3 9.9 28 2.2 6.7 5.8 17.2 6.7 19.5l.8 2 .8 2c.3.8 1.4 4.2 2.3 7.5a29 29 0 0 0 3.7 9.1q-.2.4.2 1.6l2.1 7.3a42 42 0 0 0 3.3 8.7l.5 2 2.1 7.3a76 76 0 0 0 3 10c0 .5.6 2 1.3 3.2a9 9 0 0 1 1.2 3.7c0 .8.5 2.7 1.2 4.3a51 51 0 0 1 2.3 6.8l1.3 3.8c.7 1.6 1.2 3.4 1.2 4.2s.5 2.6 1.2 4.2c.6 1.5 1.2 3 1.2 3.3l.9 1.7q.8 1.2.2 2.2-.4.9.4 1.5c.5.3 1.5 3 2.1 5.8a27 27 0 0 0 2.1 6.4q.8 1 .3 1.7t.2 1.9l1 1.8a37 37 0 0 1 2.9 9.7c.4 2.4 1.1 4.3 1.7 4.3q1 .1.5 1.4t.3 2.2c.4.5 1.5 4.3 2.5 8.4a58 58 0 0 0 2.5 8.5c.4.5 1.7 5.3 3 10.5s2.6 9.9 3 10.5 1.7 5.3 2.9 10.5 2.6 10.4 3.1 11.5 1.9 6.3 3 11.5 2.5 10.2 2.9 11a92 92 0 0 1 3.1 12.5 76 76 0 0 0 3.1 12c.4.5 1.7 6.1 2.9 12.4 1.2 6.2 2.5 12.1 3 13s1.8 7 3 13.6 2.5 12.8 3 13.7 1.9 7.5 3 14.5 2.5 13.7 3.1 14.8c.8 1.7 1.7 7.2 4.5 27.5.9 6.2 1.8 7 8.3 7.1 10.1.1 55.7 7.5 60.5 9.7 1.2.6 6.3 2 11.5 3.2s10.2 2.6 11 3c.9.5 5 1.9 9.1 3.1s8 2.5 8.5 2.9 3.9 1.8 7.5 3c7.5 2.5 33.6 15.4 39.4 19.3 2.1 1.5 4.4 2.7 5.1 2.7s2.9 1.4 5 3c4.2 3.4 5.9 3.2 2.8-.3-1.1-1.2-3-6-4.2-10.7a94 94 0 0 0-3.2-10.2c-.4-.9-1.8-5.9-3-11a72 72 0 0 0-2.9-10.3c-.4-.6-1.7-5.3-2.9-10.5a94 94 0 0 0-3.1-11.2c-.5-.9-1.8-5.6-3-10.5a95 95 0 0 0-3-10.8c-.6-1.1-1.9-6.1-3.1-11a48 48 0 0 0-2.9-10c-.4-.6-1.5-4.6-2.5-9s-2.3-9.4-3-11c-.7-1.7-2.2-6.8-3.4-11.5-1.3-4.7-2.6-9.3-3.1-10.2a68 68 0 0 1-3-9.8 68 68 0 0 0-3-9.8c-.6-.9-2.1-5.5-3.5-10.2q-3.3-11.5-6.5-20.5a7 7 0 0 1-.6-2.6 5 5 0 0 0-1-2 24 24 0 0 1-2.4-6.2c-.7-2.6-1.6-5-2.1-5.3s-.6-1.1-.3-1.8a2 2 0 0 0-.5-2.1q-1-.6-.6-1.5t-.5-1.5c-.5-.3-1-1.2-1-1.9 0-.8-.8-3.1-1.7-5.2l-1.8-4.9c-.1-.6-.9-2.7-1.8-4.7a22 22 0 0 1-1.7-5c0-.8-.7-2.4-1.6-3.6-.8-1.2-1.2-2.2-.8-2.2q.5-.1-.3-1.3c-.5-.6-1.8-3.8-2.8-7-1.1-3.1-2.3-5.7-2.8-5.7q-.8-.2-.4-1.5t-.3-2c-.5-.3-1.4-2.2-2-4.3-1.1-3.4-2.4-6.3-8.5-19.7l-4.1-9.5a1384 1384 0 0 0-41.6-84.2L655 274l-2.4-4c-2.7-4.9-6.3-10.8-12.3-20.2a92 92 0 0 1-5.3-8.6c0-.2-1.8-3.1-4-6.3a32 32 0 0 1-4-6.9q-.1-.9-1-1-1-.2-1.9-2.3c-.5-1.2-3.6-5.9-7-10.5a80 80 0 0 1-6.1-9c0-.5-1.5-2.6-3.2-4.8a64 64 0 0 1-9.8-13.3q-.2-1-2-2.1-1.8-1-2-2.3c0-.7-4.1-6.2-9.1-12.2-5-6.1-9.9-12.2-10.8-13.7-.9-1.4-7.5-9.2-14.6-17.3a237 237 0 0 1-14.2-16.9c-.7-1.2-3.7-4.6-6.8-7.5s-5.5-5.7-5.5-6.2c0-1.6-59.8-60.9-61.4-60.9q-1.4 0-1.8-.8c-.4-.8-28.6-25.2-29.2-25.2-.3 0 1.2 2.8 3.3 6.2"

/** Argument count per SVG path command, keyed by the uppercase form. */
const ARG_COUNT: Record<string, number> = {
  M: 2, L: 2, H: 1, V: 1, C: 6, S: 4, Q: 4, T: 2, A: 7, Z: 0,
}

/** A command letter, or one number (including `.5`, `-.5` and `1e3` forms). */
const TOKEN = /([MmLlHhVvCcSsQqTtAaZz])|(-?(?:\d*\.\d+|\d+\.?)(?:[eE][-+]?\d+)?)/g

interface Subpath {
  /** Index into `d` where this subpath's moveto begins. */
  at: number
  /** Absolute coordinates of the moveto, with relative chaining resolved. */
  x: number
  y: number
}

/**
 * Walk a path's commands, tracking the current point, and record where each
 * subpath starts in absolute coordinates.
 *
 * Only endpoints matter here, not curve geometry: to know where a relative
 * `m` lands we need the point the previous subpath ended on, and every
 * command's endpoint is the last coordinate pair of its argument group (or
 * the single value, for H/V). That makes this a fraction of a full path
 * parser while still being exact.
 */
function walk(d: string): Subpath[] {
  const tokens: { cmd: string | null; num: string | null; at: number }[] = []
  for (const m of d.matchAll(TOKEN)) {
    tokens.push({ cmd: m[1] ?? null, num: m[2] ?? null, at: m.index })
  }

  const starts: Subpath[] = []
  let i = 0
  // current point, and the start of the current subpath (where `z` returns to)
  let cx = 0, cy = 0, sx = 0, sy = 0
  let cmd: string | null = null

  while (i < tokens.length) {
    const tok = tokens[i]
    if (tok.cmd) {
      cmd = tok.cmd
      i++
      if (cmd === "Z" || cmd === "z") {
        cx = sx; cy = sy
        continue
      }
    } else if (cmd === null) {
      // A number before any command is malformed; skip it rather than
      // looping forever on it.
      i++
      continue
    }

    const command: string = cmd
    const upper = command.toUpperCase()
    const relative = command !== upper
    const need = ARG_COUNT[upper]
    const args: number[] = []
    while (args.length < need && i < tokens.length && tokens[i].num !== null) {
      args.push(Number(tokens[i].num))
      i++
    }
    if (args.length < need) break // truncated path — stop where the data stops

    let x: number, y: number
    if (upper === "H") {
      x = relative ? cx + args[0] : args[0]
      y = cy
    } else if (upper === "V") {
      x = cx
      y = relative ? cy + args[0] : args[0]
    } else {
      const ex = args[args.length - 2]
      const ey = args[args.length - 1]
      x = relative ? cx + ex : ex
      y = relative ? cy + ey : ey
    }

    if (upper === "M") {
      starts.push({ at: tok.at, x, y })
      sx = x; sy = y
      // Coordinates that follow a moveto are an implicit lineto, and they
      // keep the moveto's relative-ness.
      cmd = relative ? "l" : "L"
    }

    cx = x; cy = y
  }

  return starts
}

/**
 * Split a path into one string per subpath, each standing on its own.
 *
 * A subpath that began with a relative `m` is chained to wherever the
 * previous one ended, so lifting it out verbatim would move it. Its moveto
 * is rewritten as an absolute `M` at the point the walk resolved; every
 * command after that is left byte-for-byte alone, so the geometry is
 * unchanged whether it was written relative or absolute.
 */
export function splitSubpaths(d: string): string[] {
  const starts = walk(d)
  if (starts.length === 0) return []

  const bounds = [...starts.map((s) => s.at), d.length]
  return starts.map((s, k) => {
    const segment = d.slice(bounds[k], bounds[k + 1])
    const body = segment.replace(
      /^[Mm]\s*-?(?:\d*\.\d+|\d+\.?)(?:[eE][-+]?\d+)?[,\s]*-?(?:\d*\.\d+|\d+\.?)(?:[eE][-+]?\d+)?/,
      ""
    )
    return `M${s.x} ${s.y}${body}`
  })
}

/**
 * The mark's three sails, each as an independent path.
 *
 * Computed once at module load rather than generated into a checked-in
 * file: a generated file is one more thing that can go stale, and the walk
 * costs well under a millisecond for this path.
 */
export const MARK_SAILS: string[] = splitSubpaths(SAIL_PATH)

/**
 * Geometry of the mark in its own path space — NOT the 1024 viewBox the
 * logo component draws it in, which additionally applies
 * `translate(194.6 234.9) scale(0.7936)`.
 *
 * Measured with SVGGraphicsElement.getBBox() on the three split paths.
 * `feet` are the x centres of each sail, used as the pivot its heel and
 * swell rotate about — pivoting about the foot is what makes the motion
 * read as canvas filling rather than a shape sliding.
 *
 * These numbers describe the mark as currently drawn. lib/__tests__ pins
 * MARK_SAILS.length at 3, so a redrawn mark with a different number of
 * sails fails loudly here instead of silently animating the wrong pivots.
 */
export const MARK_GEOMETRY = {
  width: 800,
  height: 750,
  cx: 400,
  cy: 375,
  baseline: 750,
  feet: [203, 442, 620],
} as const

/**
 * The transform the logo component wraps the path in to place it inside a
 * 1024 viewBox. Lives here so the tight viewBox below can be derived from it
 * rather than from a second copy of the same three numbers.
 */
export const MARK_TRANSFORM = { x: 194.6, y: 234.9, scale: 0.7936 } as const

/**
 * viewBox cropped to the mark's own bounds.
 *
 * Inside the 1024 box the mark occupies roughly 62% of the width and 58% of
 * the height — the rest is padding that belongs to the squircle tile, not to
 * the silhouette. Rendered on its own at a small size that padding is most
 * of the box, and the sails shrink to a few pixels. This viewBox drops it, so
 * `<CrewshipLogo tight />` fills its container edge to edge.
 *
 * Note the mark is NOT square (about 1.07:1), so size it on one axis and let
 * the other follow.
 */
export const MARK_VIEWBOX_TIGHT = [
  MARK_TRANSFORM.x,
  MARK_TRANSFORM.y,
  MARK_GEOMETRY.width * MARK_TRANSFORM.scale,
  MARK_GEOMETRY.height * MARK_TRANSFORM.scale,
]
  .map((n) => Number(n.toFixed(2)))
  .join(" ")
