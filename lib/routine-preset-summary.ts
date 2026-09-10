import { clip, isRecord } from "./routine-step-describe"

const line = (value: string, max: number) =>
  clip(value.split(/\r?\n/).find((part) => part.trim()) ?? "", max)

/** A bounded preview, never JSON serialization or a tooltip containing raw inputs.
 * Credential/file-shaped values and sensitive field names are intentionally
 * summarized by type. Arbitrary nested objects only contribute to the count.
 */
export function routinePresetSummary(inputs: unknown): string {
  if (!isRecord(inputs)) return "Inputs unavailable"
  const entries = Object.entries(inputs)
  if (!entries.length) return "No inputs"
  const shown: string[] = []
  for (const [key, value] of entries) {
    if (shown.length === 2) break
    const fieldWords = key.replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    let preview: string | undefined
    if (/credential/i.test(key) || (isRecord(value) && ("credential_ref" in value || value.type === "credential"))) {
      preview = "Credential reference"
    } else if (/password|secret|token|api.?key|authorization|private.?key/i.test(key)) {
      preview = "Hidden"
    } else if (/(?:^|[_-])(?:files?|attachments?|documents?)(?:$|[_-])/i.test(fieldWords) || (isRecord(value) && (value.type === "file" || "filename" in value)) || (typeof value === "string" && /^(data:|file:|blob:)/i.test(value))) {
      const name = isRecord(value) ? value.name ?? value.filename : undefined
      preview = typeof name === "string" ? `File (${line(name.split(/[/\\]/).pop()!, 32)})` : "File"
    } else if (typeof value === "string") {
      preview = /^(credential:|vault:)/i.test(value) ? "Credential reference" : line(value, 56) || '""'
    } else if (value === null || typeof value === "boolean" || typeof value === "number") {
      preview = String(value)
    }
    if (preview !== undefined) shown.push(`${line(key, 24)}: ${preview}`)
  }
  const remaining = entries.length - shown.length
  if (remaining) shown.push(`+${remaining} more`)
  return `Inputs: ${shown.join(" · ")}`
}
