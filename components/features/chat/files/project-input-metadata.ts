/** The only optional metadata supported by the restricted native chat route. */
export function projectInputVersionIds(metadata?: Record<string, unknown>): string[] | null {
  if (!metadata || Object.keys(metadata).length === 0) return []
  if (Object.keys(metadata).length !== 1 || !Object.hasOwn(metadata, "project_file_versions")) return null
  const ids = metadata.project_file_versions
  if (!Array.isArray(ids) || ids.length > 16 || ids.some(id => typeof id !== "string" || !/^[a-zA-Z0-9_-]{1,96}$/.test(id)) || new Set(ids).size !== ids.length) return null
  return [...ids] as string[]
}
