"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import type { FileEntry, TreeNode } from "@/lib/types/agent"

function sortNodes(nodes: TreeNode[]) {
  nodes.sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1
    return a.name.localeCompare(b.name)
  })
}

function buildTopLevel(files: FileEntry[]): TreeNode[] {
  const roots: TreeNode[] = files.map((f) => ({
    ...f,
    children: [],
    childrenLoaded: !f.is_dir,
  }))
  sortNodes(roots)
  return roots
}

function mergeTopLevel(existing: TreeNode[], fresh: FileEntry[]): TreeNode[] {
  const oldByPath = new Map(existing.map((n) => [n.path, n]))
  const merged = fresh.map((f) => {
    const prev = oldByPath.get(f.path)
    if (prev && prev.is_dir && prev.childrenLoaded) {
      return { ...prev, size: f.size, mod_time: f.mod_time }
    }
    return { ...f, children: [], childrenLoaded: !f.is_dir }
  })
  sortNodes(merged)
  return merged
}

function insertChildren(tree: TreeNode[], parentPath: string, children: FileEntry[]): TreeNode[] {
  return tree.map((node) => {
    if (node.path === parentPath) {
      const newChildren = children.map((f) => ({
        ...f,
        children: [],
        childrenLoaded: !f.is_dir,
      }))
      sortNodes(newChildren)
      return { ...node, children: newChildren, childrenLoaded: true }
    }
    if (node.is_dir && node.children.length > 0) {
      return { ...node, children: insertChildren(node.children, parentPath, children) }
    }
    return node
  })
}

export function findNode(nodes: TreeNode[], path: string): TreeNode | undefined {
  for (const n of nodes) {
    if (n.path === path) return n
    if (n.is_dir && n.children.length > 0) {
      const found = findNode(n.children, path)
      if (found) return found
    }
  }
  return undefined
}

export interface UseTreeStateArgs {
  agentId: string | null | undefined
  workspaceId: string | null | undefined
  wsLoading: boolean
}

export interface UseTreeStateResult {
  tree: TreeNode[]
  basePrefix: string
  loading: boolean
  error: string | null
  selectedPath: string | null
  setSelectedPath: (path: string | null) => void
  expandedPaths: Set<string>
  loadingDirs: Set<string>
  toggleFolder: (path: string) => void
  refresh: () => void
}

export function useTreeState({ agentId, workspaceId, wsLoading }: UseTreeStateArgs): UseTreeStateResult {
  const [tree, setTree] = useState<TreeNode[]>([])
  const [basePrefix, setBasePrefix] = useState("")
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set())
  const [loadingDirs, setLoadingDirs] = useState<Set<string>>(new Set())

  const abortRef = useRef<AbortController | null>(null)
  const fetchRef = useRef<(() => void) | null>(null)

  const canQueryAgent = Boolean(workspaceId && agentId)

  useEffect(() => {
    if (wsLoading) return
    if (!workspaceId) { setLoading(false); setError("No workspace selected"); return }
    // Flush the previous agent's tree before deciding whether to fetch.
    // If we leave state in place while agentId is unresolved, the panel
    // flashes the last agent's files until the new fetch resolves —
    // confusing when the two agents have very different workspaces.
    abortRef.current?.abort()
    const ac = new AbortController()
    abortRef.current = ac
    // Reset the full tree-view state — not just `tree`/`selectedPath` —
    // so switching agent/workspace doesn't leak `expandedPaths` /
    // `loadingDirs` from the previous agent or briefly render the
    // empty-state UI (loading=false, tree=[]) before the new fetch
    // resolves.
    setLoading(true)
    setTree([])
    setBasePrefix("")
    setSelectedPath(null)
    setExpandedPaths(new Set())
    setLoadingDirs(new Set())
    setError(null)
    // Without a resolved agentId the legacy pathname would be
    // `/api/v1/agents//files`, which 404s. Short-circuit while the
    // AgentDetailProvider is still resolving.
    if (!agentId) { setLoading(false); return }
    let isFirstLoad = true
    async function fetchFiles() {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}`, { signal: ac.signal })
        if (!res.ok) { if (!ac.signal.aborted) setError("Failed to load files"); return }
        const data: FileEntry[] | null = await res.json()
        if (ac.signal.aborted) return
        const safeData = data ?? []
        if (isFirstLoad) {
          setTree(buildTopLevel(safeData))
          isFirstLoad = false
        } else {
          setTree((prev) => mergeTopLevel(prev, safeData))
        }
        if (safeData.length > 0) {
          const first = safeData[0]
          const idx = first.path.lastIndexOf(first.name)
          setBasePrefix(idx > 0 ? first.path.slice(0, idx) : "")
        }
        setError(null)
      } catch (err) {
        if (ac.signal.aborted) return
        if (err instanceof DOMException && err.name === "AbortError") return
        setError("Network error. Is the engine running?")
      }
      finally { if (!ac.signal.aborted) setLoading(false) }
    }
    fetchRef.current = fetchFiles
    fetchFiles()
    const interval = setInterval(fetchFiles, 120000)
    return () => { ac.abort(); clearInterval(interval); fetchRef.current = null }
  }, [agentId, workspaceId, wsLoading])

  useEffect(() => {
    return () => { abortRef.current?.abort() }
  }, [])

  const fetchSubdir = useCallback(async (dirPath: string) => {
    if (!canQueryAgent) return
    // Tie subdir loads to the same AbortController as the top-level
    // fetch so a slow folder request from the previous agent/workspace
    // can't repopulate the tree after a reset has cleared it.
    const signal = abortRef.current?.signal
    setLoadingDirs((prev) => new Set(prev).add(dirPath))
    try {
      const relPath = dirPath.startsWith(basePrefix) ? dirPath.slice(basePrefix.length) : dirPath
      const res = await fetch(
        `/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}&subdir=${encodeURIComponent(relPath)}`,
        signal ? { signal } : undefined,
      )
      if (!res.ok) return
      const data: FileEntry[] | null = await res.json()
      if (signal?.aborted) return
      setTree((prev) => insertChildren(prev, dirPath, data ?? []))
    } catch (err) {
      if (signal?.aborted) return
      if (err instanceof DOMException && err.name === "AbortError") return
      /* folder contents unavailable — tree shows empty */
    } finally {
      if (!signal?.aborted) {
        setLoadingDirs((prev) => { const next = new Set(prev); next.delete(dirPath); return next })
      }
    }
  }, [agentId, workspaceId, canQueryAgent, basePrefix])

  const toggleFolder = useCallback((path: string) => {
    setExpandedPaths((prev) => {
      const next = new Set(prev)
      if (next.has(path)) { next.delete(path) } else {
        next.add(path)
        const node = findNode(tree, path)
        if (node && node.is_dir && !node.childrenLoaded) {
          fetchSubdir(path)
        }
      }
      return next
    })
  }, [tree, fetchSubdir])

  const refresh = useCallback(() => {
    fetchRef.current?.()
  }, [])

  return {
    tree,
    basePrefix,
    loading,
    error,
    selectedPath,
    setSelectedPath,
    expandedPaths,
    loadingDirs,
    toggleFolder,
    refresh,
  }
}
