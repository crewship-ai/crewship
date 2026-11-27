"use client"
import type { NativeCraftHandBack } from "@/components/features/pages/native-craft-editor-host"
import { createContext, useContext, type ReactNode } from "react"

/**
 * What a Sheet card may ask of the chat's side pane. Absent (null) where there
 * is no side pane for a Sheet, for instance on a phone: the card then links to
 * the whole page instead.
 */
export interface SheetPanelValue {
  /** Opens this Sheet in the side pane, replacing what was shown there. */
  open(pageId: string): void
  /** The agent changed this Sheet in the live turn; the pane opens once per Sheet and conversation, never again after it was closed. */
  noteChanged(pageId: string): void
  /** The Sheet shown now, if any. */
  activePageId: string | null
  /** The Sheet the person edits beside this chat, if any. */
  editingPageId?: string | null
  /** The chat beside the panel, once it is on the server: a Sheet made here belongs to it. */
  chatId?: string | null
  /** Hands editing of that Sheet back, so the agent may change it. */
  handBack?(): Promise<NativeCraftHandBack | undefined>
}

/** Where no Sheet opens beside the chat (a phone): picking a Sheet for the next message instead. */
export interface SheetPickValue {
  choose(pageId: string): void
  chosenPageId: string | null
}

const SheetPanelContext = createContext<SheetPanelValue | null>(null)
const SheetPickContext = createContext<SheetPickValue | null>(null)
// Separate from the panel: a phone has cards but no side pane. It is also what
// turns Sheet cards on; they do not depend on the artifact projects build flag.
const SheetCardsContext = createContext<string | null>(null)

export function SheetPanelProvider({ value, workspaceId = null, pick = null, children }: { value: SheetPanelValue | null
  /** Picking a Sheet for the next message, where there is no side pane. */
  pick?: SheetPickValue | null
  /** The workspace whose Sheets the cards of this chat read; absent, no Sheet card shows. */
  workspaceId?: string | null
  children: ReactNode }) {
  return <SheetCardsContext.Provider value={workspaceId}>
    <SheetPanelContext.Provider value={value}><SheetPickContext.Provider value={pick}>{children}</SheetPickContext.Provider></SheetPanelContext.Provider>
  </SheetCardsContext.Provider>
}

/** The workspace of the Sheet cards in this chat; null where a chat shows none. */
export function useSheetCardsWorkspace(): string | null {
  return useContext(SheetCardsContext)
}

export function useSheetPick(): SheetPickValue | null {
  return useContext(SheetPickContext)
}

export function useSheetPanel(): SheetPanelValue | null {
  return useContext(SheetPanelContext)
}
