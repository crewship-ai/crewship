"use client"

import * as React from "react"
import Link from "next/link"
import { ArrowUpRight, ChevronDown, ChevronRight, FolderCode, Maximize2, Minimize2, X } from "lucide-react"
import { EditorState } from "@codemirror/state"
import { EditorView } from "@codemirror/view"

import { DetailCard, Pill } from "@/components/ui/detail"
import { Spinner } from "@/components/ui/spinner"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { isPreviewable } from "@/lib/file-format"
import { getChatFileIcon, getEditorLanguage } from "@/components/features/chat/chat-tree-row"
import { FilePreview } from "@/components/features/chat/files/file-preview"
import { FileEditor } from "@/components/features/files/file-editor"
import {
  buildRoutineFileTree,
  formatFileSize,
  routineFileStatus,
  type RoutineFile,
  type RoutineFileNode,
} from "@/lib/routine-files"

// routine-files-card — the files a routine runs, as a tree under
// /crew/shared/, drawn the way the agent Files panel draws its tree (same
// icons, same row), with the step that uses each file and whether it is on
// the share. A click splits the card and opens the same code editor the
// Files panel uses (CodeMirror, syntax colours, line numbers), locked
// read-only, beside the tree; on a phone the preview stacks below.
//
// The list says what the recipe declares and whether the file is there. It
// is not proof that the code does what its header comment says.

const MAX_PREVIEW_CHARS = 256 * 1024

/** Where the crew Files panel reads a shared file: the CLI's `shared/<path>`. */
export function crewFileDownloadUrl(crewId: string, workspaceId: string, path: string): string {
  return `/api/v1/crews/${encodeURIComponent(crewId)}/files/download?workspace_id=${encodeURIComponent(workspaceId)}&path=${encodeURIComponent(`shared/${path}`)}`
}

export function RoutineFilesCard({
  files,
  workspaceId,
  crewId,
  nameOf,
  derived = false,
}: {
  files: RoutineFile[]
  workspaceId: string
  /** The author crew whose share holds the files; without it there is no preview. */
  crewId?: string
  nameOf: (stepId: string) => string
  /** True when the list came from the recipe, not the server (older API). */
  derived?: boolean
}) {
  const tree = React.useMemo(() => buildRoutineFileTree(files), [files])
  const [closed, setClosed] = React.useState<ReadonlySet<string>>(() => new Set())
  const [open, setOpen] = React.useState<RoutineFile | null>(null)
  const toggle = (path: string) =>
    setClosed((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  const [expanded, setExpanded] = React.useState(false)
  if (!files.length) return null
  const missing = files.filter((f) => routineFileStatus(f) === "missing").length
  const unverified = files.filter((f) => routineFileStatus(f) === "unverified").length
  return (
    <DetailCard
      title={`Files this routine runs · ${files.length}`}
      icon={FolderCode}
      subtitle={missing ? `${missing} missing` : unverified === files.length ? "not verified" : undefined}
      action={
        <span className="hidden text-[11px] text-muted-foreground sm:inline">
          changed with <code className="font-mono">crewship crew files save</code>
          {derived ? " · presence not checked by this server" : " · what a file does comes from its header comment"}
        </span>
      }
      bare
      data-testid="routine-files-card"
    >
      <div className={cn("grid", open && !expanded && "md:grid-cols-[minmax(260px,1fr)_minmax(0,1.2fr)]")}>
        <div className={cn("px-2 py-1.5", open && expanded && "hidden")}>
          <div className="px-2 pb-1.5 pt-1 font-mono text-[11px] text-muted-foreground">/crew/shared/</div>
          {tree.map((node) => (
            <FileRow key={node.path} node={node} depth={0} closed={closed} onToggle={toggle} selected={open?.path ?? null} onOpen={setOpen} nameOf={nameOf} />
          ))}
        </div>
        {open && (
          <div
            className={cn(
              "flex flex-col overflow-hidden border-t border-hairline bg-[#1e1e1e] md:border-l md:border-t-0",
              expanded ? "h-[70vh]" : "h-[420px]",
            )}
            data-testid="routine-file-preview"
          >
            {/* The Files panel's editor header, minus Save: same colours, same buttons. */}
            <div className="flex shrink-0 items-center justify-between border-b border-[#3c3c3c] bg-[#252526] px-3 py-1.5">
              <div className="flex min-w-0 items-center gap-2">
                {getChatFileIcon(open.path.split("/").pop() ?? open.path, false)}
                <span className="truncate font-mono text-label font-medium text-[#cccccc]" title={`/crew/shared/${open.path}`}>{open.path}</span>
              </div>
              <div className="flex shrink-0 items-center gap-1">
                {crewId && (
                  <Link href={`/crews?crew=${encodeURIComponent(crewId)}`} className="inline-flex items-center gap-0.5 rounded px-2 py-0.5 text-micro text-primary hover:bg-[#3c3c3c]">
                    Open in Files <ArrowUpRight className="h-3 w-3" />
                  </Link>
                )}
                <button type="button" onClick={() => setExpanded((v) => !v)} aria-label={expanded ? "Collapse preview" : "Expand preview"} className="rounded p-1 text-[#888] hover:bg-[#3c3c3c]">
                  {expanded ? <Minimize2 className="h-3 w-3" /> : <Maximize2 className="h-3 w-3" />}
                </button>
                <button type="button" aria-label="Close preview" onClick={() => { setOpen(null); setExpanded(false) }} className="rounded p-1 text-[#888] hover:bg-[#3c3c3c]">
                  <X className="h-3 w-3" />
                </button>
              </div>
            </div>
            <FileBody key={open.path} file={open} crewId={crewId} workspaceId={workspaceId} />
          </div>
        )}
      </div>
    </DetailCard>
  )
}

function FileRow({
  node,
  depth,
  closed,
  onToggle,
  selected,
  onOpen,
  nameOf,
}: {
  node: RoutineFileNode
  depth: number
  closed: ReadonlySet<string>
  onToggle: (path: string) => void
  selected: string | null
  onOpen: (file: RoutineFile) => void
  nameOf: (stepId: string) => string
}) {
  const isOpen = node.is_dir && !closed.has(node.path)
  const file = node.file
  const usedBy = file?.step_ids.map(nameOf).join(", ")
  return (
    <>
      <button
        type="button"
        data-testid={node.is_dir ? `routine-file-dir-${node.path}` : `routine-file-${node.path}`}
        aria-expanded={node.is_dir ? isOpen : undefined}
        aria-current={!node.is_dir && selected === node.path ? "true" : undefined}
        onClick={() => (node.is_dir ? onToggle(node.path) : file && onOpen(file))}
        className={cn(
          "flex w-full items-center gap-1.5 rounded-md py-1 pr-2 pl-[var(--row-indent)] text-left text-xs transition-colors",
          !node.is_dir && selected === node.path ? "bg-primary/10 text-primary" : "text-muted-foreground hover:bg-accent/50 hover:text-foreground",
        )}
        style={{ "--row-indent": `${depth * 14 + 8}px` } as React.CSSProperties}
      >
        {node.is_dir ? (
          isOpen ? <ChevronDown className="h-3 w-3 shrink-0" /> : <ChevronRight className="h-3 w-3 shrink-0" />
        ) : (
          <span className="w-3 shrink-0" />
        )}
        {getChatFileIcon(node.name, node.is_dir, isOpen)}
        <span className="truncate font-mono text-foreground/85">{node.name}</span>
        {file && routineFileStatus(file) === "missing" && <Pill tone="destructive">Missing on the share</Pill>}
        {file && routineFileStatus(file) === "unverified" && (
          <span title="The crew share could not be checked right now — the file may well be there.">
            <Pill tone="default">Not verified</Pill>
          </span>
        )}
        {node.is_dir ? (
          <span className="ml-auto shrink-0 text-[11px] text-muted-foreground-soft">
            {node.fileCount} {node.fileCount === 1 ? "file" : "files"}
          </span>
        ) : (
          <>
            {usedBy && <span className="ml-auto hidden shrink-0 text-[11px] text-muted-foreground sm:inline">used by {usedBy}</span>}
            <span className={cn("shrink-0 font-mono text-[10px] text-muted-foreground-soft", !usedBy && "ml-auto")}>{formatFileSize(file?.size_bytes)}</span>
          </>
        )}
      </button>
      {node.is_dir && isOpen && node.children.map((child) => (
        <FileRow key={child.path} node={child} depth={depth + 1} closed={closed} onToggle={onToggle} selected={selected} onOpen={onOpen} nameOf={nameOf} />
      ))}
    </>
  )
}

function FileBody({ file, crewId, workspaceId }: { file: RoutineFile; crewId?: string; workspaceId: string }) {
  const [text, setText] = React.useState<string | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const textual = isPreviewable(file.path.split("/").pop() ?? file.path)
  const url = crewId ? crewFileDownloadUrl(crewId, workspaceId, file.path) : null
  const status = routineFileStatus(file)
  React.useEffect(() => {
    if (!url || !textual || status === "missing") return
    const controller = new AbortController()
    setText(null)
    setError(null)
    apiFetch(url, { signal: controller.signal })
      .then(async (res) => {
        if (!res.ok) throw new Error(`Could not load the file (${res.status}).`)
        const body = await res.text()
        if (!controller.signal.aborted)
          setText(body.length > MAX_PREVIEW_CHARS ? body.slice(0, MAX_PREVIEW_CHARS) + "\n… truncated" : body)
      })
      .catch((e: unknown) => {
        if (controller.signal.aborted) return
        setError(e instanceof Error ? e.message : String(e))
      })
    return () => controller.abort()
  }, [url, textual, status])
  if (status === "missing")
    return (
      <p className="p-3 text-xs text-muted-foreground">
        The recipe declares this file, but it is not on the crew share. Put it there with{" "}
        <code className="font-mono">crewship crew files save &lt;crew&gt; shared/{file.path} --file …</code>
      </p>
    )
  if (!url)
    return <p className="p-3 text-xs text-muted-foreground">No author crew is recorded for this routine, so its share cannot be read here.</p>
  if (!textual) return <FilePreview url={url} name={file.path.split("/").pop() ?? file.path} onClose={() => {}} />
  if (error)
    return (
      <p role="alert" className="p-3 text-xs text-destructive">
        {error}
      </p>
    )
  if (text == null)
    return (
      <p role="status" className="flex items-center gap-1.5 p-3 text-xs text-muted-foreground">
        <Spinner className="h-3 w-3" /> Loading preview…
      </p>
    )
  return (
    <>
      <div className="min-h-0 flex-1 overflow-hidden" data-testid="routine-file-editor">
        <FileEditor code={text} language={getEditorLanguage(file.path.split("/").pop() ?? file.path)} onSave={() => {}} extraExtensions={READ_ONLY} />
      </div>
      {/* Status bar, as the Files panel draws it — this one says read-only instead of Ctrl+S. */}
      <div className="flex shrink-0 items-center gap-3 bg-[#007acc] px-3 py-0.5 text-micro text-white">
        <span className="shrink-0 whitespace-nowrap">Read-only · {text.length.toLocaleString("en")} chars</span>
        <span className="min-w-0 flex-1 truncate text-right">{file.description}</span>
      </div>
    </>
  )
}

/** The editor buffer cannot be typed into or focused for editing. */
const READ_ONLY = [EditorState.readOnly.of(true), EditorView.editable.of(false)]
