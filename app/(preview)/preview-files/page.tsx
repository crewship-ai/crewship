"use client"

import { useState } from "react"
import { cn } from "@/lib/utils"
import {
  Search, Download, Copy, Check, ChevronRight, ChevronDown,
  FolderOpen, FolderClosed, FileText, FileCode, FileJson,
  GitBranch, GitCommit, GitPullRequest, HardDrive, Home,
  Users, RefreshCw, Filter, SortAsc, MoreHorizontal,
  Terminal, Box, Eye, Columns3, LayoutList, PanelLeft,
  Plus, Settings, Trash2, Upload, Archive,
  File as FileIcon,
} from "lucide-react"

/* ── Mock Data ── */
const MOCK_TREE: TreeNode[] = [
  {
    name: "google-ads-env", path: "google-ads-env", isDir: true, size: 0, modTime: "2025-03-11T10:00:00Z",
    children: [
      { name: ".env", path: "google-ads-env/.env", isDir: false, size: 245, modTime: "2025-03-11T10:05:00Z", children: [] },
      { name: "config.yaml", path: "google-ads-env/config.yaml", isDir: false, size: 1024, modTime: "2025-03-11T09:30:00Z", children: [] },
      { name: "requirements.txt", path: "google-ads-env/requirements.txt", isDir: false, size: 312, modTime: "2025-03-10T15:20:00Z", children: [] },
    ],
  },
  {
    name: "google-ads-python-main", path: "google-ads-python-main", isDir: true, size: 0, modTime: "2025-03-11T09:00:00Z",
    children: [
      { name: "main.py", path: "google-ads-python-main/main.py", isDir: false, size: 4520, modTime: "2025-03-11T10:12:00Z", children: [] },
      { name: "campaign_manager.py", path: "google-ads-python-main/campaign_manager.py", isDir: false, size: 8340, modTime: "2025-03-11T10:08:00Z", children: [] },
      { name: "utils.py", path: "google-ads-python-main/utils.py", isDir: false, size: 1280, modTime: "2025-03-10T18:00:00Z", children: [] },
      {
        name: "tests", path: "google-ads-python-main/tests", isDir: true, size: 0, modTime: "2025-03-10T16:00:00Z",
        children: [
          { name: "test_campaigns.py", path: "google-ads-python-main/tests/test_campaigns.py", isDir: false, size: 2100, modTime: "2025-03-10T16:30:00Z", children: [] },
          { name: "conftest.py", path: "google-ads-python-main/tests/conftest.py", isDir: false, size: 540, modTime: "2025-03-10T16:00:00Z", children: [] },
        ],
      },
    ],
  },
  {
    name: "googleads_env", path: "googleads_env", isDir: true, size: 0, modTime: "2025-03-09T12:00:00Z",
    children: [
      { name: "activate.sh", path: "googleads_env/activate.sh", isDir: false, size: 890, modTime: "2025-03-09T12:00:00Z", children: [] },
      {
        name: "lib", path: "googleads_env/lib", isDir: true, size: 0, modTime: "2025-03-09T12:00:00Z",
        children: [
          { name: "python3.11", path: "googleads_env/lib/python3.11", isDir: true, size: 0, modTime: "2025-03-09T12:00:00Z", children: [] },
        ],
      },
    ],
  },
  { name: "NAVOD.md", path: "NAVOD.md", isDir: false, size: 2048, modTime: "2025-03-11T10:15:00Z", children: [] },
  { name: "report.json", path: "report.json", isDir: false, size: 15360, modTime: "2025-03-11T10:20:00Z", children: [] },
  { name: "Dockerfile", path: "Dockerfile", isDir: false, size: 640, modTime: "2025-03-08T14:00:00Z", children: [] },
  { name: ".gitignore", path: ".gitignore", isDir: false, size: 128, modTime: "2025-03-08T14:00:00Z", children: [] },
]

const MOCK_PREVIEW_CODE = `import os
from google.ads.googleads.client import GoogleAdsClient
from google.ads.googleads.errors import GoogleAdsException

class CampaignManager:
    """Manages Google Ads campaigns for the agent."""

    def __init__(self, credentials_path: str):
        self.client = GoogleAdsClient.load_from_storage(credentials_path)
        self.customer_id = os.getenv("GOOGLE_ADS_CUSTOMER_ID")

    def list_campaigns(self, status_filter: str = "ENABLED"):
        """List all campaigns with optional status filter."""
        ga_service = self.client.get_service("GoogleAdsService")
        query = f"""
            SELECT
                campaign.id,
                campaign.name,
                campaign.status,
                metrics.impressions,
                metrics.clicks,
                metrics.cost_micros
            FROM campaign
            WHERE campaign.status = '{status_filter}'
            ORDER BY metrics.impressions DESC
        """
        response = ga_service.search(
            customer_id=self.customer_id,
            query=query
        )
        campaigns = []
        for row in response:
            campaigns.append({
                "id": row.campaign.id,
                "name": row.campaign.name,
                "status": row.campaign.status.name,
                "impressions": row.metrics.impressions,
                "clicks": row.metrics.clicks,
                "cost": row.metrics.cost_micros / 1_000_000,
            })
        return campaigns

    def pause_campaign(self, campaign_id: str):
        """Pause a running campaign."""
        campaign_service = self.client.get_service("CampaignService")
        campaign_operation = self.client.get_type("CampaignOperation")
        campaign = campaign_operation.update
        campaign.resource_name = campaign_service.campaign_path(
            self.customer_id, campaign_id
        )
        campaign.status = self.client.enums.CampaignStatusEnum.PAUSED
        campaign_service.mutate_campaigns(
            customer_id=self.customer_id,
            operations=[campaign_operation],
        )
        return {"status": "paused", "campaign_id": campaign_id}
`

const MOCK_GIT_LOG = [
  { hash: "a1b2c3d", message: "feat: add campaign pause functionality", author: "Pepicek", time: "2 hours ago", branch: "main" },
  { hash: "e4f5g6h", message: "fix: handle empty API response gracefully", author: "Pepicek", time: "5 hours ago", branch: "main" },
  { hash: "i7j8k9l", message: "refactor: extract utils into separate module", author: "Pepicek", time: "1 day ago", branch: "main" },
  { hash: "m0n1o2p", message: "init: Google Ads campaign manager", author: "Pepicek", time: "2 days ago", branch: "main" },
]

interface TreeNode {
  name: string
  path: string
  isDir: boolean
  size: number
  modTime: string
  children: TreeNode[]
}

/* ── Icon Utils ── */
function getFileTypeIcon(name: string, isDir: boolean, isOpen?: boolean) {
  if (isDir) {
    return isOpen
      ? <FolderOpen className="h-4 w-4 text-amber-500" />
      : <FolderClosed className="h-4 w-4 text-amber-500" />
  }
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  const n = name.toLowerCase()
  if (n === "dockerfile" || n === "docker-compose.yml") return <Box className="h-4 w-4 text-blue-400" />
  if (n === ".gitignore" || n === ".gitattributes") return <GitBranch className="h-4 w-4 text-orange-500" />
  if (n === "makefile" || n === "cmakelists.txt") return <Terminal className="h-4 w-4 text-green-600" />
  switch (ext) {
    case "py": return <FileCode className="h-4 w-4 text-yellow-500" />
    case "js": case "jsx": return <FileCode className="h-4 w-4 text-yellow-400" />
    case "ts": case "tsx": return <FileCode className="h-4 w-4 text-blue-500" />
    case "go": return <FileCode className="h-4 w-4 text-cyan-500" />
    case "rs": return <FileCode className="h-4 w-4 text-orange-600" />
    case "json": return <FileJson className="h-4 w-4 text-yellow-600" />
    case "yaml": case "yml": return <FileJson className="h-4 w-4 text-red-400" />
    case "md": case "mdx": return <FileText className="h-4 w-4 text-blue-300" />
    case "txt": return <FileText className="h-4 w-4 text-gray-500" />
    case "sh": case "bash": case "zsh": return <Terminal className="h-4 w-4 text-green-500" />
    case "env": return <Settings className="h-4 w-4 text-gray-600" />
    case "html": return <FileCode className="h-4 w-4 text-orange-500" />
    case "css": case "scss": return <FileCode className="h-4 w-4 text-blue-400" />
    case "sql": return <FileCode className="h-4 w-4 text-blue-600" />
    case "toml": return <FileJson className="h-4 w-4 text-gray-500" />
    default: return <FileIcon className="h-4 w-4 text-gray-400" />
  }
}

function formatSize(bytes: number): string {
  if (bytes === 0) return "—"
  const units = ["B", "KB", "MB", "GB"]
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  const v = bytes / Math.pow(1024, i)
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

function timeAgo(iso: string): string {
  const mins = Math.floor((Date.now() - new Date(iso).getTime()) / 60000)
  if (mins < 1) return "Just now"
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  const days = Math.floor(hrs / 24)
  return days === 1 ? "Yesterday" : `${days}d ago`
}

function countFiles(nodes: TreeNode[]): { files: number; dirs: number; size: number } {
  let files = 0, dirs = 0, size = 0
  for (const n of nodes) {
    if (n.isDir) { dirs++; const sub = countFiles(n.children); files += sub.files; dirs += sub.dirs; size += sub.size }
    else { files++; size += n.size }
  }
  return { files, dirs, size }
}

/* ── Shared: Recursive File Tree ── */
function FileTreeNode({ node, depth, selectedPath, onSelect, expandedPaths, onToggle }: {
  node: TreeNode; depth: number; selectedPath: string | null
  onSelect: (path: string) => void; expandedPaths: Set<string>; onToggle: (path: string) => void
}) {
  const isOpen = expandedPaths.has(node.path)
  return (
    <>
      <button
        className={cn(
          "w-full flex items-center gap-1.5 py-1 pr-2 text-xs transition-colors hover:bg-accent/50",
          selectedPath === node.path && "bg-accent text-foreground",
          !node.isDir && selectedPath !== node.path && "text-muted-foreground",
        )}
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
        onClick={() => node.isDir ? onToggle(node.path) : onSelect(node.path)}
      >
        {node.isDir && (isOpen ? <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" /> : <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground" />)}
        {!node.isDir && <span className="w-3" />}
        {getFileTypeIcon(node.name, node.isDir, isOpen)}
        <span className="truncate">{node.name}</span>
        {!node.isDir && <span className="ml-auto text-[10px] text-muted-foreground shrink-0">{formatSize(node.size)}</span>}
      </button>
      {node.isDir && isOpen && node.children.map((child) => (
        <FileTreeNode key={child.path} node={child} depth={depth + 1} selectedPath={selectedPath} onSelect={onSelect} expandedPaths={expandedPaths} onToggle={onToggle} />
      ))}
    </>
  )
}

/* ── Shared: File Preview Panel ── */
function FilePreviewPanel({ selectedPath, className }: { selectedPath: string | null; className?: string }) {
  const [copied, setCopied] = useState(false)
  const file = selectedPath ? findNode(MOCK_TREE, selectedPath) : null

  if (!file || file.isDir) {
    return (
      <div className={cn("flex flex-col items-center justify-center text-muted-foreground", className)}>
        <Eye className="h-8 w-8 mb-2 opacity-30" />
        <p className="text-sm">Select a file to preview</p>
      </div>
    )
  }

  return (
    <div className={cn("flex flex-col", className)}>
      <div className="flex items-center gap-2 px-4 h-[41px] border-b shrink-0">
        <div className="flex items-center gap-1.5 min-w-0 flex-1">
          {getFileTypeIcon(file.name, false)}
          <span className="text-xs font-medium truncate">{file.name}</span>
          <span className="text-[10px] text-muted-foreground shrink-0">{formatSize(file.size)}</span>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          <button className="h-6 w-6 flex items-center justify-center rounded hover:bg-accent" title="Copy path"
            onClick={() => { setCopied(true); setTimeout(() => setCopied(false), 2000) }}>
            {copied ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3 text-muted-foreground" />}
          </button>
          <button className="h-6 w-6 flex items-center justify-center rounded hover:bg-accent" title="Download">
            <Download className="h-3 w-3 text-muted-foreground" />
          </button>
        </div>
      </div>
      <div className="flex-1 overflow-y-auto bg-[#1e1e1e] text-[#d4d4d4] font-mono text-xs leading-5">
        {MOCK_PREVIEW_CODE.split("\n").map((line, i) => (
          <div key={i} className="flex hover:bg-white/5">
            <span className="w-10 shrink-0 text-right pr-3 text-[#858585] select-none">{i + 1}</span>
            <pre className="flex-1 pr-4 whitespace-pre-wrap break-all">
              {colorize(line)}
            </pre>
          </div>
        ))}
      </div>
    </div>
  )
}

function colorize(line: string) {
  if (line.trimStart().startsWith("#") || line.trimStart().startsWith("//")) return <span className="text-[#6A9955]">{line}</span>
  if (line.trimStart().startsWith('"""') || line.trimStart().startsWith("'''")) return <span className="text-[#6A9955]">{line}</span>
  if (line.match(/^\s*(import |from )/)) return <span className="text-[#C586C0]">{line}</span>
  if (line.match(/^\s*(class |def |async def |function )/)) return <span className="text-[#DCDCAA]">{line}</span>
  if (line.match(/^\s*(return |if |else|elif |for |while |try|except|finally)/)) return <span className="text-[#C586C0]">{line}</span>
  return <span>{line}</span>
}

function findNode(nodes: TreeNode[], path: string): TreeNode | null {
  for (const n of nodes) {
    if (n.path === path) return n
    if (n.isDir) { const f = findNode(n.children, path); if (f) return f }
  }
  return null
}

/* ════════════════════════════════════════════════════════════════════
   VARIANT A: 3-Column Layout (Source Nav + Tree + Preview)
   ════════════════════════════════════════════════════════════════════ */
function VariantA() {
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
function VariantB() {
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
function VariantC() {
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
function VariantD() {
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
function VariantE() {
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

/* ════════════════════════════════════════════════════════════════════
   PAGE: Variant Selector
   ════════════════════════════════════════════════════════════════════ */
const VARIANTS = [
  { id: "A", title: "3-Column: Source Nav + Tree + Preview", desc: "Dedicated source navigator on the left, file tree in the middle, large preview on the right. Best for complex multi-source browsing.", component: VariantA },
  { id: "B", title: "2-Column: Source Tabs + Tree + Preview", desc: "Source tabs at the top, cleaner layout. More horizontal space for the tree and preview.", component: VariantB },
  { id: "C", title: "VS Code Style: Activity Bar + Explorer + Editor", desc: "Familiar IDE-like experience with activity bar, collapsible sections (Agent Output, Container, Crew), git panel, and file search. Most feature-rich.", component: VariantC },
  { id: "D", title: "Minimal: Collapsible Tree + Large Preview", desc: "Simple 2-panel with collapsible file tree. Source quick-switch tabs within the tree. Maximizes preview area.", component: VariantD },
  { id: "E", title: "GitHub Style: Breadcrumb + Table + Drawer", desc: "Navigate directories like GitHub. File table with commit info, size, time. Preview slides in from the right. Familiar for developers.", component: VariantE },
]

export default function PreviewFilesPage() {
  const [activeVariant, setActiveVariant] = useState("A")
  const ActiveComponent = VARIANTS.find((v) => v.id === activeVariant)!.component

  return (
    <div className="max-w-7xl mx-auto py-8 px-6 space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Files Page Redesign</h1>
        <p className="text-sm text-muted-foreground mt-1">Full-page file browser with multi-source navigation, VS Code-style icons, git integration (future), and container browsing.</p>
      </div>

      {/* Variant selector */}
      <div className="flex flex-wrap gap-2">
        {VARIANTS.map((v) => (
          <button
            key={v.id}
            onClick={() => setActiveVariant(v.id)}
            className={cn(
              "px-3 py-1.5 rounded-lg text-xs font-medium transition-colors border",
              activeVariant === v.id ? "bg-primary text-primary-foreground border-primary" : "bg-card border-border text-muted-foreground hover:text-foreground"
            )}
          >
            {v.id}: {v.title.split(":")[0]}
          </button>
        ))}
      </div>

      {/* Active variant description */}
      <div className="bg-card border rounded-lg px-4 py-3">
        <div className="text-sm font-semibold">{VARIANTS.find((v) => v.id === activeVariant)!.title}</div>
        <p className="text-xs text-muted-foreground mt-0.5">{VARIANTS.find((v) => v.id === activeVariant)!.desc}</p>
      </div>

      {/* Mockup */}
      <ActiveComponent />

      {/* Feature comparison */}
      <div className="border rounded-lg overflow-hidden">
        <table className="w-full text-xs">
          <thead>
            <tr className="bg-muted/50">
              <th className="text-left px-4 py-2 font-semibold">Feature</th>
              {VARIANTS.map((v) => <th key={v.id} className="text-center px-3 py-2 font-semibold">{v.id}</th>)}
            </tr>
          </thead>
          <tbody>
            {[
              ["Multi-source navigation", "yes", "yes", "yes", "yes", "yes"],
              ["Recursive file tree", "yes", "yes", "yes", "yes", "no (table)"],
              ["Git integration panel", "no", "no", "yes", "no", "partial"],
              ["File search across files", "filter", "filter", "yes", "no", "no"],
              ["Collapsible sidebar", "no", "no", "no", "yes", "no"],
              ["VS Code-like icons", "yes", "yes", "yes", "yes", "yes"],
              ["Breadcrumb navigation", "no", "no", "yes", "no", "yes"],
              ["Open file tabs", "no", "no", "yes", "no", "no"],
              ["Code preview with syntax", "yes", "yes", "yes", "yes", "yes"],
              ["Space efficiency", "medium", "high", "medium", "high", "high"],
              ["Complexity to implement", "low", "low", "high", "low", "medium"],
            ].map(([feature, ...values], i) => (
              <tr key={i} className="border-t">
                <td className="px-4 py-2 text-muted-foreground">{feature}</td>
                {values.map((v, j) => (
                  <td key={j} className={cn("text-center px-3 py-2", v === "yes" ? "text-emerald-600" : v === "no" ? "text-muted-foreground/50" : "text-amber-600")}>{v}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
