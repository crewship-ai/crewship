"use client"

import { useState } from "react"
import { cn } from "@/lib/utils"
import {
  Search, Download, Copy, Check, ChevronRight, ChevronDown,
  FolderOpen, FolderClosed, FileText, FileCode, FileJson, ArrowLeft,
  GitBranch, Home, RefreshCw,
  Terminal, Box, Settings, LayoutDashboard, Zap, Key, Activity,
  Network, Store, Shield, ShieldCheck, Bot, MessageSquare, ScrollText,
  Bug, History, Bell,
  File as FileIcon,
} from "lucide-react"

/* ── Mock file tree ── */
interface TreeNode {
  name: string; path: string; isDir: boolean; size: number; modTime: string; children: TreeNode[]
}

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
    ],
  },
  { name: "NAVOD.md", path: "NAVOD.md", isDir: false, size: 2048, modTime: "2025-03-11T10:15:00Z", children: [] },
  { name: "report.json", path: "report.json", isDir: false, size: 15360, modTime: "2025-03-11T10:20:00Z", children: [] },
  { name: "Dockerfile", path: "Dockerfile", isDir: false, size: 640, modTime: "2025-03-08T14:00:00Z", children: [] },
  { name: ".gitignore", path: ".gitignore", isDir: false, size: 128, modTime: "2025-03-08T14:00:00Z", children: [] },
]

const MOCK_CODE = `import os
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
        return {"status": "paused", "campaign_id": campaign_id}`

/* ── Icon utils ── */
function getIcon(name: string, isDir: boolean, isOpen?: boolean) {
  if (isDir) return isOpen ? <FolderOpen className="h-4 w-4 text-amber-500" /> : <FolderClosed className="h-4 w-4 text-amber-500" />
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  const n = name.toLowerCase()
  if (n === "dockerfile") return <Box className="h-4 w-4 text-blue-400" />
  if (n.startsWith(".git")) return <GitBranch className="h-4 w-4 text-orange-500" />
  switch (ext) {
    case "py": return <FileCode className="h-4 w-4 text-yellow-500" />
    case "js": case "jsx": return <FileCode className="h-4 w-4 text-yellow-400" />
    case "ts": case "tsx": return <FileCode className="h-4 w-4 text-blue-500" />
    case "go": return <FileCode className="h-4 w-4 text-cyan-500" />
    case "json": return <FileJson className="h-4 w-4 text-yellow-600" />
    case "yaml": case "yml": return <FileJson className="h-4 w-4 text-red-400" />
    case "md": return <FileText className="h-4 w-4 text-blue-300" />
    case "txt": return <FileText className="h-4 w-4 text-gray-500" />
    case "sh": return <Terminal className="h-4 w-4 text-green-500" />
    case "env": return <Settings className="h-4 w-4 text-gray-600" />
    case "css": return <FileCode className="h-4 w-4 text-blue-400" />
    default: return <FileIcon className="h-4 w-4 text-gray-400" />
  }
}

function fmtSize(b: number) {
  if (!b) return "—"
  const u = ["B", "KB", "MB", "GB"]
  const i = Math.floor(Math.log(b) / Math.log(1024))
  return `${(b / Math.pow(1024, i)).toFixed(i ? 1 : 0)} ${u[i]}`
}

function fmtTime(iso: string) {
  const m = Math.floor((Date.now() - new Date(iso).getTime()) / 60000)
  if (m < 1) return "Just now"
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

function countFiles(nodes: TreeNode[]): { files: number; dirs: number; size: number } {
  let files = 0, dirs = 0, size = 0
  for (const n of nodes) {
    if (n.isDir) { dirs++; const s = countFiles(n.children); files += s.files; dirs += s.dirs; size += s.size }
    else { files++; size += n.size }
  }
  return { files, dirs, size }
}

function findNode(nodes: TreeNode[], path: string): TreeNode | null {
  for (const n of nodes) {
    if (n.path === path) return n
    if (n.isDir) { const f = findNode(n.children, path); if (f) return f }
  }
  return null
}

function colorize(line: string) {
  if (line.trimStart().startsWith("#") || line.trimStart().startsWith("//")) return <span className="text-[#6A9955]">{line}</span>
  if (line.trimStart().startsWith('"""')) return <span className="text-[#6A9955]">{line}</span>
  if (line.match(/^\s*(import |from )/)) return <span className="text-[#C586C0]">{line}</span>
  if (line.match(/^\s*(class |def )/)) return <span className="text-[#DCDCAA]">{line}</span>
  if (line.match(/^\s*(return |if |else|for |while )/)) return <span className="text-[#C586C0]">{line}</span>
  return <span>{line}</span>
}

/* ── Shared chrome ── */
const mainNav = [
  { label: "Work", items: [
    { title: "Dashboard", icon: LayoutDashboard },
    { title: "Crews", icon: Network },
    { title: "Agents", icon: Bot, active: true },
  ]},
  { label: "Configure", items: [
    { title: "Skills", icon: Zap },
    { title: "Marketplace", icon: Store, future: true },
    { title: "Credentials", icon: Key },
  ]},
  { label: "Monitor", items: [
    { title: "Runs", icon: Activity },
    { title: "Audit Log", icon: Shield },
  ]},
  { label: "System", items: [
    { title: "Settings", icon: Settings },
    { title: "Admin", icon: ShieldCheck },
  ]},
]

const agentTabs = [
  { label: "Overview", icon: LayoutDashboard },
  { label: "Sessions", icon: MessageSquare },
  { label: "Files", icon: FolderOpen, active: true },
  { label: "Runs", icon: Activity },
  { label: "Logs", icon: ScrollText },
  { label: "Skills", icon: Zap },
  { label: "Credentials", icon: Key },
  { label: "Settings", icon: Settings },
  { label: "Debug", icon: Bug },
  { label: "History", icon: History },
]

function Logo({ className }: { className?: string }) {
  return <svg xmlns="http://www.w3.org/2000/svg" viewBox="190 190 710 470" fill="currentColor" className={className}>
    <path d="M415.978 643.199C394.751 639.37 373.822 636.562 352.809 634.248C326.436 631.343 300.012 630.21 273.515 631.406C263.713 631.849 253.913 632.739 244.168 633.893C239.955 634.391 237.319 633.559 235.22 629.676C224.052 609.02 212.738 588.443 201.35 567.907C199.583 564.721 200.982 563.48 203.587 562.185C226.108 550.996 248.47 539.478 271.138 528.598C298.76 515.341 326.551 502.431 354.416 489.692C394.25 471.482 434.623 454.51 475.028 437.605C507.907 423.849 540.95 410.503 574.136 397.523C616.157 381.088 658.373 365.15 700.791 349.753C744.302 333.958 788.135 319.103 832.028 304.417C833.744 303.843 835.42 302.991 837.634 303.468C839.113 307.377 837.499 311.253 836.777 315.001C828.73 356.781 816.766 397.458 801.3 437.066C777.028 499.226 745.989 557.845 707.705 612.526C686.991 642.111 657.919 658.481 622.564 664.132C594.192 668.667 565.861 667.057 537.461 663.855C497.035 659.297 457.441 650.052 417.401 643.34C417.073 643.285 416.738 643.277 415.978 643.199Z"/>
    <path d="M728.188 262.76C703.625 254.267 680.124 258.582 656.967 266.862C625.892 277.974 597.672 294.807 569.338 311.347C521.729 339.139 474.066 366.844 426.688 395.025C369.294 429.164 311.637 462.867 254.835 497.994C253.592 498.763 252.422 499.72 250.573 499.502C250.396 497.212 252.262 496.694 253.461 495.846C287.741 471.618 322 447.36 356.362 423.247C389.086 400.284 421.773 377.262 454.743 354.655C502.543 321.88 549.921 288.454 599.308 258.065C622.648 243.703 647.852 233.693 674.993 229.428C698.681 225.706 721.961 227.037 743.271 239.612C762.439 250.921 773.646 268.003 778.308 289.619C779.338 294.391 776.38 295.691 772.893 296.997C747.631 306.454 722.301 315.735 697.158 325.5C644.849 345.815 592.927 367.085 541.247 388.959C501.372 405.837 461.747 423.282 422.406 441.343C373.221 463.923 324.133 486.737 276.278 512.081C274.592 512.974 273.051 514.483 270.512 514.056C270.876 511.886 272.662 511.261 274.089 510.4C316.881 484.61 359.763 458.969 402.459 433.021C441.175 409.492 479.651 385.569 518.304 361.935C557.099 338.212 595.321 313.536 634.763 290.893C654.867 279.352 675.696 269.105 698.993 265.453C712.707 263.303 726.159 264.624 739.325 269.333C736.171 266.399 732.395 264.591 728.188 262.76Z"/>
  </svg>
}

function Sidebar() {
  return (
    <div className="w-12 bg-card border-r flex flex-col shrink-0">
      <div className="flex items-center justify-center py-3 border-b"><Logo className="h-5 w-5" /></div>
      <div className="flex-1 overflow-y-auto py-1">
        {mainNav.map((s) => s.items.map((item) => (
          <button key={item.title} title={item.title} className={cn("w-full flex items-center justify-center py-2 transition-colors", "active" in item && item.active ? "text-foreground" : "text-muted-foreground hover:text-foreground")}>
            <item.icon className="h-4 w-4" />
          </button>
        )))}
      </div>
    </div>
  )
}

function Toolbar() {
  return (
    <div className="flex h-11 items-center justify-between px-4 bg-card border-b shrink-0">
      <div className="flex items-center gap-1.5 text-sm">
        <span className="font-medium">Unify Technology</span>
        <span className="text-muted-foreground/40">/</span>
        <span className="text-muted-foreground">Agents</span>
        <span className="text-muted-foreground/40">/</span>
        <span className="font-semibold">Pepicek</span>
      </div>
      <div className="flex items-center gap-3">
        <div className="flex items-center gap-1 text-[10px] text-emerald-600"><span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" /> Connected</div>
        <Search className="h-3.5 w-3.5 text-muted-foreground" />
        <Bell className="h-3.5 w-3.5 text-muted-foreground" />
        <div className="h-6 w-6 rounded-full bg-primary text-[9px] font-bold text-primary-foreground flex items-center justify-center">PS</div>
      </div>
    </div>
  )
}

function Rail({ hoveredRail, setHoveredRail }: { hoveredRail: boolean; setHoveredRail: (v: boolean) => void }) {
  return (
    <div
      className={cn("bg-background border-r flex flex-col shrink-0 transition-all duration-200 overflow-hidden", hoveredRail ? "w-44" : "w-12")}
      onMouseEnter={() => setHoveredRail(true)} onMouseLeave={() => setHoveredRail(false)}
    >
      <div className={cn("flex items-center border-b shrink-0 h-[41px]", hoveredRail ? "gap-2.5 px-3" : "justify-center")}>
        <img src="https://api.dicebear.com/9.x/adventurer/svg?seed=Pepicek" alt="Pepicek" className="h-7 w-7 rounded-lg shrink-0" />
        {hoveredRail && <div className="min-w-0"><div className="text-xs font-semibold truncate">Pepicek</div><div className="flex items-center gap-1 mt-0.5"><span className="h-1.5 w-1.5 rounded-full bg-gray-400" /><span className="text-[9px] text-muted-foreground">IDLE</span></div></div>}
      </div>
      <div className="flex-1 overflow-y-auto py-1">
        {agentTabs.map((tab) => (
          <button key={tab.label} className={cn("w-full flex items-center transition-colors", hoveredRail ? "gap-2.5 px-3 py-2 text-xs font-medium" : "justify-center py-2.5", tab.active ? "bg-accent text-primary" : "text-muted-foreground hover:text-foreground hover:bg-accent/50")}>
            <tab.icon className="h-3.5 w-3.5 shrink-0" />
            {hoveredRail && <span className="truncate">{tab.label}</span>}
          </button>
        ))}
      </div>
    </div>
  )
}

/* ── File Tree Node ── */
function TreeNodeRow({ node, depth, selected, expanded, onSelect, onToggle }: {
  node: TreeNode; depth: number; selected: string | null; expanded: Set<string>
  onSelect: (p: string) => void; onToggle: (p: string) => void
}) {
  const isOpen = expanded.has(node.path)
  return (
    <>
      <button
        className={cn("w-full flex items-center gap-1.5 py-1.5 pr-2 text-xs transition-colors hover:bg-accent/50",
          selected === node.path && "bg-accent text-foreground font-medium",
          selected !== node.path && "text-muted-foreground"
        )}
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
        onClick={() => node.isDir ? onToggle(node.path) : onSelect(node.path)}
      >
        {node.isDir ? (isOpen ? <ChevronDown className="h-3 w-3 shrink-0" /> : <ChevronRight className="h-3 w-3 shrink-0" />) : <span className="w-3" />}
        {getIcon(node.name, node.isDir, isOpen)}
        <span className="truncate">{node.name}</span>
        {!node.isDir && <span className="ml-auto text-[10px] text-muted-foreground/60 shrink-0">{fmtSize(node.size)}</span>}
      </button>
      {node.isDir && isOpen && node.children.map((c) => (
        <TreeNodeRow key={c.path} node={c} depth={depth + 1} selected={selected} expanded={expanded} onSelect={onSelect} onToggle={onToggle} />
      ))}
    </>
  )
}

/* ════════════════════════════════════════════════════════════════════
   FULL-CONTEXT MOCKUP: Files page inside real Crewship chrome
   State A: File tree visible (no file selected or file selected with tree open)
   State B: File preview full-screen (tree hidden, back button to return)
   ════════════════════════════════════════════════════════════════════ */
export default function PreviewFilesV2Page() {
  const [hoveredRail, setHoveredRail] = useState(false)
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set(["google-ads-python-main", "google-ads-env"]))
  const [previewMode, setPreviewMode] = useState(false) // true = full-screen file preview
  const [search, setSearch] = useState("")
  const [copied, setCopied] = useState(false)
  const stats = countFiles(MOCK_TREE)

  const toggleFolder = (p: string) => {
    setExpandedPaths((prev) => { const next = new Set(prev); next.has(p) ? next.delete(p) : next.add(p); return next })
  }

  const selectFile = (p: string) => {
    setSelectedPath(p)
    setPreviewMode(true)
  }

  const backToTree = () => {
    setPreviewMode(false)
  }

  const selectedFile = selectedPath ? findNode(MOCK_TREE, selectedPath) : null

  return (
    <div className="h-screen flex overflow-hidden">
      {/* Main Sidebar */}
      <Sidebar />

      {/* Main content area */}
      <div className="flex-1 flex flex-col min-w-0">
        <Toolbar />

        {/* Content with rounded gray area */}
        <div className="flex-1 min-h-0 overflow-hidden bg-background rounded-t-3xl mr-2">
          <div className="flex h-full">
            {/* Agent Rail */}
            <Rail hoveredRail={hoveredRail} setHoveredRail={setHoveredRail} />

            {/* Files content area */}
            <div className="flex-1 flex flex-col min-w-0 overflow-hidden">

              {/* ── STATE: File Tree (no preview or preview dismissed) ── */}
              {!previewMode && (
                <>
                  {/* Header */}
                  <div className="flex items-center justify-between h-[41px] px-4 border-b shrink-0">
                    <div className="flex items-center gap-2">
                      <Home className="h-3.5 w-3.5 text-muted-foreground" />
                      <span className="text-xs font-semibold">Agent Files</span>
                      <span className="text-[10px] text-muted-foreground bg-muted rounded-full px-1.5">{stats.files}</span>
                    </div>
                    <div className="flex items-center gap-2">
                      <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
                      <span className="text-[9px] text-emerald-600">Live</span>
                      <RefreshCw className="h-3 w-3 text-muted-foreground cursor-pointer hover:text-foreground ml-1" />
                    </div>
                  </div>

                  {/* Source tabs */}
                  <div className="flex items-center border-b shrink-0 px-4">
                    <button className="pb-2.5 pt-2.5 text-[11px] font-medium border-b-2 border-primary text-foreground mr-5 mb-[-1px]">Agent Home</button>
                    <button className="pb-2.5 pt-2.5 text-[11px] text-muted-foreground/40 mr-5 cursor-not-allowed" title="Coming soon">Container</button>
                    <button className="pb-2.5 pt-2.5 text-[11px] text-muted-foreground/40 mr-5 cursor-not-allowed" title="Coming soon">Crew Shared</button>
                    <button className="pb-2.5 pt-2.5 text-[11px] text-muted-foreground/40 cursor-not-allowed flex items-center gap-1" title="Coming soon">
                      <GitBranch className="h-3 w-3" /> Git
                    </button>
                  </div>

                  {/* Search */}
                  <div className="px-4 py-2 shrink-0">
                    <div className="relative">
                      <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
                      <input className="w-full h-8 rounded-lg border bg-card pl-8 pr-3 text-xs" placeholder="Filter files..." value={search} onChange={(e) => setSearch(e.target.value)} />
                    </div>
                  </div>

                  {/* File tree -- takes full width */}
                  <div className="flex-1 overflow-y-auto">
                    <div className="max-w-2xl">
                      {MOCK_TREE.map((node) => (
                        <TreeNodeRow key={node.path} node={node} depth={0} selected={selectedPath} expanded={expandedPaths} onSelect={selectFile} onToggle={toggleFolder} />
                      ))}
                    </div>
                  </div>

                  {/* Status bar */}
                  <div className="px-4 py-2 border-t text-[10px] text-muted-foreground flex items-center justify-between shrink-0">
                    <span>{stats.files} files, {stats.dirs} folders · {fmtSize(stats.size)}</span>
                    <span>/output/pepicek/</span>
                  </div>
                </>
              )}

              {/* ── STATE: Full-screen file preview ── */}
              {previewMode && selectedFile && !selectedFile.isDir && (
                <>
                  {/* Preview header */}
                  <div className="flex items-center gap-3 h-[41px] px-4 border-b shrink-0">
                    <button onClick={backToTree} className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors">
                      <ArrowLeft className="h-3.5 w-3.5" />
                      <span>Back to files</span>
                    </button>
                    <div className="h-4 w-px bg-border" />
                    {/* Breadcrumb */}
                    <div className="flex items-center gap-1 text-xs text-muted-foreground min-w-0">
                      {selectedPath!.split("/").map((seg, i, arr) => (
                        <span key={i} className="flex items-center gap-1 shrink-0">
                          {i > 0 && <ChevronRight className="h-3 w-3" />}
                          <span className={cn(i === arr.length - 1 && "text-foreground font-medium")}>{seg}</span>
                        </span>
                      ))}
                    </div>
                    <div className="ml-auto flex items-center gap-2 shrink-0">
                      <span className="text-[10px] text-muted-foreground">{fmtSize(selectedFile.size)} · {fmtTime(selectedFile.modTime)}</span>
                      <button onClick={() => { setCopied(true); setTimeout(() => setCopied(false), 2000) }} className="h-6 w-6 flex items-center justify-center rounded hover:bg-accent" title="Copy path">
                        {copied ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3 text-muted-foreground" />}
                      </button>
                      <button className="h-6 w-6 flex items-center justify-center rounded hover:bg-accent" title="Download">
                        <Download className="h-3 w-3 text-muted-foreground" />
                      </button>
                    </div>
                  </div>

                  {/* Code preview -- full area */}
                  <div className="flex-1 overflow-y-auto bg-[#1e1e1e] text-[#d4d4d4] font-mono text-xs leading-5">
                    {MOCK_CODE.split("\n").map((line, i) => (
                      <div key={i} className="flex hover:bg-white/5">
                        <span className="w-12 shrink-0 text-right pr-4 text-[#858585] select-none py-px">{i + 1}</span>
                        <pre className="flex-1 pr-4 whitespace-pre-wrap break-all py-px">{colorize(line)}</pre>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
