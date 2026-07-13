// Env-var-name validation — the credential "name" doubles as the ENV
// variable agents read inside their container (see credential-form.tsx
// and the backend env_var_name plumbing), so it must be a valid POSIX
// environment variable name. Same pattern as the assign-credential
// dialog's env_var input and the Go-side validation.

/** POSIX-ish env var name: uppercase letters, digits, underscore; no leading digit. */
export const ENV_VAR_NAME_RE = /^[A-Z_][A-Z0-9_]*$/

export function isValidEnvVarName(name: string): boolean {
  return ENV_VAR_NAME_RE.test(name)
}

/**
 * Best-effort normalisation of a free-form name into a valid env var
 * name: uppercase, separators (space/dash/dot) → underscore, drop
 * anything else, collapse runs, prefix a leading digit.
 *
 * Returns null when nothing salvageable remains — the caller should
 * then just show the plain validation error without a suggestion.
 */
export function suggestEnvVarName(raw: string): string | null {
  const trimmed = raw.trim()
  if (isValidEnvVarName(trimmed)) return trimmed

  let s = trimmed
    .toUpperCase()
    .replace(/[\s.-]+/g, "_")
    .replace(/[^A-Z0-9_]/g, "")
    .replace(/_{2,}/g, "_")
  if (s !== "" && /^[0-9]/.test(s)) s = `_${s}`
  return isValidEnvVarName(s) ? s : null
}
