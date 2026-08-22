import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

// Source-level, for the same reason app/(auth)/__tests__/auth-branding.test.ts
// is: these are layout properties of one 1100-line client component with a
// wizard's worth of data dependencies, and the thing worth pinning is the
// structural decision, not any one step's render.
//
// Each of these was a defect found by walking the real wizard on a freshly
// nuked instance, so each assertion carries the symptom it prevents.

const PAGE = readFileSync(
  join(process.cwd(), "app", "(onboarding)", "onboarding", "page.tsx"),
  "utf8"
)
const PREVIEW = readFileSync(
  join(process.cwd(), "components", "features", "onboarding", "onboarding-preview.tsx"),
  "utf8"
)

/**
 * The className of a column's own container — the first `<div className="…">`
 * after the marker comment, and nothing inside it.
 *
 * Scanning the whole block instead was the first version of this file and it
 * was wrong twice over: the form column contains plenty of legitimately
 * centred inner rows, and the explanatory comments quote the very class names
 * the assertions forbid.
 */
function columnClass(marker: string): string {
  const from = PAGE.indexOf(marker)
  expect(from, `${marker} moved or was renamed`).toBeGreaterThan(-1)
  const after = PAGE.slice(from)
  const open = after.indexOf("*/}")
  const match = after.slice(open).match(/<div className="([^"]+)"/)
  expect(match, `no container div follows ${marker}`).not.toBeNull()
  return match![1]
}

const leftColumn = () => columnClass("{/* LEFT: form")
const rightColumn = () => columnClass("{/* RIGHT: live preview")

describe("the setup wizard's chrome holds still", () => {
  it("anchors the form column to the top so the lockup cannot drift", () => {
    // Centring the column made the lockup and stepper slide as the step
    // content changed height — y=101 on Workspace, y=137 on Crew, y=66 on
    // Adapter. The logo visibly jumped on every Continue.
    const left = leftColumn()
    expect(left).toMatch(/flex items-start/)
    expect(left).not.toMatch(/flex items-center/)
  })

  it("anchors the preview column to the top for the same reason", () => {
    // The preview grows downward as you fill things in; centring made the
    // workspace card drift while it did.
    expect(rightColumn()).toMatch(/flex items-start/)
  })

  it("gives the preview a surface of its own", () => {
    // bg-muted/20 sat within a hair of the form column's background, so the
    // split read as one page with a hairline down it instead of two panes.
    const right = rightColumn()
    expect(right).not.toMatch(/bg-muted\/20/)
    expect(right).toMatch(/onboarding-pane/)
  })

  it("lights the surface with depth rather than with a coloured glow", () => {
    // The reach here is a big soft brand-blue radial, and it was the wrong
    // one: it is what every AI product has shipped since 2024, and next to a
    // form people have to read carefully it looks unserious. What makes a
    // split read as two panes is depth — an inset highlight and a falloff.
    const css = readFileSync(join(process.cwd(), "app", "globals.css"), "utf8")
    const rule = css.slice(css.indexOf(".onboarding-pane {"))
    const body = rule.slice(0, rule.indexOf("}"))
    expect(body).toMatch(/inset 0 1px 0/)
    expect(body).not.toMatch(/radial-gradient/)
    // No brand blue anywhere in the pane's own surface.
    expect(body).not.toMatch(/30[,\s]+123[,\s]+254|#1[eE]7[bB][fF][eE]/)
  })

  it("moves the edge light to the seam when the panes stack", () => {
    // Under lg the pane sits above the form, so a highlight on its top edge
    // would run along the top of the viewport, describing a seam that is not
    // there. The shared edge is the bottom one.
    const css = readFileSync(join(process.cwd(), "app", "globals.css"), "utf8")
    const stacked = css.slice(css.indexOf("@media (max-width: 1023px)"))
    expect(stacked.slice(0, 400)).toMatch(/inset 0 -1px 0/)
  })
})

describe("the setup lockup wears the mark the sign-in screen wears", () => {
  it("uses the bare cropped mark, not the tile", () => {
    // Nested inside both the tile's padding and the viewBox's, the sails
    // stopped being legible at lockup size.
    expect(PAGE).toMatch(/<CrewshipLogo\s+tight/)
    expect(PAGE).not.toMatch(/CrewshipLogoTile/)
  })
})

describe("the preview's empty state is a promise, not a gap", () => {
  it("reserves the height of the card that lands there", () => {
    // A thin strip left the pane looking ~85% empty on step one — which
    // reads as a failed render — and the layout jumped when the real crew
    // card arrived. The floor is the header plus four agent rows.
    const empty = PREVIEW.slice(PREVIEW.indexOf('key="empty"'))
    expect(empty).toMatch(/sm:min-h-\[248px\]/)
    // ...but only from sm up. Stacked on a phone the preview is below the
    // form and off-screen while you type, so a card's worth of reserved
    // height there is dead scroll for a landing nobody watches.
    expect(empty).toMatch(/min-h-\[120px\]/)
  })

  it("does not tell a phone user to look left", () => {
    // Stacked, there is no left — the picker is above the preview, not
    // beside it. Direction words in shared copy break in one layout or the
    // other, so this one has none.
    expect(PREVIEW).toMatch(/Your crew lands here/)
    // Comments stripped first: the note explaining this rule quotes the very
    // words it forbids, which is the third time that has bitten this file.
    const empty = PREVIEW.slice(PREVIEW.indexOf('key="empty"'), PREVIEW.indexOf('key="empty"') + 1400)
      .replace(/\{\/\*[\s\S]*?\*\/\}/g, "")
      .replace(/\/\/.*$/gm, "")
    expect(empty).not.toMatch(/on the left|on the right/)
  })
})

describe("the unauthenticated forms are usable with a thumb", () => {
  const CSS = readFileSync(join(process.cwd(), "app", "globals.css"), "utf8")
  const SHELL = readFileSync(
    join(process.cwd(), "components", "branding", "auth-split-shell.tsx"),
    "utf8"
  )

  it("raises the shared 36px controls to 44px where the pointer is a finger", () => {
    const rule = CSS.slice(CSS.indexOf(".touch-form input"))
    expect(rule.slice(0, 400)).toMatch(/min-height:\s*44px/)
  })

  it("keys that on the pointer, not on the viewport width", () => {
    // Width was the first attempt and it missed the iPad: 820px is over any
    // phone breakpoint you would pick, and it is still a finger. A mouse in
    // a narrow window, meanwhile, does not need 44px.
    const at = CSS.indexOf(".touch-form input")
    const guard = CSS.lastIndexOf("@media", at)
    expect(CSS.slice(guard, at)).toMatch(/pointer:\s*coarse/)
    expect(CSS.slice(guard, at)).not.toMatch(/max-width/)
  })

  it("applies it to both the auth shell and the setup wizard", () => {
    expect(SHELL).toMatch(/touch-form/)
    expect(PAGE).toMatch(/touch-form/)
  })
})

describe("pairing the CLI is not a dead end", () => {
  it("stops requiring a browser-pasted token once the CLI is paired", () => {
    // The green line on step 3 reads "CLI paired. You can finish below or
    // jump to `crewship setup` in the terminal" — and Launch stayed disabled
    // until a token was pasted into the browser, so the terminal route it
    // offers was unreachable. The server agrees with the message, not the
    // old gate: validateOnboardingCredential returns nil on an empty value.
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"))
    const step3 = gate.slice(gate.indexOf("step === 3"), gate.indexOf("return false"))
    const cli = step3.slice(step3.indexOf('mode === "cli"'))
    expect(cli).toMatch(/pairStatus === "consumed"/)
    // The property is "an empty token is acceptable", not "the word keyOK is
    // absent". The first version of this assertion forbade the identifier
    // outright and then failed the moment the gate legitimately started
    // using it to reject a *half-typed* token.
    expect(cli, "a paired CLI must accept an empty token").toMatch(/apiKey\.trim\(\) === ""/)
  })

  it("still requires a credential in browser mode", () => {
    // Browser mode has no CLI to land the credential afterwards, so the
    // token is the only way the agents get one.
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"))
    const step3 = gate.slice(gate.indexOf("step === 3"), gate.indexOf("return false"))
    expect(step3).toMatch(/mode === "browser"\) return keyOK/)
  })
})

describe("step 3 asks one question, then its consequence", () => {
  /** The JSX of step 3, from its guard to the telemetry block. */
  function stepThree(): string {
    const from = PAGE.indexOf("{step === 3 && (")
    const to = PAGE.indexOf("TELEMETRY CONSENT")
    expect(from, "step 3 guard moved").toBeGreaterThan(-1)
    expect(to).toBeGreaterThan(from)
    return PAGE.slice(from, to)
  }

  it("collapses the credential block behind a disclosure in CLI mode", () => {
    // The step used to ask two unrelated things at once — how the human
    // drives Crewship, and which credential the agents use — and ran off
    // the bottom of the viewport doing it. With a CLI in the picture the
    // second question has a second answer: `crewship login --pair` offers
    // to land the token in the terminal.
    //
    // Asserted as a property — a collapsed block plus a control that opens
    // it — and not by quoting the button's words. The first version of this
    // pinned the literal "Or add it now" and failed the moment that copy was
    // corrected, which taught the wrong lesson: the label is not the contract,
    // the escape hatch is.
    const step3 = stepThree()
    expect(step3).toMatch(/mode === "cli" && !showCredential/)
    expect(step3).toMatch(/setShowCredential\(true\)/)
  })

  it("never tells a launched user to run `crewship setup` to add the token", () => {
    // That instruction shipped and was false twice over: `crewship setup`
    // answers 409 "Onboarding already completed" once a crew exists, and a
    // credential created after Launch is not delivered to the agents that
    // already exist — autoAssignCredentials links workspace credentials at
    // DEPLOY time, and the read-time delivery query has no bare
    // workspace-scope arm. A crew launched without a token could not be
    // repaired by the route the UI named.
    //
    // Comments are stripped first so this cannot trip over the explanation
    // sitting next to the code it guards.
    const step3 = stepThree()
      .replace(/\{\/\*[\s\S]*?\*\/\}/g, "")
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/\/\/.*$/gm, "")
    expect(step3).not.toMatch(/crewship setup/)
  })

  it("keeps the credential open and required in browser mode", () => {
    // No terminal to fall back to there, so neither the collapse nor the
    // optional-token gate applies.
    expect(stepThree()).toMatch(/mode === "browser" \|\| showCredential/)
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"))
    expect(gate.slice(0, 900)).toMatch(/mode === "browser"\) return keyOK/)
  })

  it("puts the toolchain picker inside the credential block", () => {
    // Which toolchain you pick is part of answering "which credential", not
    // "how do I drive this" — it means nothing until a key is being pasted.
    const step3 = stepThree()
    const disclosure = step3.indexOf('mode === "browser" || showCredential')
    const toolchain = step3.indexOf("Agent toolchain")
    expect(disclosure).toBeGreaterThan(-1)
    expect(toolchain, "toolchain must sit inside the disclosure").toBeGreaterThan(disclosure)
  })

  it("refuses a half-typed token even though an empty one is fine", () => {
    // A paired CLI can finish in the terminal, so empty is a valid answer.
    // A truncated paste is not — it stores a credential that loads but
    // never works, which is the failure the token hint warns about.
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"))
    const cli = gate.slice(gate.indexOf('mode === "cli"'), gate.indexOf("return false"))
    expect(cli).toMatch(/apiKey\.trim\(\) === ""/)
    expect(cli).toMatch(/keyOK/)
    expect(cli).toMatch(/pairStatus === "consumed"/)
  })
})
