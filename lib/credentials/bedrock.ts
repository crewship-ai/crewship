export const BEDROCK_SLOT = "AWS_BEARER_TOKEN_BEDROCK"

/** OpenCode's provider id; an agent's llm_model is `amazon-bedrock/<native id>`. */
export const BEDROCK_MODEL_PREFIX = "amazon-bedrock/"

/**
 * Regions the server has a Bedrock runtime endpoint for
 * (internal/bedrock/account.go Endpoint). EU first: the EU-only policy needs
 * one of them, and it is the default for new accounts.
 */
export const BEDROCK_REGIONS: ReadonlyArray<{ id: string; label: string; eu: boolean }> = [
  { id: "eu-central-1", label: "Europe (Frankfurt)", eu: true },
  { id: "eu-central-2", label: "Europe (Zurich)", eu: true },
  { id: "eu-west-1", label: "Europe (Ireland)", eu: true },
  { id: "eu-west-2", label: "Europe (London)", eu: true },
  { id: "eu-west-3", label: "Europe (Paris)", eu: true },
  { id: "eu-north-1", label: "Europe (Stockholm)", eu: true },
  { id: "eu-south-1", label: "Europe (Milan)", eu: true },
  { id: "eu-south-2", label: "Europe (Spain)", eu: true },
  { id: "us-east-1", label: "US East (N. Virginia)", eu: false },
  { id: "us-east-2", label: "US East (Ohio)", eu: false },
  { id: "us-west-2", label: "US West (Oregon)", eu: false },
  { id: "ca-central-1", label: "Canada (Central)", eu: false },
  { id: "sa-east-1", label: "South America (São Paulo)", eu: false },
  { id: "ap-northeast-1", label: "Asia Pacific (Tokyo)", eu: false },
  { id: "ap-northeast-2", label: "Asia Pacific (Seoul)", eu: false },
  { id: "ap-northeast-3", label: "Asia Pacific (Osaka)", eu: false },
  { id: "ap-south-1", label: "Asia Pacific (Mumbai)", eu: false },
  { id: "ap-south-2", label: "Asia Pacific (Hyderabad)", eu: false },
  { id: "ap-southeast-1", label: "Asia Pacific (Singapore)", eu: false },
  { id: "ap-southeast-2", label: "Asia Pacific (Sydney)", eu: false },
]

/**
 * Inference profiles offered as checkboxes when an account is connected.
 *
 * Not a catalog of what an account may use: the account's own allowlist is,
 * and the probe confirms each model against AWS before it is saved. This is
 * only the short list people pick from instead of typing ids; any other
 * profile id can still be added by hand. Bedrock is deliberately not in
 * config/models.json — the server must not substitute a curated model for an
 * account-scoped one.
 */
export const BEDROCK_PROFILES: readonly string[] = [
  "eu.anthropic.claude-opus-5-5",
  "eu.anthropic.claude-sonnet-5-5",
  "eu.anthropic.claude-haiku-5-5",
  "eu.anthropic.claude-opus-5",
  "eu.anthropic.claude-sonnet-5",
  "eu.anthropic.claude-fable-5",
  "eu.amazon.nova-pro-v1:0",
  "eu.amazon.nova-2-lite-v1:0",
  "global.anthropic.claude-opus-5-5",
  "global.anthropic.claude-sonnet-5-5",
  "global.anthropic.claude-haiku-5-5",
  "global.anthropic.claude-fable-5-2",
  "global.openai.gpt-6.1-sol",
  "global.moonshotai.kimi-k3.1",
  "global.zai.glm-5.3",
]

const GEO: Record<string, string> = { eu: "EU", us: "US", global: "Global", apac: "APAC", jp: "JP", au: "AU", ca: "CA", "us-gov": "US GovCloud" }

/** The native id (`eu.anthropic.claude-opus-5-5`) of an agent model or a native id. */
export function bedrockNativeModel(id: string): string {
  const v = id.trim()
  return v.startsWith(BEDROCK_MODEL_PREFIX) ? v.slice(BEDROCK_MODEL_PREFIX.length) : v
}

/** The agent's llm_model for a native Bedrock id. */
export function bedrockAgentModel(id: string): string {
  return BEDROCK_MODEL_PREFIX + bedrockNativeModel(id)
}

/**
 * A readable name for a Bedrock model id: `eu.anthropic.claude-opus-5-5`
 * reads "Claude Opus 5.5 · EU". Version and date suffixes are dropped; the
 * geography of an inference profile is kept, because it is what decides
 * where the request may be served.
 */
export function bedrockModelLabel(id: string): string {
  const parts = bedrockNativeModel(id).split(".")
  if (!parts[0]) return ""
  const geo = parts.length > 2 && GEO[parts[0]] ? GEO[parts.shift()!] : null
  if (parts.length > 1) parts.shift() // vendor: anthropic, amazon, meta…
  const base = parts.join(".")
    .replace(/:\d+$/, "")
    .replace(/-v\d+$/, "")
    .replace(/-\d{8}$/, "")
  const words: string[] = []
  for (const token of base.split("-").filter(Boolean)) {
    const prev = words[words.length - 1]
    if (/^\d+$/.test(token) && prev && /^\d+(\.\d+)*$/.test(prev)) words[words.length - 1] = `${prev}.${token}`
    else words.push(/^[a-z]/.test(token) ? token[0].toUpperCase() + token.slice(1) : token)
  }
  const name = words.join(" ")
  return geo ? `${name} · ${geo}` : name
}

interface BedrockSeat {
  login?: { bedrock?: { models?: string[] } | null } | null
}

/** The models an account permits, as native ids. */
export function bedrockAccountModels(seat: BedrockSeat): string[] {
  return seat.login?.bedrock?.models ?? []
}

/**
 * The native model to assign when an agent starts paying with `seat`: the
 * agent's own model when the account permits it, else the account's first.
 */
export function bedrockModelFor(seat: BedrockSeat, agentModel: string): string {
  const models = bedrockAccountModels(seat)
  const own = bedrockNativeModel(agentModel)
  return models.includes(own) ? own : models[0] ?? ""
}

export function bedrockModels(value: string): string[] {
  return value.split(/[\s,]+/).map((v) => v.trim().replace(/^amazon-bedrock\//, "")).filter(Boolean)
}

export function bedrockAccountValue(key: string, region: string, models: string[], expires: string, euOnly: boolean): string {
  const time = new Date(expires)
  return JSON.stringify({ auth_method: "bedrock_api_key", api_key: key.trim(), region: region.trim(), models, expires_at: Number.isNaN(time.getTime()) ? "" : time.toISOString(), eu_only: euOnly })
}

export function bedrockEntryError(region: string, models: string[], expires: string, euOnly: boolean): string | null {
  if (!/^(us|eu|ap|ca|sa|me|af|il|mx)-(north|south|east|west|central|northeast|northwest|southeast|southwest)-[1-9][0-9]?$/.test(region)) return "Enter the AWS region where this Bedrock key was created."
  if (models.length < 1 || models.length > 32 || new Set(models).size !== models.length || models.some((m) => !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/.test(m))) return "Enter 1–32 unique native Bedrock model IDs."
  if (euOnly && (!region.startsWith("eu-") || models.some((m) => !m.startsWith("eu.")))) return "EU-only requires an EU region and explicit eu. model IDs."
  if (!expires || !Number.isFinite(new Date(expires).getTime()) || new Date(expires).getTime() <= Date.now()) return "Enter the key's future expiration date."
  return null
}
