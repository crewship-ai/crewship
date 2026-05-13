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

/**
 * Per-adapter "how do I get a CLI token?" docs. These deliberately
 * point at our own future docs space (docs.crewship.ai/cli-tokens/<adapter>)
 * rather than the vendor's API-key console: onboarding accepts CLI
 * tokens ONLY — the OAuth-style values that come out of
 * `claude setup-token`, `gemini auth print-token` etc. — never the
 * raw account-level API keys those console pages hand out, because
 * the agent runtime's OAuth CONNECT-tunnel mode can only inject the
 * former.
 *
 * The pages don't exist yet; until they do the URLs are temporary
 * deep-links into the vendor's own CLI-auth docs so a user clicking
 * "How to generate a CLI token →" lands somewhere actionable. Swap
 * to the Crewship guide path the moment docs ship.
 */
export const ADAPTER_TOKEN_GUIDE: Record<string, { url: string; label: string }> = {
  CLAUDE_CODE: {
    url: "https://docs.claude.com/en/docs/claude-code/setup#anthropic-api-key",
    label: "How to generate a Claude Code CLI token",
  },
  OPENCODE: {
    url: "https://opencode.ai/docs/cli/#authentication",
    label: "How to generate an OpenCode CLI token",
  },
  CODEX_CLI: {
    url: "https://developers.openai.com/codex/auth",
    label: "How to generate a Codex CLI token",
  },
  GEMINI_CLI: {
    url: "https://ai.google.dev/gemini-api/docs/cli#authentication",
    label: "How to generate a Gemini CLI token",
  },
  CURSOR_CLI: {
    url: "https://cursor.com/docs/cli#authentication",
    label: "How to generate a Cursor CLI token",
  },
  FACTORY_DROID: {
    url: "https://docs.factory.ai/cli/auth",
    label: "How to generate a Factory CLI token",
  },
}

/**
 * One-line command the user can copy-paste in their terminal to
 * produce the CLI token Crewship expects. Shown inline next to the
 * input field — beats hunting through external docs for the right
 * incantation. Order of these strings matches the vendor's primary
 * advertised onboarding command (`<cli> setup-token` family).
 */
export const ADAPTER_TOKEN_CMD: Record<string, string> = {
  CLAUDE_CODE: "claude setup-token",
  OPENCODE: "opencode auth",
  CODEX_CLI: "codex login",
  GEMINI_CLI: "gemini auth print-token",
  CURSOR_CLI: "cursor login",
  FACTORY_DROID: "droid auth login",
}

/**
 * Install / docs URL for each adapter's local CLI binary — used when
 * the user picks "Pair my CLI" so they know where to grab the tool
 * if they don't already have it.
 */
export const ADAPTER_CLI_INSTALL: Record<string, { url: string; label: string }> = {
  CLAUDE_CODE: { url: "https://docs.claude.com/code", label: "Install Claude Code" },
  OPENCODE:    { url: "https://opencode.ai/docs",      label: "Install OpenCode" },
  CODEX_CLI:   { url: "https://platform.openai.com/docs/codex/overview", label: "Install Codex CLI" },
  GEMINI_CLI:  { url: "https://ai.google.dev/gemini-api/docs/cli", label: "Install Gemini CLI" },
  CURSOR_CLI:  { url: "https://cursor.com/docs/cli",   label: "Install Cursor CLI" },
  FACTORY_DROID: { url: "https://docs.factory.ai/cli", label: "Install Factory Droid" },
}
