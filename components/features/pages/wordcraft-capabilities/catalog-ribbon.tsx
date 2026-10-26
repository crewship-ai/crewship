"use client"

import { useId } from "react"
import { createWordCraftCommandGate, defaultWordCraftCatalog, DENIED_LABEL, type CapabilityCatalog, type CommandParameters, type ParticipantCapabilities } from "./command-gate"

const groupLabels: Record<string, string> = {
  "text-enter": "Text a Enter", "character-format": "Znakový formát", paragraphs: "Odstavce", lists: "Seznamy",
  tables: "Tabulky", images: "Obrázky", comments: "Komentáře", "tracked-changes": "Sledované změny",
  "process-global": "Globální stav procesu", unclassified: "Nekvalifikované funkce",
}

/** Opt-in command surface for the C1 host. The engine's own ribbon and keyboard
 * path must use the same gate when integrated; mounting this component alone
 * does not intercept events inside an iframe or authorize server execution. */
export function WordCraftCatalogRibbon({ catalog = defaultWordCraftCatalog, participant, commandIds, parameters = {}, onCommand }: {
  catalog?: CapabilityCatalog
  participant?: ParticipantCapabilities
  commandIds?: readonly string[]
  parameters?: Readonly<Record<string, CommandParameters>>
  onCommand: (id: string, parameters: CommandParameters) => void
}) {
  const reasonId = useId()
  const gate = createWordCraftCommandGate({ catalog, participant, dispatch: onCommand })
  const visible = catalog.commands.filter(command => !commandIds || commandIds.includes(command.id))
  const groups = [...new Set(visible.map(command => command.group))]
  return <div role="toolbar" aria-label="WordCraft">
    {visible.some(command => !gate.availability(command.id).enabled) && <p id={reasonId}>{DENIED_LABEL}</p>}
    {groups.map(group => <fieldset key={group} className="inline-flex flex-wrap gap-1 border-0 p-1">
      <legend>{groupLabels[group] ?? groupLabels.unclassified}</legend>
      {visible.filter(command => command.group === group).map(command => {
        const availability = gate.availability(command.id)
        return <span key={command.id} title={availability.reason}>
          <button type="button" data-wordcraft-command={command.id} disabled={!availability.enabled}
            aria-describedby={!availability.enabled ? reasonId : undefined}
            className="rounded border px-2 py-1 disabled:opacity-50 coarse:min-h-12"
            onMouseDown={event => event.preventDefault()}
            onClick={() => gate.execute(command.id, parameters[command.id] ?? {})}>
            {command.label}
          </button>
        </span>
      })}
    </fieldset>)}
  </div>
}
