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
import { apiFetch } from "@/lib/api-fetch"
import { getEditorLanguage } from "../chat-tree-row"

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
  const { open, tabs, activeId, setOpen, setActive, closeTab, pruneToAgent } = useArtifactStore()
  const [content, setContent] = useState<string | null>(null)
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

  useEffect(() => {
    if (!active || !workspaceId) {
      setContent(null)
      setLoading(false)
      return
    }
    const ac = new AbortController()
    setLoading(true)
    apiFetch(
      `/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}&path=${encodeURIComponent(active.path)}`,
      { signal: ac.signal },
    )
      .then(async (r) => {
        if (!r.ok) throw new Error(`Failed to load: HTTP ${r.status}`)
        return r.text()
      })
      .then((data) => setContent(data))
      .catch(() => {
        if (!ac.signal.aborted) {
          // Distinct from "" (an actually empty file) so a save can't
          // overwrite a real file with empty contents on a load miss.
          setContent(null)
          toast.error("Failed to load artifact")
        }
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false)
      })
    return () => ac.abort()
  }, [active, agentId, workspaceId])

  const handleSave = async (next: string) => {
    if (!active || !workspaceId) return
    try {
      const res = await apiFetch(
        `/api/v1/agents/${agentId}/files/save?path=${encodeURIComponent(active.path)}&workspace_id=${workspaceId}`,
        {
          method: "PUT",
          headers: { "Content-Type": "text/plain" },
          body: next,
        },
      )
      if (!res.ok) throw new Error("Save failed")
      // Replace the in-memory baseline with what we just wrote so a
      // remount (mode toggle, tab switch back) doesn't show pre-save
      // text and a Cancel doesn't revert through stale state.
      setContent(next)
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
        <ArtifactViewSwitch />
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
          ) : active && content !== null ? (
            <FileEditor
              code={content}
              language={active.language ?? getEditorLanguage(active.title)}
              onSave={handleSave}
            />
          ) : (
            <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
              No artifact open
            </div>
          )
        }
        diff={
          <div className="flex items-center justify-center h-full p-8 text-sm text-muted-foreground text-center">
            Diff view coming soon — will show before/after when an agent edits
            a tracked file.
          </div>
        }
        preview={
          <div className="flex items-center justify-center h-full p-8 text-sm text-muted-foreground text-center">
            Preview view coming soon — markdown render, image preview, sandbox
            output.
          </div>
        }
      />
    </Artifact>
  )
}
