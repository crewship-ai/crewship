"use client"

import { useState } from "react"
import { cn } from "@/lib/utils"
import {
  Search, ChevronRight, ChevronDown,
  FolderOpen, GitBranch, GitCommit, HardDrive, Home,
  Users, RefreshCw,
  PanelLeft, Plus,
  File as FileIcon,
} from "lucide-react"
import {
  MOCK_TREE, MOCK_GIT_LOG,
  formatSize, timeAgo, countFiles,
} from "./mocks"
import {
  getFileTypeIcon, FileTreeNode, FilePreviewPanel,
} from "./tree-components"

export function VariantA() {
  const [source, setSource] = useState<"agent" | "container" | "crew">("agent")
  const [selectedPath, setSelectedPath] = useState<string | null>("google-ads-python-main/main.py")
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set(["google-ads-python-main", "google-ads-env"]))
  const [search, setSearch] = useState("")
  const stats = countFiles(MOCK_TREE)

  const toggleFolder = (path: string) => {
    setExpandedPaths((prev) => { const next = new Set(prev); next.has(path) ? next.delete(path) : next.add(path); return next })
  }

  const sources = [
    { id: "agent" as const, label: "Agent Home", icon: Home, badge: `${stats.files}`, enabled: true },
    { id: "container" as const, label: "Container", icon: HardDrive, badge: "—", enabled: false },
    { id: "crew" as const, label: "Crew Shared", icon: Users, badge: "—", enabled: false },
  ]

  return (
    <div className="flex h-[600px] border rounded-xl overflow-hidden bg-background">
      {/* Source Navigator */}
      <div className="w-40 border-r flex flex-col shrink-0 bg-muted/30">
        <div className="flex items-center gap-2 h-[41px] px-3 border-b shrink-0">
          <FolderOpen className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-xs font-semibold">Sources</span>
        </div>
        <div className="flex-1 py-1">
          {sources.map((s) => (
            <button
              key={s.id}
              onClick={() => s.enabled && setSource(s.id)}
              className={cn(
                "w-full flex items-center gap-2 px-3 py-2 text-xs transition-colors",
                !s.enabled && "opacity-40 cursor-not-allowed",
                source === s.id && s.enabled ? "bg-accent text-foreground font-medium" : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
              )}
            >
              <s.icon className="h-3.5 w-3.5 shrink-0" />
              <span className="truncate">{s.label}</span>
              <span className="ml-auto text-[10px] bg-muted rounded px-1">{s.badge}</span>
            </button>
          ))}
        </div>
        <div className="border-t p-2 space-y-1">
          <button className="w-full flex items-center gap-2 px-2 py-1.5 text-[10px] text-muted-foreground hover:text-foreground rounded hover:bg-accent/50 transition-colors">
            <GitBranch className="h-3 w-3" />
            <span>Git</span>
            <span className="ml-auto text-[9px] bg-muted rounded px-1 py-0.5">Soon</span>
          </button>
        </div>
      </div>

      {/* File Tree */}
      <div className="w-64 border-r flex flex-col shrink-0">
        <div className="flex items-center gap-2 h-[41px] px-3 border-b shrink-0">
          <span className="text-[10px] text-muted-foreground">/output/</span>
          <span className="text-xs font-medium truncate">pepicek</span>
          <div className="ml-auto flex items-center gap-1">
            <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
            <span className="text-[9px] text-emerald-600">Live</span>
          </div>
        </div>
        <div className="px-2 py-1.5 shrink-0">
          <div className="relative">
            <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
            <input className="w-full h-7 rounded-md border bg-card pl-7 pr-2 text-xs" placeholder="Filter files..." value={search} onChange={(e) => setSearch(e.target.value)} />
          </div>
        </div>
        <div className="flex-1 overflow-y-auto">
          {MOCK_TREE.map((node) => (
            <FileTreeNode key={node.path} node={node} depth={0} selectedPath={selectedPath} onSelect={setSelectedPath} expandedPaths={expandedPaths} onToggle={toggleFolder} />
          ))}
        </div>
        <div className="px-3 py-2 border-t text-[10px] text-muted-foreground flex items-center justify-between">
          <span>{stats.files} files, {stats.dirs} folders</span>
          <span>{formatSize(stats.size)}</span>
        </div>
      </div>

      {/* Preview */}
      <FilePreviewPanel selectedPath={selectedPath} className="flex-1" />
    </div>
  )
}

/* ════════════════════════════════════════════════════════════════════
   VARIANT B: 2-Column with Source Tabs (Tabs at top + Tree + Preview)
   ════════════════════════════════════════════════════════════════════ */
export function VariantB() {
  const [source, setSource] = useState<"agent" | "container" | "crew">("agent")
  const [selectedPath, setSelectedPath] = useState<string | null>("google-ads-python-main/main.py")
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set(["google-ads-python-main", "google-ads-env"]))
  const [search, setSearch] = useState("")
  const stats = countFiles(MOCK_TREE)

  const toggleFolder = (path: string) => {
    setExpandedPaths((prev) => { const next = new Set(prev); next.has(path) ? next.delete(path) : next.add(path); return next })
  }

  const sources = [
    { id: "agent" as const, label: "Agent", icon: Home, enabled: true },
    { id: "container" as const, label: "Container", icon: HardDrive, enabled: false },
    { id: "crew" as const, label: "Crew", icon: Users, enabled: false },
  ]

  return (
    <div className="flex flex-col h-[600px] border rounded-xl overflow-hidden bg-background">
      {/* Top: Source tabs + actions */}
      <div className="flex items-center border-b shrink-0 h-[41px]">
        <div className="flex items-end h-full px-2">
          {sources.map((s) => (
            <button
              key={s.id}
              onClick={() => s.enabled && setSource(s.id)}
              className={cn(
                "flex items-center gap-1.5 px-3 pb-2.5 text-xs font-medium border-b-2 mb-[-1px] transition-colors",
                !s.enabled && "opacity-40 cursor-not-allowed",
                source === s.id && s.enabled ? "border-primary text-foreground" : "border-transparent text-muted-foreground"
              )}
            >
              <s.icon className="h-3 w-3" />
              {s.label}
            </button>
          ))}
        </div>
        <div className="ml-auto flex items-center gap-2 px-3">
          <button className="flex items-center gap-1 text-[10px] text-muted-foreground hover:text-foreground">
            <GitBranch className="h-3 w-3" />
            <span>main</span>
          </button>
          <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
          <span className="text-[9px] text-emerald-600">Live</span>
        </div>
      </div>

      {/* Content: Tree + Preview */}
      <div className="flex flex-1 overflow-hidden">
        {/* Tree */}
        <div className="w-64 border-r flex flex-col shrink-0">
          <div className="px-2 py-1.5 shrink-0">
            <div className="relative">
              <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
              <input className="w-full h-7 rounded-md border bg-card pl-7 pr-2 text-xs" placeholder="Filter files..." value={search} onChange={(e) => setSearch(e.target.value)} />
            </div>
          </div>
          <div className="flex-1 overflow-y-auto">
            {MOCK_TREE.map((node) => (
              <FileTreeNode key={node.path} node={node} depth={0} selectedPath={selectedPath} onSelect={setSelectedPath} expandedPaths={expandedPaths} onToggle={toggleFolder} />
            ))}
          </div>
          <div className="px-3 py-2 border-t text-[10px] text-muted-foreground flex items-center justify-between">
            <span>{stats.files} files · {formatSize(stats.size)}</span>
            <RefreshCw className="h-3 w-3 cursor-pointer hover:text-foreground" />
          </div>
        </div>

        {/* Preview */}
        <FilePreviewPanel selectedPath={selectedPath} className="flex-1" />
      </div>
    </div>
  )
}

/* ════════════════════════════════════════════════════════════════════
   VARIANT C: VS Code Style (Activity Bar + Explorer + Editor + Minimap)
   ════════════════════════════════════════════════════════════════════ */
export function VariantC() {
  const [activeTab, setActiveTab] = useState<"files" | "git" | "search">("files")
  const [selectedPath, setSelectedPath] = useState<string | null>("google-ads-python-main/main.py")
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set(["google-ads-python-main", "google-ads-env"]))
  const [search, setSearch] = useState("")
  const stats = countFiles(MOCK_TREE)

  const toggleFolder = (path: string) => {
    setExpandedPaths((prev) => { const next = new Set(prev); next.has(path) ? next.delete(path) : next.add(path); return next })
  }

  const activityItems = [
    { id: "files" as const, icon: FileIcon, label: "Explorer" },
    { id: "search" as const, icon: Search, label: "Search" },
    { id: "git" as const, icon: GitBranch, label: "Git" },
  ]

  return (
    <div className="flex h-[600px] border rounded-xl overflow-hidden bg-background">
      {/* Activity Bar (thin icon strip) */}
      <div className="w-10 bg-muted/50 border-r flex flex-col items-center pt-2 gap-1 shrink-0">
        {activityItems.map((item) => (
          <button
            key={item.id}
            onClick={() => setActiveTab(item.id)}
            className={cn(
              "h-9 w-9 flex items-center justify-center rounded-lg transition-colors",
              activeTab === item.id ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
            )}
            title={item.label}
          >
            <item.icon className="h-4 w-4" />
          </button>
        ))}
      </div>

      {/* Sidebar Panel */}
      <div className="w-56 border-r flex flex-col shrink-0">
        {activeTab === "files" && (
          <>
            <div className="flex items-center justify-between h-[41px] px-3 border-b shrink-0">
              <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Explorer</span>
              <div className="flex items-center gap-1">
                <button className="h-5 w-5 flex items-center justify-center rounded hover:bg-accent" title="New file"><Plus className="h-3 w-3 text-muted-foreground" /></button>
                <button className="h-5 w-5 flex items-center justify-center rounded hover:bg-accent" title="Refresh"><RefreshCw className="h-3 w-3 text-muted-foreground" /></button>
              </div>
            </div>
            <div className="px-2 py-1.5 shrink-0">
              <div className="relative">
                <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
                <input className="w-full h-6 rounded border bg-card pl-7 pr-2 text-[11px]" placeholder="Filter..." value={search} onChange={(e) => setSearch(e.target.value)} />
              </div>
            </div>
            <div className="flex-1 overflow-y-auto">
              <div className="px-2 pt-1 pb-0.5">
                <button className="w-full flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground hover:text-foreground py-1">
                  <ChevronDown className="h-2.5 w-2.5" />
                  Agent Output
                </button>
              </div>
              {MOCK_TREE.map((node) => (
                <FileTreeNode key={node.path} node={node} depth={0} selectedPath={selectedPath} onSelect={setSelectedPath} expandedPaths={expandedPaths} onToggle={toggleFolder} />
              ))}
              <div className="px-2 pt-3 pb-0.5">
                <button className="w-full flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/50 py-1">
                  <ChevronRight className="h-2.5 w-2.5" />
                  Container Root
                  <span className="ml-auto text-[8px] bg-muted rounded px-1">Soon</span>
                </button>
              </div>
              <div className="px-2 pt-1 pb-0.5">
                <button className="w-full flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/50 py-1">
                  <ChevronRight className="h-2.5 w-2.5" />
                  Crew Shared
                  <span className="ml-auto text-[8px] bg-muted rounded px-1">Soon</span>
                </button>
              </div>
            </div>
            <div className="px-3 py-1.5 border-t text-[10px] text-muted-foreground flex items-center gap-2">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
              {stats.files} files · {formatSize(stats.size)}
            </div>
          </>
        )}
        {activeTab === "git" && (
          <>
            <div className="flex items-center h-[41px] px-3 border-b shrink-0">
              <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Source Control</span>
            </div>
            <div className="flex items-center gap-2 px-3 py-2 border-b">
              <GitBranch className="h-3.5 w-3.5 text-muted-foreground" />
              <span className="text-xs font-medium">main</span>
              <ChevronDown className="h-3 w-3 text-muted-foreground ml-auto" />
            </div>
            <div className="flex-1 overflow-y-auto py-1">
              <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Recent Commits</div>
              {MOCK_GIT_LOG.map((commit) => (
                <div key={commit.hash} className="px-3 py-2 hover:bg-accent/50 cursor-pointer">
                  <div className="flex items-center gap-2">
                    <GitCommit className="h-3 w-3 text-muted-foreground shrink-0" />
                    <span className="text-xs truncate">{commit.message}</span>
                  </div>
                  <div className="flex items-center gap-2 mt-0.5 ml-5">
                    <span className="text-[10px] text-muted-foreground font-mono">{commit.hash}</span>
                    <span className="text-[10px] text-muted-foreground">{commit.time}</span>
                  </div>
                </div>
              ))}
            </div>
          </>
        )}
        {activeTab === "search" && (
          <>
            <div className="flex items-center h-[41px] px-3 border-b shrink-0">
              <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Search</span>
            </div>
            <div className="px-3 py-2">
              <input className="w-full h-7 rounded border bg-card px-2 text-xs" placeholder="Search in files..." />
              <div className="flex items-center gap-2 mt-1.5">
                <input className="w-full h-7 rounded border bg-card px-2 text-xs" placeholder="Include pattern (e.g. *.py)" />
              </div>
            </div>
            <div className="flex-1 flex items-center justify-center text-muted-foreground">
              <p className="text-xs">Type to search across files</p>
            </div>
          </>
        )}
      </div>

      {/* Editor / Preview */}
      <div className="flex-1 flex flex-col">
        {/* Open file tabs */}
        <div className="flex items-center h-[35px] border-b shrink-0 bg-muted/20">
          <div className="flex items-center gap-0.5 px-1">
            {selectedPath && (
              <div className="flex items-center gap-1.5 px-3 py-1.5 bg-background border-b-2 border-primary rounded-t text-xs">
                {getFileTypeIcon(selectedPath.split("/").pop() ?? "", false)}
                <span className="font-medium">{selectedPath.split("/").pop()}</span>
              </div>
            )}
          </div>
        </div>
        {/* Breadcrumb */}
        {selectedPath && (
          <div className="flex items-center gap-1 px-4 h-[25px] text-[10px] text-muted-foreground border-b shrink-0">
            {selectedPath.split("/").map((part, i, arr) => (
              <span key={i} className="flex items-center gap-1">
                {i > 0 && <ChevronRight className="h-2.5 w-2.5" />}
                <span className={i === arr.length - 1 ? "text-foreground" : ""}>{part}</span>
              </span>
            ))}
          </div>
        )}
        <FilePreviewPanel selectedPath={selectedPath} className="flex-1" />
      </div>
    </div>
  )
}

/* ════════════════════════════════════════════════════════════════════
   VARIANT D: Minimal 2-Panel (Expandable tree + large preview)
   ════════════════════════════════════════════════════════════════════ */
export function VariantD() {
  const [selectedPath, setSelectedPath] = useState<string | null>("google-ads-python-main/main.py")
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set(["google-ads-python-main", "google-ads-env"]))
  const [treeCollapsed, setTreeCollapsed] = useState(false)
  const stats = countFiles(MOCK_TREE)

  const toggleFolder = (path: string) => {
    setExpandedPaths((prev) => { const next = new Set(prev); next.has(path) ? next.delete(path) : next.add(path); return next })
  }

  return (
    <div className="flex h-[600px] border rounded-xl overflow-hidden bg-background">
      {/* Tree (collapsible) */}
      {!treeCollapsed && (
        <div className="w-72 border-r flex flex-col shrink-0">
          <div className="flex items-center justify-between h-[41px] px-3 border-b shrink-0">
            <div className="flex items-center gap-2">
              <Home className="h-3.5 w-3.5 text-muted-foreground" />
              <span className="text-xs font-semibold">Agent Files</span>
              <span className="text-[10px] text-muted-foreground bg-muted rounded-full px-1.5">{stats.files}</span>
            </div>
            <div className="flex items-center gap-1">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
              <button onClick={() => setTreeCollapsed(true)} className="h-5 w-5 flex items-center justify-center rounded hover:bg-accent ml-1">
                <PanelLeft className="h-3 w-3 text-muted-foreground" />
              </button>
            </div>
          </div>
          {/* Source quick-switch */}
          <div className="flex items-center border-b shrink-0">
            <button className="flex-1 text-center py-2 text-[10px] font-medium border-b-2 border-primary text-foreground">Agent Home</button>
            <button className="flex-1 text-center py-2 text-[10px] text-muted-foreground/50" title="Coming soon">Container</button>
            <button className="flex-1 text-center py-2 text-[10px] text-muted-foreground/50" title="Coming soon">Crew</button>
          </div>
          <div className="flex-1 overflow-y-auto">
            {MOCK_TREE.map((node) => (
              <FileTreeNode key={node.path} node={node} depth={0} selectedPath={selectedPath} onSelect={setSelectedPath} expandedPaths={expandedPaths} onToggle={toggleFolder} />
            ))}
          </div>
          <div className="px-3 py-2 border-t text-[10px] text-muted-foreground flex items-center justify-between">
            <span>{stats.files} files, {stats.dirs} folders · {formatSize(stats.size)}</span>
          </div>
        </div>
      )}

      {/* Collapsed tree toggle */}
      {treeCollapsed && (
        <button onClick={() => setTreeCollapsed(false)} className="w-10 border-r flex flex-col items-center pt-3 shrink-0 hover:bg-accent/30">
          <PanelLeft className="h-4 w-4 text-muted-foreground rotate-180" />
          <span className="text-[9px] text-muted-foreground mt-2 [writing-mode:vertical-lr]">Files ({stats.files})</span>
        </button>
      )}

      {/* Preview */}
      <FilePreviewPanel selectedPath={selectedPath} className="flex-1" />
    </div>
  )
}

/* ════════════════════════════════════════════════════════════════════
   VARIANT E: GitHub-Style (Breadcrumb nav + table list + preview drawer)
   ════════════════════════════════════════════════════════════════════ */
export function VariantE() {
  const [currentDir, setCurrentDir] = useState<string[]>([])
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [showPreview, setShowPreview] = useState(false)

  const getCurrentEntries = () => {
    let entries = MOCK_TREE
    for (const seg of currentDir) {
      const dir = entries.find((n) => n.name === seg && n.isDir)
      if (dir) entries = dir.children
      else break
    }
    return [...entries.filter((n) => n.isDir).sort((a, b) => a.name.localeCompare(b.name)),
            ...entries.filter((n) => !n.isDir).sort((a, b) => a.name.localeCompare(b.name))]
  }

  const entries = getCurrentEntries()

  const navigateToDir = (name: string) => setCurrentDir([...currentDir, name])
  const navigateToBreadcrumb = (index: number) => setCurrentDir(currentDir.slice(0, index))
  const selectFile = (path: string) => { setSelectedPath(path); setShowPreview(true) }

  return (
    <div className="flex h-[600px] border rounded-xl overflow-hidden bg-background">
      <div className={cn("flex flex-col", showPreview ? "w-1/2 border-r" : "flex-1")}>
        {/* Header */}
        <div className="flex items-center gap-3 h-[41px] px-4 border-b shrink-0">
          <div className="flex items-center gap-1.5">
            <Home className="h-3.5 w-3.5 text-muted-foreground" />
            <span className="text-xs font-medium">Agent Home</span>
          </div>
          <div className="flex items-center gap-1 text-xs text-muted-foreground">
            <button onClick={() => navigateToBreadcrumb(0)} className="hover:text-foreground">output</button>
            {currentDir.map((seg, i) => (
              <span key={i} className="flex items-center gap-1">
                <span>/</span>
                <button onClick={() => navigateToBreadcrumb(i + 1)} className="hover:text-foreground">{seg}</button>
              </span>
            ))}
          </div>
          <div className="ml-auto flex items-center gap-2">
            <button className="flex items-center gap-1 text-[10px] text-muted-foreground hover:text-foreground">
              <GitBranch className="h-3 w-3" /> main
            </button>
            <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
          </div>
        </div>

        {/* Source tabs */}
        <div className="flex items-center border-b shrink-0 px-4">
          <button className="pb-2 pt-2 text-[11px] font-medium border-b-2 border-primary text-foreground mr-4">Agent Home</button>
          <button className="pb-2 pt-2 text-[11px] text-muted-foreground/50 mr-4">Container</button>
          <button className="pb-2 pt-2 text-[11px] text-muted-foreground/50">Crew Shared</button>
        </div>

        {/* File table */}
        <div className="flex-1 overflow-y-auto">
          {/* Git info bar */}
          <div className="flex items-center gap-2 px-4 py-2 border-b bg-muted/20 text-xs">
            <GitCommit className="h-3 w-3 text-muted-foreground" />
            <span className="text-muted-foreground">Pepicek</span>
            <span className="truncate">feat: add campaign pause functionality</span>
            <span className="ml-auto text-muted-foreground shrink-0">a1b2c3d · 2 hours ago</span>
          </div>
          <table className="w-full text-xs">
            <tbody>
              {entries.map((entry) => (
                <tr
                  key={entry.path}
                  className="border-b hover:bg-muted/30 cursor-pointer"
                  onClick={() => entry.isDir ? navigateToDir(entry.name) : selectFile(entry.path)}
                >
                  <td className="px-4 py-2.5 flex items-center gap-2">
                    {getFileTypeIcon(entry.name, entry.isDir)}
                    <span className={cn(entry.isDir && "font-medium")}>{entry.name}</span>
                  </td>
                  <td className="px-4 py-2.5 text-muted-foreground text-right whitespace-nowrap">{entry.isDir ? "—" : formatSize(entry.size)}</td>
                  <td className="px-4 py-2.5 text-muted-foreground text-right whitespace-nowrap">{timeAgo(entry.modTime)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="px-4 py-2 border-t text-[10px] text-muted-foreground">
          {entries.filter((e) => !e.isDir).length} files, {entries.filter((e) => e.isDir).length} folders
        </div>
      </div>

      {/* Preview drawer */}
      {showPreview && (
        <div className="flex-1 flex flex-col">
          <FilePreviewPanel selectedPath={selectedPath} className="flex-1" />
        </div>
      )}
    </div>
  )
}

