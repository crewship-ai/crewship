// Local shape checks only. A successful check does not verify the provider.
export function credentialEntryError(value: string, type: string, provider: string, mode: string): string | null {
  if (new TextEncoder().encode(value).length > 64 * 1024) return "The value must be no larger than 64 KiB."
  if (!value.trim()) return null // required-field handling supplies its label
  if (type === "PROVIDER_LOGIN" && mode === "subscription") {
    if (provider === "ANTHROPIC") return value.trim().startsWith("sk-ant-oat") ? null : "Paste the setup token from claude setup-token, or choose API key."
    if (provider === "OPENAI" || provider === "GOOGLE") {
      let parsed: Record<string, unknown>
      try { parsed = JSON.parse(value); if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error() }
      catch { return "Paste the complete login JSON object or choose its file." }
      const fields = provider === "OPENAI" ? ["access_token", "id_token"] : ["access_token", "refresh_token"]
      const tokens = (provider === "OPENAI" ? parsed.tokens : parsed) as Record<string, unknown> | undefined
      for (const field of fields) {
        if (typeof tokens?.[field] !== "string" || !(tokens[field] as string).trim()) return `The login file is missing ${provider === "OPENAI" ? "tokens." : ""}${field}.`
      }
      if (provider === "GOOGLE" && (typeof parsed.expiry_date !== "number" || parsed.expiry_date <= 0)) return "The login file is missing a valid expiry_date."
    }
  }
  if (type === "SSH_KEY" || type === "CERTIFICATE") {
    const label = value.trim().match(/^-----BEGIN ([A-Z ]+)-----/)?.[1]
    const marker = type === "SSH_KEY" ? "PRIVATE KEY" : "CERTIFICATE"
    if (!label?.endsWith(marker) || !value.includes(`-----END ${label}-----`)) return `Paste the complete ${type === "SSH_KEY" ? "private key" : "certificate"}, including its BEGIN and END lines.`
  }
  return null
}
