"use client"

import { useSyncExternalStore } from "react"
import type { WordCraftConnectionMonitor, WordCraftConnectionState } from "./connection-monitor"

export function wordCraftConnectionLabel(state: WordCraftConnectionState): string {
  if (state.phase === "disconnected") return state.hasUnconfirmedDraft ? "nespojeno · neuloženo, rozepsaný text zachován" : "nespojeno · stav uložení nepotvrzen"
  if (state.phase === "connecting") return state.hasUnconfirmedDraft ? "připojování · neuloženo, rozepsaný text zachován" : "připojování · stav uložení nepotvrzen"
  if (state.hasUnconfirmedDraft) return "neuloženo · rozepsaný text zachován"
  if (state.serverSaveState === "saved") return "Uloženo"
  return state.serverSaveState === "saving" ? "Ukládání…" : "stav uložení nepotvrzen"
}

/** Opt-in status line for the command editor. Render beside the mounted editor;
 * a disconnected state must never become a conditional editor mount. */
export function WordCraftConnectionIndicator({ monitor }: { monitor: WordCraftConnectionMonitor }) {
  const state = useSyncExternalStore(monitor.subscribe, monitor.getSnapshot, monitor.getSnapshot)
  return <span role="status" aria-live="polite" className="text-sm" data-wordcraft-connection={state.phase} data-wordcraft-revision={state.revision ?? undefined}>
    {wordCraftConnectionLabel(state)}
  </span>
}
