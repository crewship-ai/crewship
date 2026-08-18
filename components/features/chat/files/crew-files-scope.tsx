"use client"

/**
 * The crew scope of the right rail's Files tab.
 *
 * Two things it replaces:
 *
 *  · A placeholder. The tab used to render "Shared crew files (loaded on
 *    demand)" under a Crew heading and then never load anything on any
 *    demand. The scope existed as a promise and nothing kept it.
 *  · A lie. The one implementation that did fetch (the deleted
 *    files/three-tier-files.tsx) swallowed a failed response into an empty
 *    array — `.catch(() => setCrewFiles([]))` — which drew as "No shared crew
 *    files". A 502 and an empty crew are not the same sentence, and the tab
 *    was saying the wrong one.
 *
 * Four states, because there are four things that can be true:
 *
 *   loading    the request is out
 *   error      it failed; says so, names it, offers a retry (ScopeFailure)
 *   no crew    the agent is in no crew, so there is nothing to ask — an
 *              agent's files live under its crew's storage root
 *   empty      the crew answered, and it has nothing shared
 *
 * On demand for real: this component is a CHILD of the collapsed Crew
 * `ScopeSection`, which mounts its children only when open, so nothing is
 * fetched until someone opens the scope.
 *
 * A folder here opens onto its children, or it does not open. The top level of
 * `<crewId>/` is mostly directories — one per agent slug — and this scope first
 * shipped with `loadingDirs={new Set()}` and a toggle that only wrote to
 * `expanded`: nothing fetched, so the chevron turned and zero rows appeared,
 * with no spinner and no error, permanently. That is the same lie as the empty
 * crew, one level down. The watcher below is the agent scope's, narrowed to one
 * listing route.
 *
 * Endpoints
 *   GET /api/v1/agents/{agentId}                 → crew_id (router_crews.go:312)
 *   GET /api/v1/crews/{crewId}/files             → the shared listing
 *   GET /api/v1/crews/{crewId}/files?subdir=…    → one directory's children
 */

import { useCallback, useEffect, useRef, useState } from "react"
import { FolderOpen, Users } from "lucide-react"
import { toast } from "sonner"

import { Spinner } from "@/components/ui/spinner"
import { apiFetch } from "@/lib/api-fetch"

import {
  ChatTreeRow,
  type FileEntry,
  type TreeNode,
  buildTopLevelTree,
  findTreeNode,
  insertTreeChildren,
} from "../chat-tree-row"
import { ScopeFailure, httpError, scopeErrorMessage, useRetry } from "../scope-fetch"

interface CrewFilesScopeProps {
  agentId: string
  workspaceId: string | null
  /** Currently open file (path), for the selection highlight. */
  selectedFile?: string | null
  /** The crew id rides along: the caller must read and write in the crew's tree. */
  onFileClick: (node: TreeNode, crewId: string) => void
}

type Status = "loading" | "ready" | "error"

export function CrewFilesScope({
  agentId,
  workspaceId,
  selectedFile,
  onFileClick,
}: CrewFilesScopeProps) {
  const { nonce, retry } = useRetry()
  const [status, setStatus] = useState<Status>("loading")
  const [crewId, setCrewId] = useState<string | null>(null)
  const [tree, setTree] = useState<TreeNode[]>([])
  const [error, setError] = useState("")
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [loadingDirs, setLoadingDirs] = useState<Set<string>>(new Set())
  // Directories already asked for, so the watcher below fires once per
  // directory rather than once per render.
  const fetchedDirsRef = useRef<Set<string>>(new Set())
  // Bumped whenever the listing is (re)fetched, so a directory response that
  // lands after a retry cannot graft its children onto the new tree.
  const listingGenRef = useRef(0)

  useEffect(() => {
    if (!workspaceId) {
      setStatus("loading")
      return
    }
    const ac = new AbortController()
    setStatus("loading")
    // A fresh listing invalidates every expansion taken against the old one.
    listingGenRef.current += 1
    setExpanded(new Set())
    setLoadingDirs(new Set())
    fetchedDirsRef.current = new Set()
    const ws = encodeURIComponent(workspaceId)
    ;(async () => {
      // The panel is handed an agent id, not a crew id — the crew comes from
      // the agent, and a failure to resolve it is a failure of this scope,
      // not an empty crew.
      const ar = await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}?workspace_id=${ws}`, {
        signal: ac.signal,
      })
      if (!ar.ok) throw httpError(ar.status)
      const agent: { crew_id?: string | null } = await ar.json()
      const crew = agent?.crew_id ?? null
      if (ac.signal.aborted) return
      setCrewId(crew)
      if (!crew) {
        setTree([])
        setStatus("ready")
        return
      }
      const fr = await apiFetch(
        `/api/v1/crews/${encodeURIComponent(crew)}/files?workspace_id=${ws}`,
        { signal: ac.signal },
      )
      if (!fr.ok) throw httpError(fr.status)
      const data: FileEntry[] | null = await fr.json()
      if (ac.signal.aborted) return
      setTree(buildTopLevelTree(data ?? []))
      setStatus("ready")
    })().catch((e: unknown) => {
      if (ac.signal.aborted || (e instanceof Error && e.name === "AbortError")) return
      setError(scopeErrorMessage(e))
      setStatus("error")
    })
    return () => ac.abort()
  }, [agentId, workspaceId, nonce])

  // Fetch children for any expanded directory that has none yet. Written as a
  // watcher over `expanded` rather than as work inside the toggle, so it is the
  // same shape as the agent scope's — one place that turns "this is open" into
  // "this is loaded", whoever opened it.
  useEffect(() => {
    if (!workspaceId || !crewId || status !== "ready") return
    const gen = listingGenRef.current
    const ws = encodeURIComponent(workspaceId)
    const prefix = `${crewId}/`
    for (const path of expanded) {
      if (loadingDirs.has(path) || fetchedDirsRef.current.has(path)) continue
      const node = tree.reduce<TreeNode | undefined>(
        (found, n) => found ?? findTreeNode(n, path),
        undefined,
      )
      if (!node || node.childrenLoaded) continue
      fetchedDirsRef.current.add(path)
      // The listing route reads `subdir`, relative to the crew root; the keys
      // it returns are absolute (`<crewId>/…`), so strip the prefix back off.
      const subdir = path.startsWith(prefix) ? path.slice(prefix.length) : path
      setLoadingDirs((p) => new Set(p).add(path))
      apiFetch(
        `/api/v1/crews/${encodeURIComponent(crewId)}/files?workspace_id=${ws}&subdir=${encodeURIComponent(subdir)}`,
      )
        .then((r) => { if (!r.ok) throw httpError(r.status); return r.json() })
        .then((data: FileEntry[] | null) => {
          if (listingGenRef.current !== gen) return
          setTree((prev) => insertTreeChildren(prev, path, data ?? []))
        })
        .catch((e: unknown) => {
          if (listingGenRef.current !== gen) return
          // Leaving the row open with nothing under it would say "this folder
          // is empty", which is exactly the claim this scope must never make on
          // a failed fetch. Close it, name the failure, and let the next click
          // re-ask.
          fetchedDirsRef.current.delete(path)
          setExpanded((p) => { const n = new Set(p); n.delete(path); return n })
          toast.error(`Could not load ${node.name} — ${scopeErrorMessage(e)}`)
        })
        .finally(() => setLoadingDirs((p) => { const n = new Set(p); n.delete(path); return n }))
    }
  }, [tree, expanded, loadingDirs, crewId, workspaceId, status])

  const toggleFolder = useCallback((path: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }, [])

  if (status === "loading") {
    return (
      <div
        data-testid="crew-files-loading"
        role="status"
        className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-muted-foreground"
      >
        <Spinner className="h-3 w-3" />
        Loading the crew&apos;s files…
      </div>
    )
  }

  if (status === "error") {
    return (
      <ScopeFailure
        data-testid="crew-files-error"
        label="Could not load the crew's shared files."
        detail={`${error} — this is not an empty crew; the list could not be fetched.`}
        onRetry={retry}
      />
    )
  }

  if (!crewId) {
    return (
      <Hint testId="crew-files-no-crew" icon={Users}>
        This agent is in no crew, so there is no shared scope to read.
      </Hint>
    )
  }

  if (tree.length === 0) {
    return (
      <Hint testId="crew-files-empty" icon={FolderOpen}>
        The crew has nothing shared yet.
      </Hint>
    )
  }

  return (
    <div className="py-0.5">
      {tree.map((node) => (
        <ChatTreeRow
          key={node.path}
          node={node}
          depth={0}
          expanded={expanded}
          loadingDirs={loadingDirs}
          selectedFile={selectedFile ?? null}
          onToggle={toggleFolder}
          onFileClick={(clicked) => onFileClick(clicked, crewId)}
        />
      ))}
    </div>
  )
}

function Hint({
  testId,
  icon: Icon,
  children,
}: {
  testId: string
  icon: typeof Users
  children: React.ReactNode
}) {
  return (
    <div
      data-testid={testId}
      className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-muted-foreground/70"
    >
      <Icon className="h-3 w-3 shrink-0" aria-hidden />
      {children}
    </div>
  )
}
