type Definition = Record<string, unknown>
export interface PublicationGroup {
  label: string
  added: string[]
  changed: string[]
  removed: string[]
  reordered: boolean
  readable: boolean
}
const canonical = (value: unknown): string => {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`
  if (value && typeof value === "object")
    return `{${Object.entries(value)
      .filter(([, v]) => v !== undefined)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([k, v]) => `${JSON.stringify(k)}:${canonical(v)}`)
      .join(",")}}`
  return JSON.stringify(value) ?? ""
}
function entries(value: unknown, key: string): Definition[] | null {
  if (value === undefined) return []
  if (
    !Array.isArray(value) ||
    value.some((v) => !v || typeof v !== "object" || Array.isArray(v) || typeof v[key] !== "string")
  )
    return null
  return new Set(value.map((v) => v[key])).size === value.length ? value : null
}
export function routinePublicationChanges(
  before: Definition,
  after: Definition,
): { groups: PublicationGroup[]; settings: string[] } {
  const groups = (
    [
      ["steps", "id", "Workflow steps"],
      ["inputs", "name", "Input questions"],
      ["outputs", "name", "Expected results"],
    ] as const
  ).map(([field, key, label]) => {
    const old = entries(before[field], key)
    const next = entries(after[field], key)
    const group: PublicationGroup = {
      label,
      added: [],
      changed: [],
      removed: [],
      reordered: false,
      readable: old !== null && next !== null,
    }
    if (!old || !next) return group
    const previous = new Map(old.map((v) => [v[key], v]))
    const current = new Map(next.map((v) => [v[key], v]))
    const title = (v: Definition) => String(v.label || v.name || v[key])
    for (const value of next) {
      const prior = previous.get(value[key])
      if (!prior) group.added.push(title(value))
      else if (canonical(value) !== canonical(prior)) group.changed.push(title(value))
    }
    for (const value of old) if (!current.has(value[key])) group.removed.push(title(value))
    group.reordered =
      canonical(old.filter((v) => current.has(v[key])).map((v) => v[key])) !==
      canonical(next.filter((v) => previous.has(v[key])).map((v) => v[key]))
    return group
  })
  const settings = [...new Set([...Object.keys(before), ...Object.keys(after)])].filter(
    (key) =>
      !["steps", "inputs", "outputs"].includes(key) &&
      canonical(before[key]) !== canonical(after[key]),
  )
  return { groups, settings }
}
