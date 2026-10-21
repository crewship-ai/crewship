import { describe, it, expect } from "vitest"
import { readFileSync, readdirSync, statSync } from "node:fs"
import { join } from "node:path"
import { stripComments } from "./dead-agent-routes.test"

// Source-level, for the same reason app/(auth)/__tests__/auth-branding.test.ts
// is: these are layout properties of one large client component with a
// wizard's worth of data dependencies, and the thing worth pinning is the
// structural decision, not any one step's render.
//
// Each of these was a defect found by walking the real wizard on a freshly
// nuked instance, or a Harbor 2 rule (#3409), so each assertion carries the
// symptom it prevents.

const PAGE = readFileSync(join(process.cwd(), "app", "(onboarding)", "onboarding", "page.tsx"), "utf8")
const CARD = readFileSync(join(process.cwd(), "components", "features", "onboarding", "setup-card.tsx"), "utf8")

/** The JSX of one exported component in setup-card.tsx, comments removed. */
function component(name: string): string {
  const from = CARD.indexOf(`export function ${name}(`)
  expect(from, `${name} moved or was renamed`).toBeGreaterThan(-1)
  const next = CARD.indexOf("\nexport function ", from + 1)
  return stripComments(CARD.slice(from, next === -1 ? undefined : next))
}

function onboardingSources(dir = join(process.cwd(), "app", "(onboarding)")): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (name === "__tests__") continue
    if (statSync(p).isDirectory()) out.push(...onboardingSources(p))
    else if (/\.tsx?$/.test(name)) out.push(p)
  }
  return out
}

describe("the setup card holds still", () => {
  it("puts the lockup above the card, outside anything that changes per step", () => {
    // The old split page centred its form column, so the lockup slid as the
    // step changed height — y=101 on Workspace, y=137 on Crew, y=66 on
    // Adapter. The frame renders the lockup once, before the card slot.
    const frame = component("SetupFrame")
    expect(frame.indexOf("<CrewshipLogo")).toBeGreaterThan(-1)
    expect(frame.indexOf("<CrewshipLogo")).toBeLessThan(frame.indexOf("{children}"))
  })

  it("anchors the card to the top so a taller step grows downwards", () => {
    const frame = component("SetupFrame")
    expect(frame).not.toMatch(/justify-center/)
    expect(frame).not.toMatch(/items-center justify-center/)
  })

  it("eases the width between L and XL instead of jumping", () => {
    const card = component("SetupCard")
    expect(CARD).toMatch(/lg: "md:max-w-\[800px\]"/)
    expect(CARD).toMatch(/xl: "md:max-w-\[960px\]"/)
    expect(card).toMatch(/transition-\[max-width\]/)
    expect(card).toMatch(/motion-reduce:transition-none/)
  })

  it("has no close button and no Cancel — Skip setup is the only way out", () => {
    // There is nothing behind the card to return to; a Cancel would be a
    // third Skip with a friendlier name.
    const code = stripComments(PAGE)
    expect(code).not.toMatch(/aria-label="Close"/)
    expect(code).not.toMatch(/>\s*Cancel\s*</)
  })
})

describe("the wizard's controls stay reachable however tall the step grows", () => {
  it("keeps the footer outside the scrollport", () => {
    // Step 3 grows with every crew the Guide creates. Launch used to be pushed
    // below the fold by the second crew. The body is the only thing that
    // scrolls; the footer is its sibling and never shrinks.
    expect(component("SetupCardBody")).toMatch(/overflow-y-auto/)
    const footer = component("SetupCardFooter")
    expect(footer).toMatch(/\bshrink-0\b/)
    expect(footer).not.toMatch(/overflow-y-auto/)
  })

  it("pins the footer to the bottom of a phone, on an opaque surface", () => {
    // sticky rather than fixed, and opaque so a scrolling list cannot show
    // through the Launch label.
    const footer = component("SetupCardFooter")
    expect(footer).toMatch(/\bsticky\b/)
    expect(footer).toMatch(/\bbottom-0\b/)
    expect(footer).not.toMatch(/\bfixed\b/)
    expect(footer).toMatch(/\bbg-muted\b/)
    expect(footer).toMatch(/safe-area-inset-bottom/)
  })
})

describe("Harbor 2 page rules for the setup", () => {
  it("shows no spinner anywhere in the onboarding route", () => {
    for (const file of onboardingSources()) {
      expect(stripComments(readFileSync(file, "utf8")), file).not.toMatch(/<Spinner\b|components\/ui\/spinner/)
    }
  })

  it("has no oversized form headings and no raw colours", () => {
    // The step title is the card header's h1, not a text-2xl inside the form.
    const code = stripComments(PAGE)
    expect(code).not.toMatch(/text-2xl/)
    expect(code).not.toMatch(/rgba\(/)
    expect(stripComments(CARD)).not.toMatch(/rgba\(|#[0-9a-fA-F]{3,6}\b/)
  })

  it("uses the kit's step bar, confirmation and error state", () => {
    expect(PAGE).toMatch(/<WizardSteps\b/)
    expect(PAGE).toMatch(/<ConfirmDialog\b/)
    expect(PAGE).toMatch(/<PageError\b/)
    expect(PAGE).not.toMatch(/function VerticalStepper|OnboardingPreview/)
  })
})

describe("the setup lockup wears the mark the sign-in screen wears", () => {
  it("uses the bare cropped mark, not the tile", () => {
    // Nested inside both the tile's padding and the viewBox's, the sails
    // stopped being legible at lockup size.
    expect(CARD).toMatch(/<CrewshipLogo\s+tight/)
    expect(CARD).not.toMatch(/CrewshipLogoTile/)
  })
})

describe("the unauthenticated forms are usable with a thumb", () => {
  const CSS = readFileSync(join(process.cwd(), "app", "globals.css"), "utf8")
  const SHELL = readFileSync(join(process.cwd(), "components", "branding", "auth-card.tsx"), "utf8")

  it("raises the shared 36px controls to 44px where the pointer is a finger", () => {
    const rule = CSS.slice(CSS.indexOf(".touch-form input"))
    expect(rule.slice(0, 400)).toMatch(/min-height:\s*44px/)
  })

  it("keys that on the pointer, not on the viewport width", () => {
    // Width missed the iPad: 820px is over any phone breakpoint you would
    // pick, and it is still a finger.
    const at = CSS.indexOf(".touch-form input")
    const guard = CSS.lastIndexOf("@media", at)
    expect(CSS.slice(guard, at)).toMatch(/pointer:\s*coarse/)
    expect(CSS.slice(guard, at)).not.toMatch(/max-width/)
  })

  it("applies it to both the auth shell and the setup frame", () => {
    expect(SHELL).toMatch(/touch-form/)
    expect(component("SetupFrame")).toMatch(/touch-form/)
  })
})

describe("the model token is the agents' requirement, not the human's", () => {
  /** The step-2 arm of canContinue, sliced between the step 2 and step 3 lines. */
  function tokenGate(): string {
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"))
    return gate.slice(gate.indexOf("step === 2"), gate.indexOf("step === 3"))
  }

  it("requires a token in every mode, with only the local-model exception", () => {
    // This gate has been wrong in both directions. Requiring pairing blocked
    // Continue with no explanation; dropping the token produced a crew of
    // agents with zero credentials that could not be repaired afterwards —
    // `crewship setup` answers 409 once a crew exists, and a credential
    // created later is never linked (autoAssignCredentials runs at deploy).
    const gate = tokenGate()
    expect(gate).toMatch(/apiKey\.trim\(\)\.length >= 8/)
    expect(gate).toMatch(/isLocalModel\(model\)/)
  })

  it("does not let the handoff mode change whether a token is needed", () => {
    const gate = tokenGate()
    expect(gate, "mode must not gate the token").not.toMatch(/mode === "(cli|browser)"/)
    expect(gate, "pairing must not gate Continue").not.toMatch(/pairStatus/)
  })
})

// Step 3 opens a chat with an agent that runs inside a container. Before this
// gate the wizard let a user through with no runtime and then answered their
// first message with two stacked errors naming an internal component.
describe("the crew step is not offered without a runtime to run it in", () => {
  it("gates Continue on the server actually driving a runtime, not merely on Docker existing", () => {
    // `available` means a runtime answers a ping; `in_use` means THIS server
    // drives one. Under --no-docker the first is true and nothing can start.
    expect(PAGE).toContain("runtimeInUse")
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"), PAGE.indexOf("if (step === 3)"))
    expect(gate, "step-2 gate does not consult runtimeInUse").toContain("runtimeInUse")
    expect(gate, "gating on `available` would pass under --no-docker").not.toMatch(/runtimeReady\s*(===|&&)/)
  })

  it("blocks while the probe is still in flight rather than defaulting open", () => {
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"), PAGE.indexOf("if (step === 3)"))
    expect(gate).toContain("runtimeInUse === true")
  })

  it("offers a re-check instead of demanding a page reload", () => {
    expect(PAGE).toContain("onboarding-runtime-blocker")
    expect(PAGE).toContain("Check again")
    expect(PAGE).toContain("checkRuntime")
  })

  it("step 1 no longer promises setup can be finished without a runtime", () => {
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
  /** The JSX of step 2, from its guard to the telemetry block. */
  function stepTwo(): string {
    const from = PAGE.indexOf("step === 2 && (")
    const to = PAGE.indexOf("TELEMETRY CONSENT")
    expect(from, "step 2 guard moved").toBeGreaterThan(-1)
    expect(to).toBeGreaterThan(from)
    return PAGE.slice(from, to)
  }

  /** Comments stripped: the notes beside a correction quote what it corrected. */
  function stepTwoCode(): string {
    return stripComments(stepTwo())
  }

  it("never hides the model picker behind the handoff choice", () => {
    const step2 = stepTwoCode()
    expect(step2, "credential block must not be mode-gated").not.toMatch(/mode === "browser" \|\| showCredential/)
    expect(step2, "the collapse state is gone entirely").not.toMatch(/showCredential/)
    expect(step2).toMatch(/htmlFor="model"/)
    expect(step2).toMatch(/Agent toolchain/)
  })

  it("does not make a first-run user install the CLI to finish", () => {
    expect(PAGE).toMatch(/useState<HandoffMode>\("browser"\)/)
    const step2 = stepTwo()
    const browserCard = step2.indexOf('title="Chat in browser"')
    const cliCard = step2.indexOf('title="Also pair my CLI"')
    expect(browserCard).toBeGreaterThan(-1)
    expect(cliCard).toBeGreaterThan(-1)
    expect(browserCard, "the no-install path comes first").toBeLessThan(cliCard)
  })

  it("never tells a launched user to run `crewship setup` to add the token", () => {
    // `crewship setup` answers 409 once a crew exists, and a credential
    // created after Launch is not delivered to agents that already exist.
    expect(stepTwoCode()).not.toMatch(/crewship setup/)
  })

  it("leads with what the crew needs, not with how the human works", () => {
    // The step's title is the card header; it names the agents' requirement.
    expect(stepTwoCode()).not.toMatch(/How will you work\?/)
    const header = stripComments(PAGE.slice(PAGE.indexOf("const header:"), PAGE.indexOf("return (\n    <SetupFrame>")))
    expect(header).toMatch(/Give your agents a model/)
    expect(header).not.toMatch(/How will you work\?/)
  })

  it("says plainly that pairing does not credential the agents", () => {
    expect(PAGE.slice(PAGE.indexOf("const header:"))).toMatch(/does not give them one/)
  })
})

// Each of these is a dead end a first-time user could walk into.
describe("the wizard never strands the person", () => {
  it("says why the primary button is disabled, beside it", () => {
    expect(PAGE).toContain("onboarding-blocking-reason")
    const fn = PAGE.slice(PAGE.indexOf("const blockingReason"), PAGE.indexOf("const persistAdapterCredential"))
    for (const step of ["step === 1", "step === 2", "step === 3"]) expect(fn).toContain(step)
  })

  it("asks before Skip, and Skip is not wired straight to the request", () => {
    expect(PAGE).toMatch(/title="Skip setup\?"/)
    expect(PAGE).toMatch(/onConfirm=\{handleSkip\}/)
    expect(PAGE).not.toMatch(/onClick=\{handleSkip\}/)
  })

  it("explains an unreachable Guide instead of silently showing templates", () => {
    expect(PAGE).toContain("onboarding-guide-unavailable")
  })

  it("keeps Launch reachable after a reload when a crew already exists", () => {
    const gate = PAGE.slice(PAGE.indexOf("const canContinue"), PAGE.indexOf("const blockingReason"))
    expect(gate).toContain("existingCrewCount > 0")
    const launch = PAGE.slice(PAGE.indexOf("async function handleLaunch"), PAGE.indexOf("async function handleSkip"))
    expect(launch).toMatch(/existingCrewCount > 0[\s\S]*\/api\/v1\/onboarding\/complete/)
  })

  it("does not tell a phone user the chat is on the right", () => {
    const step3 = stripComments(PAGE.slice(PAGE.indexOf("{chatStep && ("), PAGE.indexOf('crewMode === "template" && (')))
    expect(step3).not.toMatch(/on the right|on the left/)
  })
})
