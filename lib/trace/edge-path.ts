// Turns a dagre edge route into an SVG path.
//
// dagre lays out edges as well as nodes: for every edge it produces a
// polyline that steps around the nodes in between. The canvas used to
// throw that away and draw a bezier straight from the source handle to
// the target handle, which is indistinguishable from a routed edge for
// a one-rank hop and badly wrong for a skip edge — a six-rank edge
// swept diagonally across every node it passed.
//
// Corners are rounded so the result reads as a drawn line rather than a
// wireframe, and so the eye can follow a turn without losing the thread
// where two edges cross.

export interface Pt {
  x: number
  y: number
}

/**
 * SVG path `d` through `points`, with corners rounded to `radius`.
 *
 * The path starts exactly on the first point and ends exactly on the
 * last: those are the handle positions the edge must physically meet,
 * and an approximation there shows as a visible gap at the node border.
 *
 * The radius is clamped per corner to half the shorter adjoining
 * segment. Unclamped, a generous radius on a short segment overshoots
 * the neighbouring corner and the path crosses itself.
 */
export function roundedPolylinePath(points: readonly Pt[], radius = 8): string {
  const pts = dedupe(points)
  if (pts.length < 2) return ""
  if (pts.length === 2) return `M ${fmt(pts[0])} L ${fmt(pts[1])}`

  let d = `M ${fmt(pts[0])}`
  for (let i = 1; i < pts.length - 1; i++) {
    const prev = pts[i - 1]
    const curr = pts[i]
    const next = pts[i + 1]

    const inLen = dist(prev, curr)
    const outLen = dist(curr, next)
    const r = Math.min(radius, inLen / 2, outLen / 2)

    if (r <= 0.01) {
      // Collapsed segment — a curve here would be a no-op with a
      // chance of NaN. Take the corner square.
      d += ` L ${fmt(curr)}`
      continue
    }

    const entry = along(curr, prev, r)
    const exit = along(curr, next, r)
    d += ` L ${fmt(entry)} Q ${fmt(curr)} ${fmt(exit)}`
  }
  d += ` L ${fmt(pts[pts.length - 1])}`
  return d
}

/**
 * A point in the middle of the route, for anchoring an edge label.
 *
 * Uses the route's own vertices rather than the straight-line midpoint
 * of its endpoints — on a routed edge those are different places, and
 * the straight-line one can land on top of an unrelated node.
 */
export function midpointOf(points: readonly Pt[]): Pt | null {
  const pts = dedupe(points)
  if (pts.length === 0) return null
  if (pts.length === 1) return { ...pts[0] }
  const mid = pts.length / 2
  if (pts.length % 2 === 1) return { ...pts[Math.floor(mid)] }
  const a = pts[mid - 1]
  const b = pts[mid]
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }
}

/** Drop consecutive duplicates — they make segment length 0 and NaN follows. */
function dedupe(points: readonly Pt[]): Pt[] {
  const out: Pt[] = []
  for (const p of points) {
    if (!Number.isFinite(p?.x) || !Number.isFinite(p?.y)) continue
    const last = out[out.length - 1]
    if (last && last.x === p.x && last.y === p.y) continue
    out.push({ x: p.x, y: p.y })
  }
  return out
}

function dist(a: Pt, b: Pt): number {
  return Math.hypot(b.x - a.x, b.y - a.y)
}

/** Point `r` away from `from`, heading toward `to`. */
function along(from: Pt, to: Pt, r: number): Pt {
  const len = dist(from, to)
  if (len === 0) return { ...from }
  return {
    x: from.x + ((to.x - from.x) / len) * r,
    y: from.y + ((to.y - from.y) / len) * r,
  }
}

function fmt(p: Pt): string {
  return `${round(p.x)},${round(p.y)}`
}

function round(n: number): number {
  return Math.round(n * 100) / 100
}
