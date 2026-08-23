/**
 * What every SubBar action USED to open, and what it opens now.
 *
 * Read out of the components on 2026-08-23, not remembered. Each row names the
 * file so a claim here can be checked against the source in one jump.
 *
 * All twelve have since landed on the shared shell. The rows describe the
 * BEFORE and are kept that way on purpose: the argument for the shell is the
 * eleven widths, the three shells and the two overlays, and an audit that
 * rewrites itself as the work lands leaves the page asserting a conclusion
 * with nothing behind it. `migrated` carries the after.
 *
 * The DOORS were already unified before any of this — every one is a
 * `SubBarPrimary` or `SubBarSecondary` in the same row, at the same height, in
 * the same order (sub-bar.tsx). What sat behind them was not, and this table
 * is why.
 */

export type Shell =
  /** Radix `<Dialog>` via components/ui/dialog. Overlay, focus trap, Esc for free. */
  | "radix"
  /** Hand-rolled `fixed inset-0 z-50` + `bg-black/50`. Dims like Radix, but no focus trap. */
  | "hand-rolled"
  /** Hand-rolled + `bg-background/70 backdrop-blur-md`. Frosts the page behind it. */
  | "hand-rolled-blur"

export interface Door {
  page: string
  /** The label on the button, verbatim. */
  action: string
  /** Where the surface lives. */
  file: string
  shell: Shell
  /** Desktop max width in px. A range = it changes between steps. */
  width: string
  /** How the footer's primary action is built. */
  primary: string
  /** ⌘↵ submits? */
  cmdEnter: boolean
  /** Full-bleed on a phone? */
  mobile: boolean
  /** What it should become. */
  proposed: string
  /**
   * Landed on the shared shell.
   *
   * The row STAYS, describing what the door used to be. An audit that rewrites
   * itself as the work lands stops being evidence — the argument for the shell
   * IS the eleven widths and the two overlays, and deleting them once they are
   * gone leaves a page asserting a conclusion with nothing behind it.
   */
  migrated?: { on: string; note?: string }
}

export const DOORS: Door[] = [
  {
    page: "Issues",
    action: "New Issue",
    file: "features/orchestration/create-issue-modal.tsx",
    shell: "radix",
    width: "640",
    primary: "raw <button> h-7 bg-primary",
    cmdEnter: true,
    mobile: false,
    proposed: "md · reference",
    migrated: { on: "2026-08-23", note: "The reference. Its shape became the shell; its pills, popovers and Create-more switch lifted across unchanged." },
  },
  {
    page: "Issues",
    action: "New Project",
    file: "features/orchestration/create-project-modal.tsx",
    shell: "radix",
    width: "720",
    primary: "raw <button> h-7 bg-primary",
    cmdEnter: true,
    mobile: false,
    proposed: "md",
    migrated: { on: "2026-08-23", note: "Pill row moved below the description into the slot the shell has for it — a visible reorder, flagged rather than hidden." },
  },
  {
    page: "Routines",
    action: "New routine",
    file: "features/routines/routine-create-dialog.tsx",
    shell: "hand-rolled",
    width: "576 → 672 → 768",
    primary: "<Button size=sm>",
    cmdEnter: false,
    mobile: false,
    proposed: "lg · fixed",
    migrated: { on: "2026-08-23", note: "Pinned at lg. CodeMirror, the YAML/JSON conversion and the /test_run → saveToken → /save chain survived intact; the insides came onto the kit afterwards." },
  },
  {
    page: "Routines",
    action: "Import",
    file: "features/routines/routines-layout.tsx:392",
    shell: "hand-rolled",
    width: "672",
    primary: "<Button size=sm>",
    cmdEnter: false,
    mobile: false,
    proposed: "sm",
    migrated: { on: "2026-08-23", note: "Now sm. The collision rule is a control instead of a paragraph." },
  },
  {
    page: "Pages",
    action: "New page",
    file: "features/pages/page-editor.tsx:939",
    shell: "hand-rolled-blur",
    width: "1100",
    primary: "<Button size=sm>",
    cmdEnter: false,
    mobile: false,
    proposed: "xl",
    migrated: { on: "2026-08-23", note: "The blur and the hand-rolled overlay are gone, and it gained a focus trap it never had. First real user of the dirty guard." },
  },
  {
    page: "Pages",
    action: "Import",
    file: "features/pages/page-import-dialog.tsx:128",
    shell: "hand-rolled-blur",
    width: "560",
    primary: "<Button size=sm>",
    cmdEnter: false,
    mobile: false,
    proposed: "sm",
    migrated: { on: "2026-08-23", note: "Keeps the unresolved-reference worklist, and is the first real user of CreateSurfaceRefusal's field list." },
  },
  {
    page: "Crews",
    action: "New crew",
    file: "features/crews/create-crew-dialog.tsx",
    shell: "radix",
    width: "680 → 940",
    primary: 'raw <button> "✓ Create crew"',
    cmdEnter: true,
    mobile: false,
    proposed: "lg · fixed",
    migrated: { on: "2026-08-23", note: "Width pinned at lg. Later rebuilt to the four-step shape /design proposed: Runtime folded into Container, egress open by default, MCP gone." },
  },
  {
    page: "Crews",
    action: "New agent",
    file: "features/crews/create-agent/create-agent-dialog.tsx",
    shell: "radix",
    width: "640",
    primary: "raw <button>",
    cmdEnter: false,
    mobile: false,
    proposed: "md",
    migrated: { on: "2026-08-23", note: "All sixteen fields of createAgentRequest verified present against the Go struct after the move." },
  },
  {
    page: "Skills",
    action: "Import",
    file: "skills/import-dialog.tsx",
    shell: "radix",
    width: "512",
    primary: "<Button> default h-9",
    cmdEnter: false,
    mobile: false,
    proposed: "sm",
    migrated: { on: "2026-08-23", note: "Three sources behind one door, on the shell's Choice instead of a Radix tab strip." },
  },
  {
    page: "Credentials",
    action: "Add secret",
    file: "features/credentials/add-secret-sheet.tsx",
    shell: "radix",
    width: "680",
    primary: "wizard-owned",
    cmdEnter: false,
    mobile: true,
    proposed: "md",
    migrated: { on: "2026-08-23", note: "The one door that already handled a phone. Its full-screen takeover became the shared bottom sheet, and its DetailCards became the kit's sections." },
  },
  {
    page: "Credentials",
    action: "Connect via OAuth",
    file: "features/credentials/connect-oauth-dialog.tsx",
    shell: "radix",
    width: "448",
    primary: "form-owned",
    cmdEnter: false,
    mobile: false,
    proposed: "sm",
    migrated: { on: "2026-08-23", note: "OAuthForm is shared with the MCP picker, so it publishes its primary rather than surrendering it — the picker keeps its inline row, this door puts Authorize in the footer." },
  },
  {
    page: "Integrations",
    action: "Add integration",
    file: "features/integrations/add-integration-dialog.tsx",
    shell: "radix",
    width: "672",
    primary: "none — picking closes it",
    cmdEnter: false,
    mobile: false,
    proposed: "xl",
    migrated: { on: "2026-08-23", note: "Kind → service kept. Renders no primary, which the shell now allows rather than treating as a contradiction." },
  },
]

/**
 * The counts the table adds up to. Computed, not typed in, so they cannot
 * drift from the rows above.
 */
export const DIVERGENCE = {
  doors: DOORS.length,
  migrated: DOORS.filter((d) => d.migrated).length,
  shells: new Set(DOORS.map((d) => d.shell)).size,
  widths: new Set(DOORS.flatMap((d) => d.width.split(" → "))).size,
  overlays: new Set(DOORS.map((d) => (d.shell === "hand-rolled-blur" ? "blur" : "dim"))).size,
  primaries: new Set(DOORS.map((d) => d.primary)).size,
  cmdEnter: DOORS.filter((d) => d.cmdEnter).length,
  mobile: DOORS.filter((d) => d.mobile).length,
} as const

/**
 * Counted across `components/**` and `app/**` on the same date, with
 * `grep -o 'border-white/\[0\.[0-9]*\]' | sort | uniq -c`. These are not about
 * the modals specifically — they are the same disease one level down, and any
 * unification that stops at the dialog shell leaves them in place.
 */
export const TOKEN_DRIFT = [
  {
    what: "Hairline borders",
    detail: "border-white/[0.04 · 0.05 · 0.06 · 0.07 · 0.08 · 0.1 · 0.10 · 0.12 · 0.15 · 0.20]",
    count: "10 alphas · 260 uses",
    fix: "border-hairline (a --border mix, so it works in light mode too)",
  },
  {
    what: "Modal shells outside the three primitives",
    detail: "fixed inset-0 z-50 written by hand in feature components",
    count: "9 files",
    fix: "CreateSurface for creates, Sheet for inspectors, AlertDialog for confirms",
  },
  {
    what: "Typography scales",
    detail: ".type-* (app) and .type-page-* (Pages) describe the same four roles",
    count: "2 scales + raw text-[11.5px] / text-[12.5px] / text-[15px]",
    fix: "one scale; .type-page-* folds into .type-*",
  },
  {
    what: "Raw <button> in feature code",
    detail: "hand-styled buttons next to the shared <Button> in the same files",
    count: "477 raw vs 419 shared",
    fix: "shared variants; raw <button> only where there is genuinely no variant",
  },
] as const
