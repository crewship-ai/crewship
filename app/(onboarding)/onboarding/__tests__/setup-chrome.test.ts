import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { stripComments } from "./dead-agent-routes.test"

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

// Step 3 grew unbounded: every crew the Guide creates adds a card to the left
// column, and "Built so far" adds a row per crew, routine and page under
// those. On a 800px-tall window the second created crew already pushed Launch
// off the bottom, and the only way to reach the button that finishes setup was
// to scroll the page — with nothing on screen suggesting there was anything
// below to scroll to. A person who cannot see Launch cannot finish onboarding.
describe("the wizard's controls stay reachable however tall the step grows", () => {
  /** The nav row holding Back / Skip setup / Launch. */
  function navRow(): string {
    const at = PAGE.indexOf("{!launchSummary && (")
    expect(at, "the nav row's guard moved").toBeGreaterThan(-1)
    const match = PAGE.slice(at).match(/<div className="([^"]+)"/)
    expect(match, "no container div follows the nav guard").not.toBeNull()
    return match![1]
  }

  it("pins Back / Skip / Launch to the bottom of the column", () => {
    // sticky, not fixed: the wizard stacks under lg, where the form column is
    // followed by the preview pane. `fixed` would leave the bar welded to the
    // viewport while the user scrolled through the preview, describing
    // controls that no longer apply to what is on screen. Sticky releases the
    // moment its own column scrolls past, which is the correct scope.
    const nav = navRow()
    expect(nav).toMatch(/\bsticky\b/)
    expect(nav).toMatch(/\bbottom-0\b/)
    expect(nav).not.toMatch(/\bfixed\b/)
  })

  it("gives that bar an opaque surface so content cannot show through it", () => {
    // A translucent bar over a scrolling list of crew cards renders the
    // Launch label on top of moving text and neither can be read.
    expect(navRow()).toMatch(/bg-background/)
  })

  it("carries its own bottom padding instead of leaving a gap under itself", () => {
    // The column's own bottom padding used to sit BELOW the nav row. Once the
    // row is sticky that padding becomes a transparent letterbox at the foot
    // of the scrollport, and the crew cards slide visibly through it under
    // the pinned bar.
    const left = leftColumn()
    expect(left, "the column must not pad below the sticky bar").toMatch(/pb-0/)
    expect(navRow(), "the bar owns the bottom inset now").toMatch(/pb-\d/)
  })

  it("scrolls the step content rather than the whole desktop page", () => {
    // The two columns are independent full-height panes on lg; scrolling the
    // document there would move the preview too.
    expect(leftColumn()).toMatch(/lg:overflow-y-auto/)
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
    // words it forbids, which is the third time that has bitten this file —
    // hence the shared character-scanner (dead-agent-routes.test.ts's
    // stripComments) rather than another inline `.replace` chain.
    const empty = stripComments(
      PREVIEW.slice(PREVIEW.indexOf('key="empty"'), PREVIEW.indexOf('key="empty"') + 1400),
    )
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

describe("the model token is the agents' requirement, not the human's", () => {
  /**
   * The step-2 arm of canContinue — the Adapter step, which now comes
   * before Crew. Sliced between the step 2 and step 3 lines so a stray
   * substring in the step-3 arm (`crewMode`, `appliedProposal`) can't drift
   * into this assertion by accident.
   */
  function tokenGate(): string {
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"))
    return gate.slice(gate.indexOf("step === 2"), gate.indexOf("step === 3"))
  }

  it("requires a token in every mode, with only the local-model exception", () => {
    // This gate has now been wrong in both directions, which is why it is
    // pinned by property rather than by shape.
    //
    // It first required `keyOK && pairStatus === "consumed"` — right about
    // the token, but it disabled Continue with no explanation and read as a
    // dead end. The fix for that should have been to say WHY. Instead the
    // token requirement was dropped, and that produced a crew of four agents
    // with zero credentials that could not be repaired afterwards:
    // `crewship setup` answers 409 once a crew exists, and a credential
    // created later is never linked, because autoAssignCredentials runs at
    // deploy time.
    //
    // Agents run in containers and always need their own credential. The
    // only exemption is a local model on the operator's own endpoint (#944).
    const gate = tokenGate()
    expect(gate).toMatch(/apiKey\.trim\(\)\.length >= 8/)
    expect(gate).toMatch(/isLocalModel\(model\)/)
  })

  it("does not let the handoff mode change whether a token is needed", () => {
    // Pairing signs the operator's TERMINAL in. It gives the agents nothing,
    // so it must not appear in this gate at all — in either direction.
    const gate = tokenGate()
    expect(gate, "mode must not gate the token").not.toMatch(/mode === "(cli|browser)"/)
    expect(gate, "pairing must not gate Continue").not.toMatch(/pairStatus/)
  })
})

// Step 3 opens a chat with an agent that runs inside a container. Before this
// gate the wizard let a user through with no runtime and then answered their
// first message with two stacked errors naming an internal component — after
// they had already committed to the step, and two steps after step 1 told
// them it was fine to carry on without Docker.
describe("the crew step is not offered without a runtime to run it in", () => {
  it("gates Continue on the server actually driving a runtime, not merely on Docker existing", () => {
    // `available` means a runtime is installed and answering a ping; `in_use`
    // means THIS server is driving one. A host running Docker under a
    // crewshipd started with --no-docker reports available=true and can start
    // no container at all, so gating on `available` would pass and the chat
    // would still fail. dev.sh falls back to exactly that mode.
    expect(PAGE).toContain("runtimeInUse")
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"), PAGE.indexOf("if (step === 3)"))
    expect(gate, "step-2 gate does not consult runtimeInUse").toContain("runtimeInUse")
    expect(gate, "gating on `available` would pass under --no-docker").not.toMatch(/runtimeReady\s*(===|&&)/)
  })

  it("blocks while the probe is still in flight rather than defaulting open", () => {
    // `null` is "we do not know yet". Treating unknown as permission would
    // reopen the hole on every slow probe.
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"), PAGE.indexOf("if (step === 3)"))
    expect(gate).toContain("runtimeInUse === true")
  })

  it("offers a re-check instead of demanding a page reload", () => {
    // The probe used to run once on mount, so a user who started Docker
    // mid-wizard stayed blocked until they reloaded.
    expect(PAGE).toContain("onboarding-runtime-blocker")
    expect(PAGE).toContain("Re-check")
    expect(PAGE).toContain("checkRuntime")
  })

  it("step 1 no longer promises setup can be finished without a runtime", () => {
    // It used to say "You can still finish setup now and start a runtime
    // later from Settings" — which the step-2 gate now contradicts outright.
    expect(PAGE).not.toContain("You can still finish setup now and start a runtime later")
  })
})

describe("onboarding resume never invents a fresh account", () => {
  it("uses the refresh-aware API gate and restores durable workspace state", () => {
    expect(PAGE).toMatch(/apiFetch\("\/api\/v1\/onboarding\/status"\)/)
    expect(PAGE).toMatch(/loadOnboardingResumeState\(\)/)
    expect(PAGE).toMatch(/snapshot\.preferredLanguage[\s\S]*setStep\(2\)/)
    expect(PAGE).toMatch(/setStep\(3\)/)
  })

  it("reuses an encrypted credential without putting its plaintext in React", () => {
    expect(PAGE).toMatch(/apiKey: null/)
    expect(PAGE).toMatch(/savedCredentialSelected/)
    expect(PAGE).toMatch(/Saved token — leave blank to reuse/)
  })
})

describe("step 2 asks one question, then its consequence", () => {
  /** The JSX of step 2 (Adapter + token), from its guard to the telemetry
   *  block. Adapter moved from step 3 to step 2 in the Workspace → Adapter
   *  → Crew reorder (the setup agent's chat, now step 3's default, needs a
   *  credential in place before it opens — see page.tsx's own doc comment
   *  and persistAdapterCredential). */
  function stepTwo(): string {
    const from = PAGE.indexOf("step === 2 && (")
    const to = PAGE.indexOf("TELEMETRY CONSENT")
    expect(from, "step 2 guard moved").toBeGreaterThan(-1)
    expect(to).toBeGreaterThan(from)
    return PAGE.slice(from, to)
  }

  /**
   * step 2 with every comment removed.
   *
   * Needed by any assertion phrased as "this string must NOT appear": the
   * comments next to a correction quote the wording being corrected, so a
   * raw scan matches the explanation and reports the bug it documents. This
   * is the third time that has bitten in this file — hence the shared
   * character-scanner (`stripComments`, dead-agent-routes.test.ts) rather
   * than another inline `.replace` chain.
   */
  function stepTwoCode(): string {
    return stripComments(stepTwo())
  }

  it("never hides the model picker behind the handoff choice", () => {
    // The credential block used to be gated on
    // `mode === "browser" || showCredential`, and the model picker lives
    // inside it — so choosing "Pair my CLI" silently removed the choice of
    // which model the agents run. Backwards: the model is a fact about the
    // agents, the handoff is about where the human works.
    const step2 = stepTwoCode()
    expect(step2, "credential block must not be mode-gated").not.toMatch(
      /mode === "browser" \|\| showCredential/,
    )
    expect(step2, "the collapse state is gone entirely").not.toMatch(/showCredential/)
    // Both controls present unconditionally.
    expect(step2).toMatch(/htmlFor="model"/)
    expect(step2).toMatch(/Agent toolchain/)
  })

  it("does not make a first-run user install the CLI to finish", () => {
    // The person this screen exists for is installing Crewship for the first
    // time and may have no CLI at all. Leading with "Pair my CLI" as the
    // recommended path sent them to a GitHub release page to download a
    // binary before they could finish signing up.
    expect(PAGE).toMatch(/useState<HandoffMode>\("browser"\)/)
    const step2 = stepTwo()
    const browserCard = step2.indexOf('title="Chat in browser"')
    const cliCard = step2.indexOf('title="Also pair my CLI"')
    expect(browserCard).toBeGreaterThan(-1)
    expect(cliCard).toBeGreaterThan(-1)
    expect(browserCard, "the no-install path comes first").toBeLessThan(cliCard)
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
    expect(stepTwoCode()).not.toMatch(/crewship setup/)
  })

  it("leads with what the crew needs, not with how the human works", () => {
    // The heading asked "How will you work?" — the human's question — while
    // the step's actual requirement belongs to the agents. That framing is
    // how the token came to look optional once you had answered about
    // yourself, and it is what let the mode picker swallow the model choice.
    const step2 = stepTwoCode()
    expect(step2).not.toMatch(/How will you work\?/)
    expect(step2).toMatch(/Give your agents a model/)
  })

  it("says plainly that pairing does not credential the agents", () => {
    // The single sentence that would have saved this whole round trip.
    expect(stepTwo()).toMatch(/does not give them one/)
  })
})
