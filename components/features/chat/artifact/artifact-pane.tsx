"use client"

import { useEffect, useMemo, useState } from "react"
import dynamic from "next/dynamic"
import { X } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { motion } from "motion/react"
import { toast } from "sonner"

import {
  Artifact,
  ArtifactBody,
  ArtifactHeader,
  ArtifactViewSwitch,
} from "@/components/ai-elements/artifact"
import { cn } from "@/lib/utils"
import { spring } from "@/lib/motion"
import { useArtifactStore } from "@/stores/artifact-store"
import { useWorkspace } from "@/hooks/use-workspace"
import { getEditorLanguage } from "../chat-tree-row"
import {
  ArtifactContentRefused,
  readArtifactFile,
  saveArtifactFile,
  type ArtifactFile,
} from "./artifact-file-io"

const FileEditor = dynamic(
  () =>
    import("@/components/features/files/file-editor").then((m) => m.FileEditor),
  {
    ssr: false,
    loading: () => (
      <div className="flex items-center justify-center h-full">
        <Spinner className="h-5 w-5 text-muted-foreground" />
      </div>
    ),
  },
)

interface ArtifactPaneProps {
  agentId: string
  width?: number
}

export function ArtifactPane({ agentId, width = 540 }: ArtifactPaneProps) {
  const { workspaceId } = useWorkspace()
  // Narrow selectors — the pane subscribed to the whole artifact store and
  // re-rendered on every unrelated store write.
  const open = useArtifactStore((s) => s.open)
  const tabs = useArtifactStore((s) => s.tabs)
  const activeId = useArtifactStore((s) => s.activeId)
  const setOpen = useArtifactStore((s) => s.setOpen)
  const setActive = useArtifactStore((s) => s.setActive)
  const closeTab = useArtifactStore((s) => s.closeTab)
  const pruneToAgent = useArtifactStore((s) => s.pruneToAgent)
  // Bytes are held together with the path they came from. Anything the pane
  // shows or writes is checked against the active tab's path, so a body read
  // for one file can never be edited — or saved — as another.
  const [loaded, setLoaded] = useState<ArtifactFile | null>(null)
  const [loading, setLoading] = useState(false)

  // Drop tabs that belong to a previously-active agent. Without this,
  // a tab opened for agent A would be loaded/saved through agent B's
  // endpoints after a context swap.
  useEffect(() => {
    pruneToAgent(agentId)
  }, [agentId, pruneToAgent])

  // Filter to the current agent before picking the active tab —
  // belt-and-suspenders against a race where pruneToAgent hasn't run yet.
  const active = useMemo(
    () => tabs.find((t) => t.id === activeId && t.agentId === agentId) ?? null,
    [tabs, activeId, agentId],
  )

  // File bytes come from the download route via readArtifactFile, which
  // refuses any response that isn't a file stream. The pane used to GET the
  // *listing* route with a `path` query the server ignores, so it rendered a
  // JSON directory listing as the file — and would save it back over the file.
  useEffect(() => {
    if (!active || !workspaceId) {
      setLoaded(null)
      setLoading(false)
      return
    }
    const ac = new AbortController()
    setLoading(true)
    // Drop the previous tab's bytes immediately: nothing may be displayed
    // under a header/path it did not come from, not even for one frame.
    setLoaded(null)
    readArtifactFile({
      agentId,
      workspaceId,
      path: active.path,
      signal: ac.signal,
    })
      .then((file) => {
        if (!ac.signal.aborted) setLoaded(file)
      })
      .catch((err) => {
        if (!ac.signal.aborted) {
          // Distinct from "" (an actually empty file) so a save can't
          // overwrite a real file with empty contents on a load miss.
          setLoaded(null)
          toast.error(
            err instanceof ArtifactContentRefused
              ? "Refused to open artifact: the server did not return the file"
              : "Failed to load artifact",
          )
        }
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false)
      })
    return () => ac.abort()
  }, [active, agentId, workspaceId])

  const handleSave = async (next: string) => {
    if (!active || !workspaceId) return
    // The mirror of the read guard: only a path whose bytes this pane
    // actually received may be written. Without it a save could target the
    // active tab while the editor still held another file's (or a refused
    // response's) contents.
    if (!loaded || loaded.path !== active.path) {
      toast.error("Refused to save: this artifact was never loaded")
      return
    }
    try {
      await saveArtifactFile({
        agentId,
        workspaceId,
        path: loaded.path,
        text: next,
      })
      // Replace the in-memory baseline with what we just wrote so a
      // remount (mode toggle, tab switch back) doesn't show pre-save
      // text and a Cancel doesn't revert through stale state.
      setLoaded({ path: loaded.path, text: next })
      toast.success(`${active.title} saved`)
    } catch {
      toast.error("Failed to save file")
    }
  }

  return (
    <Artifact open={open} onOpenChange={setOpen} width={width}>
      <ArtifactHeader
        title={active?.title ?? "Artifact"}
        subtitle={active?.path}
      >
        {/* Diff/Preview are not implemented yet — offer only the editor
              view so the switcher (which hides itself below two views)
              never exposes a "coming soon" dead end. */}
          <ArtifactViewSwitch views={["editor"]} />
      </ArtifactHeader>
      {tabs.length > 0 && (
        <div className="flex items-center gap-1 overflow-x-auto border-b px-2 py-1 shrink-0 bg-muted/20">
          {tabs.map((t) => {
            const isActive = t.id === activeId
            return (
              <motion.div
                key={t.id}
                layout
                transition={spring.snappy}
                className={cn(
                  "group/tab inline-flex items-center gap-1.5 rounded px-2 py-1 text-xs whitespace-nowrap transition-colors",
                  isActive
                    ? "bg-background border text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                <button
                  type="button"
                  onClick={() => setActive(t.id)}
                  className="truncate max-w-[140px] text-left"
                >
                  {t.title}
                </button>
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation()
                    closeTab(t.id)
                  }}
                  aria-label={`Close ${t.title}`}
                  className="h-4 w-4 inline-flex items-center justify-center rounded opacity-0 group-hover/tab:opacity-100 focus-visible:opacity-100 hover:bg-muted/60 transition-opacity"
                >
                  <X className="h-3 w-3" />
                </button>
              </motion.div>
            )
          })}
        </div>
      )}
      <ArtifactBody
        editor={
          loading ? (
            <div className="flex items-center justify-center h-full">
              <Spinner className="h-5 w-5 text-muted-foreground" />
            </div>
          ) : active && loaded && loaded.path === active.path ? (
            <FileEditor
              key={active.id}
              code={loaded.text}
              language={active.language ?? getEditorLanguage(active.title)}
              onSave={handleSave}
            />
          ) : (
            <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
              No artifact open
            </div>
          )
        }
      />
    </Artifact>
  )
}
