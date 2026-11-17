export const BEDROCK_SLOT = "AWS_BEARER_TOKEN_BEDROCK"

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
