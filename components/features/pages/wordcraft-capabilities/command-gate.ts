import { compileParameterValidator, type ParameterSchema } from './parameter-validator'
export type { ParameterSchema } from './parameter-validator'
import published from "@/schemas/wordcraft/capabilities.json"

export type CommandParameters = Readonly<Record<string, unknown>>
export interface CapabilityCommand {
  id: string
  label: string
  location: string
  shortcut: string
  group: string
  qualification: string
  permanentlyForbidden: boolean
  schemaStatus: string
  parameterSchema: false | ParameterSchema
}
export interface CapabilityCatalog {
  version: string
  engine: { sha: string }
  commands: readonly CapabilityCommand[]
}
/** Effective command IDs from the server's existing participant authorization.
 * This is display/dispatch input, never an independent role authority. */
export interface ParticipantCapabilities {
  engineSha: string
  catalogVersion: string
  allowedCommands: readonly string[]
}
export interface CommandAvailability { enabled: boolean; reason?: typeof DENIED_LABEL }
export const DENIED_LABEL = "nemáte oprávnění"
// JSON module inference widens the generated false schema literals to boolean.
export const defaultWordCraftCatalog = published as CapabilityCatalog
const known = new Map(defaultWordCraftCatalog.commands.map(command => [command.id, command]))
const denied: CommandAvailability = { enabled: false, reason: DENIED_LABEL }

/** Client affordance only. The server must independently validate the same
 * catalog, role, parameters, target, revision and nested command effects. */
export function createWordCraftCommandGate({ catalog = defaultWordCraftCatalog, participant, dispatch }: {
  catalog?: CapabilityCatalog
  participant?: ParticipantCapabilities
  dispatch: (id: string, parameters: CommandParameters) => void
}) {
  const commands = new Map(catalog.commands.map(command => [command.id, command]))
  const validators = new Map(catalog.commands.filter(command => command.qualification === 'qualified' && command.schemaStatus === 'exact' && !command.permanentlyForbidden).map(command => [command.id, compileParameterValidator(command.parameterSchema)]))
  const allowed = new Set(participant?.allowedCommands)
  const versionMatches = catalog.engine.sha === published.engine.sha && catalog.version === published.version
    && participant?.engineSha === catalog.engine.sha && participant.catalogVersion === catalog.version
  function availability(id: string): CommandAvailability {
    const original = known.get(id), command = commands.get(id)
    if (!versionMatches || !original || !command || original.permanentlyForbidden || command.permanentlyForbidden
      || command.qualification !== "qualified" || command.schemaStatus !== "exact" || !command.parameterSchema || !validators.get(id) || !allowed.has(id)) return denied
    return { enabled: true }
  }
  function execute(id: string, parameters: CommandParameters = {}): boolean {
    const command = commands.get(id)
    if (!availability(id).enabled || !command || !validators.get(id)?.(parameters)) return false
    dispatch(id, parameters)
    return true
  }
  return { availability, execute }
}

export interface ShortcutEvent {
  key: string
  code?: string
  ctrlKey: boolean
  metaKey: boolean
  altKey: boolean
  shiftKey: boolean
  isComposing?: boolean
  nativeEvent?: { isComposing?: boolean }
  preventDefault(): void
  stopPropagation(): void
}

function matchesShortcut(shortcut: string, event: ShortcutEvent, platform: "mac" | "other"): boolean {
  const parts = shortcut.split("+")
  const key = parts.pop()?.toLowerCase()
  const modifiers = new Set(parts.map(part => part.toLowerCase()))
  if (!key || [...modifiers].some(part => !["mod", "ctrl", "meta", "alt", "shift"].includes(part))) return false
  const logicalKey = event.key === " " ? "space" : event.key.toLowerCase().replace(/^arrow/, "")
  // The registry uses the engine's unshifted key names (Shift+=, Shift+8).
  // DOM key contains the produced glyph (+, *); code retains the physical key.
  const physicalKeys: Record<string, string> = { Equal: "=", Minus: "-", Comma: ",", Period: ".", Slash: "/", Semicolon: ";", Quote: "'", Backquote: "`", BracketLeft: "[", BracketRight: "]", Backslash: "\\" }
  const physicalKey = event.code?.startsWith("Digit") ? event.code.slice(5) : event.code && physicalKeys[event.code]
  return (key === logicalKey || key === physicalKey)
    && event.ctrlKey === (modifiers.has("ctrl") || modifiers.has("mod") && platform === "other")
    && event.metaKey === (modifiers.has("meta") || modifiers.has("mod") && platform === "mac")
    && event.altKey === modifiers.has("alt") && event.shiftKey === modifiers.has("shift")
}

/** Attach only to the editor surface. A denied or ambiguous registered shortcut
 * is consumed so it cannot fall through to an ungated native handler. */
export function wordCraftShortcutHandler({ catalog = defaultWordCraftCatalog, gate, parameters = {}, platform = "other" }: {
  catalog?: CapabilityCatalog
  gate: ReturnType<typeof createWordCraftCommandGate>
  parameters?: Readonly<Record<string, CommandParameters>>
  platform?: "mac" | "other"
}) {
  return (event: ShortcutEvent): boolean => {
    if (event.isComposing || event.nativeEvent?.isComposing) return false
    // Keep native pinned bindings consumable even if the supplied catalog omits
    // or relabels an unavailable command. Unknown bindings still fail closed.
    const matches = [...new Set([...defaultWordCraftCatalog.commands, ...catalog.commands]
      .filter(command => command.shortcut.split(" / ").some(shortcut => matchesShortcut(shortcut, event, platform)))
      .map(command => command.id))]
    if (!matches.length) return false
    event.preventDefault()
    event.stopPropagation()
    if (matches.length === 1) gate.execute(matches[0], parameters[matches[0]] ?? {})
    return true
  }
}
