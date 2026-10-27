"use client"
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react"
import { useArtifactProjectsAvailable } from "./use-artifact-project-data"

export type ArtifactViewerTab = "preview" | "comments" | "versions"

export interface ArtifactViewerTarget {
  slug: string
  version?: number
  tab?: ArtifactViewerTab
  /** What opened the viewer (a chat card's tool call id), so only that card shows as open when several show one project. */
  source?: string
}

export interface ArtifactViewerValue {
  workspaceId: string
  target: ArtifactViewerTarget | null
  open(target: ArtifactViewerTarget): void
  close(): void
}

const ArtifactViewerContext = createContext<ArtifactViewerValue | null>(null)
// Separate from the viewer value so the chat transcript can ask "are artifact
// cards on?" without subscribing every turn to target changes, and without a
// query hook in components that render outside a QueryClientProvider.
const ArtifactCardsAvailableContext = createContext(false)

/** Mounting the provider is the feature switch for artifact project cards in chat. */
export function ArtifactViewerProvider({ workspaceId, children, target: controlled, onTargetChange }: {
  workspaceId: string
  children: ReactNode
  /** Controlled mode: the chat panel owns the target because it lays out the side pane. */
  target?: ArtifactViewerTarget | null
  onTargetChange?: (target: ArtifactViewerTarget | null) => void
}) {
  const [own, setOwn] = useState<ArtifactViewerTarget | null>(null)
  const target = controlled === undefined ? own : controlled
  const change = useRef(onTargetChange)
  change.current = onTargetChange
  const setTarget = useCallback((next: ArtifactViewerTarget | null) => {
    if (change.current) change.current(next)
    else setOwn(next)
  }, [])
  const opener = useRef<HTMLElement | null>(null)
  const isOpen = useRef(false)
  const available = useArtifactProjectsAvailable(workspaceId)

  const open = useCallback((next: ArtifactViewerTarget) => {
    // Keep the first opener while the viewer is already open: switching tabs or
    // versions from inside the viewer must not make focus return into it.
    if (!isOpen.current) {
      const active = document.activeElement
      opener.current = active instanceof HTMLElement && active !== document.body ? active : null
    }
    isOpen.current = true
    setTarget(next)
  }, [setTarget])

  const close = useCallback(() => {
    isOpen.current = false
    setTarget(null)
    const el = opener.current
    opener.current = null
    if (el?.isConnected) el.focus()
  }, [setTarget])

  const value = useMemo(() => ({ workspaceId, target, open, close }), [workspaceId, target, open, close])
  return <ArtifactViewerContext.Provider value={value}>
    <ArtifactCardsAvailableContext.Provider value={available}>{children}</ArtifactCardsAvailableContext.Provider>
  </ArtifactViewerContext.Provider>
}

/** null outside the provider, which means the feature is off. */
export function useArtifactViewer(): ArtifactViewerValue | null {
  return useContext(ArtifactViewerContext)
}

/** True only inside the provider and when the server reports artifact projects available. */
export function useArtifactToolCardsEnabled(): boolean {
  return useContext(ArtifactCardsAvailableContext)
}
