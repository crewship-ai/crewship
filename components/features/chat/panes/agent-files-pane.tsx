"use client"

// Files — the agent's working directory, as a page.
//
// This is a WRAPPER, not a second file browser. The three-scope tree
// (agent / crew / workspace) already exists at
// components/features/chat/files/three-tier-files.tsx and the row renderer
// at components/features/chat/chat-tree-row.tsx; both are used here
// unchanged. What they do not do is fetch the agent scope or own a failure
// state — ThreeTierFiles takes `agentFiles` pre-fetched, because in the
// right drawer the chat panel had already loaded them. A pane that IS the
// centre column has no such parent, so the fetch and its three states live
// here and the tree is handed the result.
//
// Endpoint: GET /api/v1/agents/{agentId}/files?workspace_id=…
//   internal/api/router_orchestration.go:733 → ProxyHandler.AgentFiles
//   (internal/api/proxy_files.go:13). Note it returns [] — not 404 — when
//   the agent has no crew_id, because agent files live under the crew's
//   storage root. That empty array means "there is no workspace on disk",
//   which is a different sentence from "the workspace is empty", and the
//   pane says the right one.
//
// Preview reuses the chat panel's useFileEditor hook, read-only: it opens
// the file through GET /api/v1/agents/{agentId}/files/download
// (router_orchestration.go:734). No save button is wired — a folder view
// is for reading, and the editor already exists in the drawer.

import { useCallback, useEffect, useState } from "react"
import { FileText, FolderClosed, Users, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard, EmptyState } from "@/components/ui/detail"
import { Spinner } from "@/components/ui/spinner"
import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"

import type { FileEntry, TreeNode } from "../chat-tree-row"
import { ThreeTierFiles } from "../files/three-tier-files"
import { useFileEditor } from "../hooks/use-file-editor"
import { PaneError, PaneLoading, PaneShell } from "./pane-shell"

export interface AgentFilesPaneProps {
  agentId: string
  agentSlug: string
  crewId?: string | null
}

type Status = "loading" | "ready" | "error"

export function AgentFilesPane({ agentId, agentSlug, crewId }: AgentFilesPaneProps) {
  const { workspaceId } = useWorkspace()
  const [nonce, setNonce] = useState(0)
  const [status, setStatus] = useState<Status>("loading")
  const [files, setFiles] = useState<FileEntry[]>([])
  const [error, setError] = useState<string>("")

  const retry = useCallback(() => setNonce((n) => n + 1), [])

  const { editorFile, editorContent, editorLoading, openFileEditor, closeEditor } = useFileEditor({
    agentId,
    workspaceId,
  })

  useEffect(() => {
    if (!workspaceId) {
      setStatus("loading")
      return
    }
    const ac = new AbortController()
    setStatus("loading")
    apiFetch(
      `/api/v1/agents/${encodeURIComponent(agentId)}/files?workspace_id=${encodeURIComponent(workspaceId)}`,
      { signal: ac.signal },
    )
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((data: FileEntry[] | null) => {
        if (ac.signal.aborted) return
        setFiles(data ?? [])
        setStatus("ready")
      })
      .catch((e: Error) => {
        if (ac.signal.aborted || e.name === "AbortError") return
        setError(e.message)
        setStatus("error")
      })
    return () => ac.abort()
  }, [agentId, workspaceId, nonce])

  const onFileClick = useCallback(
    (node: TreeNode) => openFileEditor({ path: node.path, name: node.name }),
    [openFileEditor],
  )

  const fileCount = files.filter((f) => !f.is_dir).length

  return (
    <PaneShell
      icon={FolderClosed}
      title="Files"
      subtitle={
        status === "ready" && crewId
          ? `${fileCount} file${fileCount === 1 ? "" : "s"} in ${agentSlug}'s own scope, plus everything the crew shares`
          : `Everything ${agentSlug} can read on disk — its own scope, the crew's, the workspace's`
      }
      bare
      data-testid="files-pane"
    >
      {status === "loading" && <PaneLoading label="Loading files…" data-testid="files-pane-loading" />}

      {status === "error" && (
        <PaneError
          data-testid="files-error"
          title="Could not list this agent's files"
          detail={`GET /api/v1/agents/${agentId}/files failed — ${error}. Nothing below is missing; the list simply could not be fetched.`}
          onRetry={retry}
        />
      )}

      {/* An agent with no crew has no storage root at all — the endpoint
          answers [] by design (proxy_files.go:38). Rendering the tree here
          would show three empty scopes and imply the files were deleted. */}
      {status === "ready" && !crewId && (
        <div data-testid="files-empty-no-crew" className="py-8">
          <EmptyState
            icon={Users}
            title="No file workspace yet"
            description={`${agentSlug} is not assigned to a crew, and an agent's files live under its crew's storage root. Add it to a crew and its workspace appears here on the first run.`}
          />
        </div>
      )}

      {status === "ready" && crewId && (
        <div className="flex h-full min-h-0 flex-col">
          {files.length === 0 && (
            <div data-testid="files-empty-agent-scope" className="border-b border-hairline px-4 py-3">
              <p className="type-meta leading-relaxed text-muted-foreground">
                {agentSlug} has not written anything to its own scope yet — files appear here as it works.
                The crew scope below may still have shared files.
              </p>
            </div>
          )}
          <div className="min-h-0 flex-1 overflow-y-auto">
            <ThreeTierFiles
              crewId={crewId}
              workspaceId={workspaceId}
              agentFiles={files}
              selectedFile={editorFile?.path ?? null}
              onFileClick={onFileClick}
            />
          </div>

          {editorFile && (
            <div className="max-h-[45%] shrink-0 overflow-y-auto border-t border-hairline p-4">
              <DetailCard
                title={editorFile.name}
                icon={FileText}
                subtitle="read-only"
                action={
                  <Button variant="ghost" size="icon-xs" onClick={closeEditor} aria-label="Close preview">
                    <X className="h-3.5 w-3.5" />
                  </Button>
                }
              >
                {editorLoading ? (
                  <div className="flex items-center gap-2 text-muted-foreground">
                    <Spinner className="h-3.5 w-3.5" />
                    <span className="type-meta">Loading {editorFile.name}…</span>
                  </div>
                ) : editorContent === null ? (
                  <p className="type-meta text-muted-foreground">
                    Could not read this file. Open it from the chat drawer to edit it.
                  </p>
                ) : (
                  <pre className="type-meta max-h-80 overflow-auto whitespace-pre-wrap font-mono leading-relaxed text-foreground">
                    {editorContent}
                  </pre>
                )}
              </DetailCard>
            </div>
          )}
        </div>
      )}
    </PaneShell>
  )
}
