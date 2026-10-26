/** Immutable storage format selects a producer. Unknown formats never fall
 * back to a different engine or create a blank native document. */
export function wordCraftRenderer(format: string | null | undefined, commandsAvailable: boolean): "legacy" | "commands" | "unavailable" {
  if (format === undefined || format === "mooring-causal-v1") return "legacy"
  return format === "wordcraft-c1-v2" && commandsAvailable ? "commands" : "unavailable"
}
