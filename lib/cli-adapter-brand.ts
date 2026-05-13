/**
 * Brand-accurate fills for the six supported CLI adapters. These map
 * onto the brand colour each vendor actually uses in their own marks
 * (not the corporate web-safe colours that get blogged about) so the
 * onboarding adapter picker shows the same chips users see in
 * Claude / ChatGPT / Cursor / Factory desktop apps.
 *
 * Tints (bg/border with reduced opacity) come from the
 * crewship-web design system so the chip styling matches the
 * marketing site without re-deriving the math.
 */

export interface AdapterBrand {
  /** Solid icon fill — usually the brand hex. */
  fg: string
  /** Background tint for the icon container (10–14% alpha). */
  bg: string
  /** Border tint for the icon container (35–45% alpha). */
  border: string
}

export const ADAPTER_BRAND: Record<string, AdapterBrand> = {
  // Anthropic's Claude product uses a warm peach in the wordmark and
  // product surfaces (not the corporate near-black). Matches how
  // Claude Code itself renders in the user's terminal.
  CLAUDE_CODE: {
    fg: "#D97757",
    bg: "rgba(217, 119, 87, 0.12)",
    border: "rgba(217, 119, 87, 0.40)",
  },
  // OpenAI's monochrome wordmark; the green of the ChatGPT app icon
  // is the practical "brand colour" most users associate with the
  // company.
  CODEX_CLI: {
    fg: "#10A37F",
    bg: "rgba(16, 163, 127, 0.12)",
    border: "rgba(16, 163, 127, 0.40)",
  },
  // Google's primary blue from the 4-colour G. Single-colour fill
  // here keeps the chip readable at 16–20px.
  GEMINI_CLI: {
    fg: "#4285F4",
    bg: "rgba(66, 133, 244, 0.12)",
    border: "rgba(66, 133, 244, 0.40)",
  },
  // Cursor's signature cyan — the colour of the active cursor in the
  // editor and the primary accent across cursor.com.
  CURSOR_CLI: {
    fg: "#22D3EE",
    bg: "rgba(34, 211, 238, 0.12)",
    border: "rgba(34, 211, 238, 0.40)",
  },
  // Factory's "Droid" mark uses an amber/gold body across factory.ai.
  FACTORY_DROID: {
    fg: "#F59E0B",
    bg: "rgba(245, 158, 11, 0.14)",
    border: "rgba(245, 158, 11, 0.40)",
  },
  // OpenCode has no consumer brand identity. Neutral light-gray reads
  // as "tooling" without competing with the named vendors.
  OPENCODE: {
    fg: "#A1A1AA",
    bg: "rgba(161, 161, 170, 0.12)",
    border: "rgba(161, 161, 170, 0.40)",
  },
}

/**
 * Safe lookup that falls back to a neutral grey for unknown adapter
 * keys. Used in the onboarding adapter picker so adding a 7th adapter
 * to the registry doesn't crash the UI before its brand chip lands.
 */
export function getAdapterBrand(key: string): AdapterBrand {
  return ADAPTER_BRAND[key] ?? {
    fg: "#A1A1AA",
    bg: "rgba(161, 161, 170, 0.12)",
    border: "rgba(161, 161, 170, 0.40)",
  }
}
