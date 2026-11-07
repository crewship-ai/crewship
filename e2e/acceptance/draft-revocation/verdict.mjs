// When a run of the draft revocation proof counts. A green table is not enough:
// a skip, a test that left no row, or a narrowed run proves less than it looks.

export const EXIT = { pass: 0, failed: 1, setup: 2, partial: 3 }

/**
 * @param {{ playwrightStatus: number | null, listed: number, rows: Array<{ result: string }>, narrowed: boolean }} run
 * @returns {{ exit: number, verdict: string, reasons: string[] }}
 */
export function verdict({ playwrightStatus, listed, rows, narrowed }) {
  const count = (result) => rows.filter((row) => row.result === result).length
  const failed = count("failed"), skipped = count("skipped"), passed = count("passed")
  const reasons = []
  if (listed === 0) reasons.push("no test was listed")
  if (failed) reasons.push(`${failed} failed`)
  if (skipped) reasons.push(`${skipped} skipped`)
  if (rows.length !== listed) reasons.push(`${listed} listed but ${rows.length} left a result`)
  if (playwrightStatus !== 0) reasons.push(`playwright exited ${playwrightStatus}`)
  if (reasons.length) return { exit: EXIT.failed, verdict: "FAIL", reasons }
  if (narrowed) return { exit: EXIT.partial, verdict: "PARTIAL", reasons: [`only ${passed} selected tests ran`] }
  return { exit: EXIT.pass, verdict: "PASS", reasons: [] }
}

/** Keeps the native engine settings of an env file and nothing else. Agents stay
 * off: this proof starts no agent, and they need a qualified runtime. */
export function nativeSettings(text) {
  const settings = new Map()
  for (const raw of text.split("\n")) {
    const line = raw.trim()
    const match = /^(CREWSHIP_CRAFT_NATIVE_[A-Z0-9_]+)=(.*)$/.exec(line)
    if (match && match[1] !== "CREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED") settings.set(match[1], match[2])
  }
  return settings
}

/** Word and Sheet are both proven or the run does not start. */
export function missingEngines(settings) {
  return ["WORD", "GRID"].flatMap((engine) => ["ENGINE_REVISION", "CLI_PATH", "CLI_SHA256", "EDITOR_BUNDLE", "EDITOR_URL"].map((name) => `CREWSHIP_CRAFT_NATIVE_${engine}_${name}`))
    .concat("CREWSHIP_CRAFT_NATIVE_ENABLED", "CREWSHIP_CRAFT_NATIVE_EDITOR_BUNDLE_DIR")
    .filter((name) => !settings.get(name))
}
