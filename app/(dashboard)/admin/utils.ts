const SECRET_PATTERNS = [
  /sk-ant-[a-zA-Z0-9_-]{20,}/g,
  /sk-[a-zA-Z0-9]{20,}/g,
  /AIza[a-zA-Z0-9_-]{35}/g,
  /AKIA[A-Z0-9]{16}/g,
  /Bearer\s+[a-zA-Z0-9._-]{20,}/g,
  /-----BEGIN[A-Z ]*PRIVATE KEY-----[\s\S]*?-----END[A-Z ]*PRIVATE KEY-----/g,
  /ghp_[a-zA-Z0-9]{36}/g,
  /gho_[a-zA-Z0-9]{36}/g,
]

/**
 * Redact known secret patterns (API keys, tokens, private keys) from text.
 * Preserves the first 8 characters for identification, replacing the rest with "***REDACTED***".
 */
export function redactSecrets(text: string): string {
  let result = text
  for (const pattern of SECRET_PATTERNS) {
    result = result.replace(pattern, (m) => m.slice(0, 8) + "***REDACTED***")
  }
  return result
}

/** Redact credentials from a URL (username, password, query string) for safe display. */
export function redactUrl(raw: string): string {
  try {
    const url = new URL(raw)
    if (url.username || url.password) {
      url.username = url.username ? "****" : ""
      url.password = ""
    }
    if (url.search) url.search = ""
    return url.toString()
  } catch {
    return raw
  }
}
