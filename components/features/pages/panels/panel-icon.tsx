import {
  Banknote, Calendar, Clock, Container, Cpu, Database, HardDrive,
  MemoryStick, Network, Rocket, Siren,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { CONCEPT_ICON } from "@/lib/concept-icons"

/**
 * The panel icon vocabulary — the client half of `internal/pages/icons.go`.
 *
 * A panel's icon used to be derived from its schema, which is right until a
 * page carries three `status.v1` panels: "is it running", "who is on call" and
 * "what deployed today" then wear one face, and the header stops telling the
 * reader which panel they are looking at. `icon:` in the page spec lets the
 * author say what the panel is ABOUT.
 *
 * THE SET IS CLOSED, AND THIS FILE IS THE MIRROR, NOT A SECOND OPINION.
 * The server refuses an icon outside the set at save time, naming the allowed
 * values. This map is the same list, and
 * `__tests__/panel-icon.test.tsx` reads `internal/pages/icons.go` and fails
 * when the two disagree — in both directions. A name the server accepts and
 * this file cannot draw is a blank header, which reads as a design decision
 * rather than as an error; a name here the server refuses is a glyph nobody
 * can ever select.
 *
 * WHY NOT JUST ACCEPT ANY `lucide-react` NAME.
 * Because then the vocabulary is the icon library's and not ours: a spec would
 * pin us to that dependency, break when it renames an export, and let an
 * author name something that does not exist and get nothing. The names below
 * are concepts a producer watches; which glyph draws them is our decision to
 * change.
 *
 * WHY TWO OF THEM COME FROM `CONCEPT_ICON`.
 * That file is the product's answer to "does this concept already have an
 * icon SOMEWHERE" — checking is the entire point of it. `people` and `queue`
 * are concepts it already holds (crews, inbox) and they keep those faces here.
 * `memory` deliberately does NOT: `CONCEPT_ICON.memory` is what an agent
 * remembers between sessions, and this one is a stick of RAM. Same word, two
 * concepts — reusing the brain for free memory would be the drift concept-icons
 * exists to prevent, wearing the costume of consistency.
 *
 * WHAT IS ABSENT, AND WHY.
 *  - No `check`, no `warning`. The panel already renders a verdict: status.v1
 *    draws ✓ / ! / ✕ per item and the frame draws the freshness word. A tick
 *    in the header is a second verdict on the same card and the reader cannot
 *    tell whether it describes the subject or the state.
 *  - No `TriangleAlert`. It is the broken-panel chrome (fallback-panel.tsx);
 *    an author-chosen glyph must never make a working panel look broken. The
 *    `alert` name is admitted — an incident board is a real subject — and
 *    draws a siren instead.
 *  - No colour, per icon or otherwise. Colour on this surface means state
 *    (`ok`/`warning`/`critical`, and the freshness verdict). A second colour
 *    axis on the same glyph would collide with the first. The icon carries
 *    identity; the state carries colour.
 */
export const PANEL_ICON = {
  // ── The machine ──
  /** RAM. Not the agent's memory — see the note above. */
  memory: MemoryStick,
  cpu: Cpu,
  disk: HardDrive,
  network: Network,
  /** What is running where. */
  container: Container,

  // ── What it talks to ──
  database: Database,
  /** Work waiting to be done: depth, backlog, age of the oldest. Inbox is
   *  already the product's icon for "things that have arrived for you", and a
   *  queue is that with a length. */
  queue: CONCEPT_ICON.inbox,

  // ── Time ──
  /** Elapsed time: uptime, latency, how long since. */
  clock: Clock,
  /** Dated things: a schedule, a deadline, a period. */
  calendar: Calendar,

  // ── The business the machine exists for ──
  /** Revenue, cost, a balance. A banknote and not a dollar sign: the currency
   *  is the payload's, not the icon's. */
  money: Banknote,
  /** Headcount, who is on call, who is in. Crews' own icon. */
  people: CONCEPT_ICON.crews,
  /** Releases and what shipped. */
  deploy: Rocket,
  /** Incidents as a SUBJECT — how many are open, who is paging. Never this
   *  panel's own verdict. */
  alert: Siren,
} as const satisfies Record<string, LucideIcon>

export type PanelIconName = keyof typeof PANEL_ICON

/** The vocabulary, for the parity test and for anything that lists it. */
export const PANEL_ICON_NAMES = Object.keys(PANEL_ICON) as PanelIconName[]

/**
 * Narrows an untrusted string to the closed set.
 *
 * A Set rather than `name in PANEL_ICON`, exactly as `isPanelSchema` does it:
 * inherited keys — `__proto__`, `constructor`, `toString` — are on every
 * object and must never select a glyph.
 */
const PANEL_ICON_SET: ReadonlySet<string> = new Set<string>(PANEL_ICON_NAMES)

export function isPanelIconName(value: unknown): value is PanelIconName {
  return typeof value === "string" && PANEL_ICON_SET.has(value)
}

/**
 * Untrusted string in, icon component out. Never returns undefined.
 *
 * The fallback is the caller's — in practice the icon the panel's schema
 * implies — so an icon this build cannot resolve degrades to exactly what the
 * panel looked like before anyone declared one. A header must never render
 * empty: an absent glyph is indistinguishable from a deliberate design, which
 * is the quiet failure the closed set exists to prevent.
 */
export function resolvePanelIcon(name: unknown, fallback: LucideIcon): LucideIcon {
  return isPanelIconName(name) ? PANEL_ICON[name] : fallback
}
