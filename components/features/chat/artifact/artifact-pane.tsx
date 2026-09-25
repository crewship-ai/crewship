"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import dynamic from "next/dynamic"
import { motion } from "motion/react"
import { ArrowLeft, Download, Eye, FileCode2, FileSpreadsheet, FileText, Maximize2, Minimize2, Pause, Play, X } from "lucide-react"
import { marked } from "marked"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { isManagerTier } from "@/lib/permissions/tiers"
import { useArtifactStore } from "@/stores/artifact-store"
import { useWorkspace } from "@/hooks/use-workspace"
import { getEditorLanguage } from "../chat-tree-row"
import { FilePreview } from "../files/file-preview"
import { ArtifactContentRefused, artifactDownloadUrl, readArtifactSnapshot, saveArtifactFile, type ArtifactSnapshot } from "./artifact-file-io"

const FileEditor = dynamic(
  () => import("@/components/features/files/file-editor").then((m) => m.FileEditor),
  { ssr: false, loading: () => <div className="flex h-full items-center justify-center"><Spinner className="size-5" /></div> },
)

const BINARY = /\.(pdf|png|jpe?g|webp)$/i
const WORKBOOK = /\.xlsx?$/i
const PREVIEW = /\.(html?|csv|tsv|md)$/i
const INTERVAL = 5000

function sameBytes(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false
  return true
}

function CsvPreview({ text, separator }: { text: string; separator: string }) {
  const rows = useMemo(() => {
    const result: string[][] = []
    let row: string[] = [], cell = "", quoted = false
    for (let i = 0; i < text.length && result.length < 100; i++) {
      const c = text[i]
      if (c === '"') {
        if (quoted && text[i + 1] === '"') { cell += '"'; i++ }
        else quoted = !quoted
      } else if (c === separator && !quoted) { row.push(cell); cell = "" }
      else if ((c === "\n" || c === "\r") && !quoted) {
        if (c === "\r" && text[i + 1] === "\n") i++
        row.push(cell); result.push(row); row = []; cell = ""
      } else cell += c
    }
    if (row.length || cell) { row.push(cell); result.push(row) }
    return result
  }, [text, separator])
  return <div className="h-full overflow-auto p-3" aria-label="Spreadsheet preview">
    <table className="w-full border-collapse text-left text-xs"><tbody>
      {rows.map((row, i) => <tr key={i} className="border-b border-border/50">
        <th scope="row" className="sticky left-0 bg-background px-2 py-1 font-normal text-muted-foreground">{i + 1}</th>
        {row.slice(0, 30).map((cell, j) => <td key={j} className="min-w-24 max-w-72 truncate border-l border-border/50 px-2 py-1" title={cell}>{cell}</td>)}
      </tr>)}
    </tbody></table>
    {rows.length === 100 && <p className="py-3 text-xs text-muted-foreground">Showing the first 100 rows.</p>}
  </div>
}

function ArtifactPreview({ path, snapshot, url, revision, onClose }: { path: string; snapshot: ArtifactSnapshot; url: string; revision: number; onClose: () => void }) {
  const text = useMemo(() => new TextDecoder().decode(snapshot.bytes), [snapshot])
  if (BINARY.test(path)) {
    // Existing viewer validates signatures and renders PDFs with pdfjs.
    return <FilePreview key={revision} url={`${url}&revision=${revision}`} name={path.split("/").pop() ?? path} onClose={onClose} showHeader={false} />
  }
  if (/\.md$/i.test(path)) {
    // The opaque, script-free frame keeps agent-authored markup out of the app DOM.
    const csp = '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; img-src data:; style-src \'unsafe-inline\'; font-src data:; form-action \'none\'; base-uri \'none\'">'
    const style = '<style>body{box-sizing:border-box;max-width:800px;min-height:100%;margin:0 auto;padding:40px 48px;background:#fff;color:#242932;font:15px/1.6 system-ui,sans-serif}h1,h2,h3{line-height:1.25}h1{font-size:28px}h2{font-size:20px;margin-top:30px}table{border-collapse:collapse;width:100%}th,td{border:1px solid #d9dfe6;padding:7px;text-align:left}blockquote{border-left:3px solid #4c9aff;padding-left:12px;color:#526070}code{background:#f1f3f6;padding:2px 4px}a{color:#276ac2}</style>'
    return <iframe title={`Preview ${path}`} sandbox="" referrerPolicy="no-referrer" className="h-full w-full bg-[#242629]" srcDoc={csp + style + marked.parse(text, { async: false })} />
  }
  if (/\.html?$/i.test(path)) {
    // Agent HTML has an opaque origin, no scripts and no network access.
    const csp = '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; img-src data:; style-src \'unsafe-inline\'; font-src data:; form-action \'none\'; base-uri \'none\'">'
    return <iframe title={`Preview ${path}`} sandbox="" referrerPolicy="no-referrer" className="h-full w-full bg-white" srcDoc={csp + text} />
  }
  if (/\.(csv|tsv)$/i.test(path)) return <CsvPreview text={text} separator={/\.tsv$/i.test(path) ? "\t" : ","} />
  return <pre className="h-full overflow-auto whitespace-pre-wrap break-words p-4 font-mono text-xs">{text}</pre>
}

/** Inline workspace: its parent places this beside the transcript. */
export function ArtifactPane({ agentId, width = 540, expanded = false }: { agentId: string; width?: number; expanded?: boolean }) {
  const { workspaceId, role } = useWorkspace()
  const canEdit = isManagerTier(role)
  const open = useArtifactStore((s) => s.open)
  const tabs = useArtifactStore((s) => s.tabs)
  const activeId = useArtifactStore((s) => s.activeId)
  const setOpen = useArtifactStore((s) => s.setOpen)
  const setFocus = useArtifactStore((s) => s.setFocus)
  const setActive = useArtifactStore((s) => s.setActive)
  const closeTab = useArtifactStore((s) => s.closeTab)
  const pruneToAgent = useArtifactStore((s) => s.pruneToAgent)
  const active = useMemo(() => tabs.find((t) => t.id === activeId && t.agentId === agentId) ?? null, [tabs, activeId, agentId])
  const path = active?.path ?? ""
  const [snapshot, setSnapshot] = useState<ArtifactSnapshot | null>(null)
  const [revision, setRevision] = useState(0)
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null)
  const [following, setFollowing] = useState(true)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")
  const [view, setView] = useState<"preview" | "editor">("preview")
  const [dirty, setDirty] = useState(false)
  const previousRef = useRef<ArtifactSnapshot | null>(null)

  useEffect(() => { pruneToAgent(agentId) }, [agentId, pruneToAgent])
  useEffect(() => {
    setSnapshot(null); setRevision(0); setUpdatedAt(null)
    previousRef.current = null
    setFollowing(true); setDirty(false); setError("")
    setView(BINARY.test(path) || WORKBOOK.test(path) || PREVIEW.test(path) ? "preview" : "editor")
  }, [agentId, path])

  useEffect(() => {
    // A Pause transition re-runs this effect. Do not perform its initial
    // fetch on that transition; cleanup below already aborts the prior read.
    if (!open || !following || !path || !workspaceId) return
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    let first = true
    const read = async () => {
      if (document.visibilityState === "hidden") { timer = setTimeout(read, INTERVAL); return }
      if (first) setLoading(true)
      try {
        const next = await readArtifactSnapshot({ agentId, workspaceId, path, signal: controller.signal })
        if (controller.signal.aborted) return
        if (!previousRef.current || previousRef.current.path !== next.path || !sameBytes(previousRef.current.bytes, next.bytes)) {
          previousRef.current = next
          setSnapshot(next)
          setRevision((n) => n + 1)
          setUpdatedAt(new Date())
        }
        setError("")
      } catch (cause) {
        if (!controller.signal.aborted) setError(cause instanceof ArtifactContentRefused ? cause.message : "Could not refresh this artifact")
      } finally {
        if (!controller.signal.aborted) {
          setLoading(false); first = false
          if (following) timer = setTimeout(read, INTERVAL)
        }
      }
    }
    void read()
    return () => { controller.abort(); if (timer) clearTimeout(timer) }
  }, [open, path, agentId, workspaceId, following])

  if (!open) return null
  const binary = BINARY.test(path) || WORKBOOK.test(path)
  const loaded = snapshot?.path === path ? snapshot : null
  const text = loaded && !binary ? new TextDecoder().decode(loaded.bytes) : null
  const url = active && workspaceId ? artifactDownloadUrl(agentId, workspaceId, path) : ""
  const ArtifactIcon = /\.(csv|tsv|xlsx?)$/i.test(path) ? FileSpreadsheet : /\.html?$/i.test(path) ? FileCode2 : FileText
  const handleSave = async (next: string) => {
    if (!canEdit || !active || !workspaceId || !loaded || loaded.path !== active.path) {
      toast.error("Refused to save: no editable artifact is loaded"); return
    }
    try {
      await saveArtifactFile({ agentId, workspaceId, path: loaded.path, text: next })
      const saved = { path: loaded.path, bytes: new TextEncoder().encode(next) }
      previousRef.current = saved
      setSnapshot(saved)
      setRevision((n) => n + 1); setUpdatedAt(new Date()); setDirty(false)
      toast.success(`${active.title} saved`)
    } catch { toast.error("Failed to save file") }
  }

  return <motion.aside
    initial={{ x: 32, opacity: 0 }} animate={{ x: 0, opacity: 1 }} transition={{ duration: 0.2 }}
    aria-label="Live artifact"
    className={cn("flex h-full min-h-0 min-w-0 flex-col border-l border-border bg-background max-md:absolute max-md:inset-0 max-md:z-30 max-md:!w-full", expanded ? "flex-1" : "shrink-0")}
    style={expanded ? { width: 0 } : { width: `min(${width}px, 42vw)` }}
  >
    <header className="flex min-h-14 shrink-0 items-center gap-2 border-b border-border bg-card px-3 py-2">
      <Button variant="ghost" size="icon-sm" aria-label="Back to artifacts" onClick={() => setOpen(false)}><ArrowLeft className="size-4" /></Button>
      <span className="flex size-8 shrink-0 items-center justify-center rounded-md border border-border bg-muted/40"><ArtifactIcon className="size-4 text-primary" /></span>
      <div className="min-w-0 flex-1"><p className="truncate text-sm font-semibold" title={active?.title}>{active?.title ?? "Artifact"}</p><p className="truncate text-[11px] text-muted-foreground" title={path}>{path}</p></div>
      <Button variant="outline" size="sm" aria-label={expanded ? "Show chat alongside" : "Expand artifact"} onClick={() => setFocus(!expanded)}>{expanded ? <Minimize2 className="size-3.5" /> : <Maximize2 className="size-3.5" />}<span className="hidden lg:inline">{expanded ? "Chat" : "Expand"}</span></Button>
      {loaded && <Button variant="outline" size="sm" asChild><a href={url} download={active?.title} aria-label="Download artifact"><Download className="size-3.5" /> Download</a></Button>}
    </header>
    {tabs.length > 1 && <div className="flex shrink-0 gap-1 overflow-x-auto border-b px-2 py-1">
      {tabs.filter((t) => t.agentId === agentId).map((tab) => <div key={tab.id} className={cn("flex items-center rounded text-xs", activeId === tab.id ? "bg-muted text-foreground" : "text-muted-foreground")}>
        <button type="button" className="max-w-32 truncate px-2 py-1" onClick={() => setActive(tab.id)}>{tab.title}</button>
        <button type="button" className="px-1" aria-label={`Close ${tab.title}`} onClick={() => closeTab(tab.id)}><X className="size-3" /></button>
      </div>)}
    </div>}
    <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card/70 px-3 py-1.5 text-xs">
      <span className={cn("size-1.5 rounded-full", following ? "bg-success" : "bg-warn")} aria-hidden="true" />
      <span role="status" className="min-w-0 flex-1 truncate text-muted-foreground">{dirty ? "Editing · live updates paused" : following ? `Live · revision ${revision || "…"}` : `Paused · revision ${revision || "…"}`}{updatedAt && ` · updated ${updatedAt.toLocaleTimeString()}`}</span>
      <Button variant="ghost" size="sm" className="h-7 gap-1" onClick={() => setFollowing((v) => !v)} disabled={dirty} aria-label={following ? "Pause live updates" : "Follow live updates"}>{following ? <Pause className="size-3" /> : <Play className="size-3" />}{following ? "Pause" : "Follow"}</Button>
    </div>
    {text !== null && <div className="flex shrink-0 gap-1 border-b border-border bg-card/70 px-3 py-1">
      <Button variant={view === "preview" ? "secondary" : "ghost"} size="sm" className="h-7 gap-1" onClick={() => {
        if (dirty && !window.confirm("Discard unsaved artifact changes?")) return
        setDirty(false); setView("preview")
      }}><Eye className="size-3" />Preview</Button>
      {canEdit && <Button variant={view === "editor" ? "secondary" : "ghost"} size="sm" className="h-7 gap-1" onClick={() => { setView("editor"); setFollowing(false) }}><FileCode2 className="size-3" />Editor</Button>}
    </div>}
    {error && <div role="alert" className="border-b px-3 py-2 text-xs text-destructive">{error}</div>}
    <div className="relative min-h-0 flex-1 overflow-hidden">
      {loading && !loaded && <div className="flex h-full items-center justify-center"><Spinner className="size-5" /></div>}
      {!loading && !loaded && !error && <p className="p-4 text-sm text-muted-foreground">No artifact open</p>}
      {loaded && WORKBOOK.test(path) && <div className="p-4 text-sm text-muted-foreground">Spreadsheet preview supports CSV and TSV. <a href={url} download className="text-primary underline">Download this workbook</a> to open it.</div>}
      {loaded && !WORKBOOK.test(path) && (view === "preview" || binary) && <ArtifactPreview path={path} snapshot={loaded} url={url} revision={revision} onClose={() => setOpen(false)} />}
      {loaded && text !== null && view === "editor" && <FileEditor key={`${activeId}:${revision}`} code={text} language={active?.language ?? getEditorLanguage(active?.title ?? path)} onSave={handleSave} onDirtyChange={(v) => { setDirty(v); if (v) setFollowing(false) }} />}
    </div>
  </motion.aside>
}
