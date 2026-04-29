"use client"

import { useEffect, useMemo, useState } from "react"
import dynamic from "next/dynamic"
import { Loader2, X } from "lucide-react"
import { motion } from "motion/react"
import { toast } from "sonner"

import {
  Artifact,
  ArtifactBody,
  ArtifactHeader,
  ArtifactViewSwitch,
} from "@/components/ai-elements/artifact"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { spring } from "@/lib/motion"
import { useArtifactStore } from "@/stores/artifact-store"
import { useWorkspace } from "@/hooks/use-workspace"
import { getEditorLanguage } from "../chat-tree-row"

const FileEditor = dynamic(
  () =>
    import("@/components/features/files/file-editor").then((m) => m.FileEditor),
  {
    ssr: false,
    loading: () => (
      <div className="flex items-center justify-center h-full">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
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
  const { open, tabs, activeId, setOpen, setActive, closeTab } = useArtifactStore()
  const [content, setContent] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const active = useMemo(
    () => tabs.find((t) => t.id === activeId) ?? null,
    [tabs, activeId],
  )

  useEffect(() => {
    if (!active || !workspaceId) {
      setContent(null)
      return
    }
    const ac = new AbortController()
    setLoading(true)
    fetch(
      `/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}&path=${encodeURIComponent(active.path)}`,
      { signal: ac.signal },
    )
      .then((r) => (r.ok ? r.text() : null))
      .then((data) => setContent(data ?? ""))
      .catch(() => {
        if (!ac.signal.aborted) setContent("")
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false)
      })
    return () => ac.abort()
  }, [active, agentId, workspaceId])

  const handleSave = async (next: string) => {
    if (!active || !workspaceId) return
    try {
      const res = await fetch(
        `/api/v1/agents/${agentId}/files/save?path=${encodeURIComponent(active.path)}&workspace_id=${workspaceId}`,
        {
          method: "PUT",
          headers: { "Content-Type": "text/plain" },
          body: next,
        },
      )
      if (!res.ok) throw new Error("Save failed")
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
              <motion.button
                key={t.id}
                type="button"
                layout
                transition={spring.snappy}
                onClick={() => setActive(t.id)}
                className={cn(
                  "group/tab inline-flex items-center gap-1.5 rounded px-2 py-1 text-xs whitespace-nowrap transition-colors",
                  isActive
                    ? "bg-background border text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                <span className="truncate max-w-[140px]">{t.title}</span>
                <Button
                  asChild
                  size="icon-sm"
                  variant="ghost"
                  className="h-4 w-4 opacity-0 group-hover/tab:opacity-100"
                  aria-label={`Close ${t.title}`}
                >
                  <span
                    role="button"
                    tabIndex={-1}
                    onClick={(e) => {
                      e.stopPropagation()
                      closeTab(t.id)
                    }}
                  >
                    <X className="h-3 w-3" />
                  </span>
                </Button>
              </motion.button>
            )
          })}
        </div>
      )}
      <ArtifactBody
        editor={
          loading ? (
            <div className="flex items-center justify-center h-full">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
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
