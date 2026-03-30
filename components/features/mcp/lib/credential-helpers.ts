/** Check whether a value looks like a credential reference: ${SOME_VAR} */
export function isCredentialRef(value: string): boolean {
  return /^\$\{[A-Z_][A-Z0-9_]*\}$/.test(value)
}

/** Derive a credential name from an env var key. E.g. GITHUB_TOKEN -> github-token */
export function deriveCredentialName(envKey: string): string {
  return envKey.toLowerCase().replace(/_/g, "-")
}
