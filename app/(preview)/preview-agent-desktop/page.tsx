"use client"

import { useState } from "react"
import { cn } from "@/lib/utils"
import {
  Search, Send, Plus, ChevronDown, BookOpen, Bell,
  LayoutDashboard, Zap, Key, Activity, Network, Store,
  Settings, Bug, History, MessageSquare, Shield, ShieldCheck,
  FolderOpen, ScrollText, Paperclip, PanelLeftClose,
  User, Bot, MoreVertical,
} from "lucide-react"

/* Crewship main sidebar nav */
const mainNavSections = [
  {
    label: "Work",
    items: [
      { title: "Dashboard", href: "/", icon: LayoutDashboard },
      { title: "Crews", href: "/crews", icon: Network },
      { title: "Agents", href: "/agents", icon: Bot, active: true },
    ],
  },
  {
    label: "Configure",
    items: [
      { title: "Skills", href: "/skills", icon: Zap },
      { title: "Marketplace", href: "/marketplace", icon: Store, future: true },
      { title: "Credentials", href: "/credentials", icon: Key },
    ],
  },
  {
    label: "Monitor",
    items: [
      { title: "Runs", href: "/runs", icon: Activity },
      { title: "Audit Log", href: "/audit", icon: Shield },
    ],
  },
  {
    label: "System",
    items: [
      { title: "Settings", href: "/settings", icon: Settings },
      { title: "Admin", href: "/admin", icon: ShieldCheck },
    ],
  },
]

function CrewshipLogo({ className }: { className?: string }) {
  return (
    <svg xmlns="http://www.w3.org/2000/svg" viewBox="190 190 710 470" fill="currentColor" className={className}>
      <path d="M415.978 643.199C394.751 639.37 373.822 636.562 352.809 634.248C326.436 631.343 300.012 630.21 273.515 631.406C263.713 631.849 253.913 632.739 244.168 633.893C239.955 634.391 237.319 633.559 235.22 629.676C224.052 609.02 212.738 588.443 201.35 567.907C199.583 564.721 200.982 563.48 203.587 562.185C226.108 550.996 248.47 539.478 271.138 528.598C298.76 515.341 326.551 502.431 354.416 489.692C394.25 471.482 434.623 454.51 475.028 437.605C507.907 423.849 540.95 410.503 574.136 397.523C616.157 381.088 658.373 365.15 700.791 349.753C744.302 333.958 788.135 319.103 832.028 304.417C833.744 303.843 835.42 302.991 837.634 303.468C839.113 307.377 837.499 311.253 836.777 315.001C828.73 356.781 816.766 397.458 801.3 437.066C777.028 499.226 745.989 557.845 707.705 612.526C686.991 642.111 657.919 658.481 622.564 664.132C594.192 668.667 565.861 667.057 537.461 663.855C497.035 659.297 457.441 650.052 417.401 643.34C417.073 643.285 416.738 643.277 415.978 643.199Z"/>
      <path d="M728.188 262.76C703.625 254.267 680.124 258.582 656.967 266.862C625.892 277.974 597.672 294.807 569.338 311.347C521.729 339.139 474.066 366.844 426.688 395.025C369.294 429.164 311.637 462.867 254.835 497.994C253.592 498.763 252.422 499.72 250.573 499.502C250.396 497.212 252.262 496.694 253.461 495.846C287.741 471.618 322 447.36 356.362 423.247C389.086 400.284 421.773 377.262 454.743 354.655C502.543 321.88 549.921 288.454 599.308 258.065C622.648 243.703 647.852 233.693 674.993 229.428C698.681 225.706 721.961 227.037 743.271 239.612C762.439 250.921 773.646 268.003 778.308 289.619C779.338 294.391 776.38 295.691 772.893 296.997C747.631 306.454 722.301 315.735 697.158 325.5C644.849 345.815 592.927 367.085 541.247 388.959C501.372 405.837 461.747 423.282 422.406 441.343C373.221 463.923 324.133 486.737 276.278 512.081C274.592 512.974 273.051 514.483 270.512 514.056C270.876 511.886 272.662 511.261 274.089 510.4C316.881 484.61 359.763 458.969 402.459 433.021C441.175 409.492 479.651 385.569 518.304 361.935C557.099 338.212 595.321 313.536 634.763 290.893C654.867 279.352 675.696 269.105 698.993 265.453C712.707 263.303 726.159 264.624 739.325 269.333C736.171 266.399 732.395 264.591 728.188 262.76Z"/>
    </svg>
  )
}

function MainSidebar({ collapsed }: { collapsed?: boolean }) {
  return (
    <div className={cn("bg-card border-r flex flex-col shrink-0", collapsed ? "w-12" : "w-48")}>
      {/* Logo */}
      <div className={cn("flex items-center border-b shrink-0", collapsed ? "justify-center py-3" : "gap-2 px-3 py-3")}>
        <CrewshipLogo className="h-5 w-5 shrink-0" />
        {!collapsed && <span className="text-sm font-semibold">Crewship</span>}
      </div>
      {/* Nav sections */}
      <div className="flex-1 overflow-y-auto py-1">
        {mainNavSections.map((section) => (
          <div key={section.label} className="mb-1">
            {!collapsed && <div className="px-3 py-1 text-[9px] uppercase tracking-wider font-semibold text-muted-foreground">{section.label}</div>}
            {section.items.map((item) => (
              <button
                key={item.title}
                className={cn(
                  "w-full flex items-center transition-colors",
                  collapsed ? "justify-center py-2" : "gap-2.5 px-3 py-1.5 text-xs",
                  "future" in item && item.future
                    ? "text-muted-foreground/40 cursor-default"
                    : "active" in item && item.active
                      ? "bg-accent text-foreground font-medium"
                      : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
                )}
                title={collapsed ? item.title : undefined}
              >
                <item.icon className="h-4 w-4 shrink-0" />
                {!collapsed && <span>{item.title}</span>}
                {!collapsed && "future" in item && item.future && <span className="text-[8px] bg-muted px-1 rounded ml-auto">FUTURE</span>}
              </button>
            ))}
          </div>
        ))}
      </div>
      {/* Collapse button */}
      {!collapsed && (
        <div className="p-2 border-t">
          <button className="w-full flex items-center gap-2 px-2 py-1.5 text-xs text-muted-foreground hover:text-foreground rounded hover:bg-accent/50">
            <PanelLeftClose className="h-3.5 w-3.5" />
            <span>Collapse</span>
          </button>
        </div>
      )}
    </div>
  )
}

function TopToolbar() {
  return (
    <div className="flex h-12 items-center justify-between px-4 bg-card border-b shrink-0">
      {/* Left: breadcrumb */}
      <div className="flex items-center gap-1.5 min-w-0">
        <button className="flex items-center gap-1.5 rounded-md px-1.5 py-1 hover:bg-accent transition-colors shrink-0">
          <div className="flex h-5 w-5 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground">U</div>
          <span className="text-sm font-medium">Unify Technology</span>
          <ChevronDown className="h-3 w-3 text-muted-foreground" />
        </button>
        <span className="text-muted-foreground/40 text-sm">/</span>
        <span className="text-sm text-muted-foreground">Agents</span>
        <span className="text-muted-foreground/40 text-sm">/</span>
        <span className="text-sm font-semibold">Pepicek</span>
      </div>
      {/* Right: indicators */}
      <div className="flex items-center gap-1.5 shrink-0">
        <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-full border bg-emerald-50 border-emerald-200">
          <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
          <span className="text-[10px] font-medium text-emerald-700">Engine</span>
        </div>
        <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-full border bg-emerald-50 border-emerald-200">
          <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
          <span className="text-[10px] font-medium text-emerald-700">Live</span>
        </div>
        <button className="h-8 gap-2 rounded-full border px-3 flex items-center text-muted-foreground hover:text-foreground">
          <Search className="h-3.5 w-3.5" />
          <span className="text-xs">Search...</span>
          <kbd className="h-5 rounded border bg-muted px-1.5 text-[10px] font-mono">&#8984;K</kbd>
        </button>
        <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
          <BookOpen className="h-4 w-4 text-muted-foreground" />
        </button>
        <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent relative">
          <Bell className="h-4 w-4 text-muted-foreground" />
          <span className="absolute -top-0.5 -right-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-destructive text-[9px] font-bold text-white">3</span>
        </button>
        <button className="flex items-center gap-2 rounded-md px-1.5 py-1 hover:bg-accent">
          <div className="flex h-7 w-7 items-center justify-center rounded-full bg-primary text-[10px] font-semibold text-primary-foreground">PS</div>
          <span className="text-xs font-medium">Pavel</span>
          <ChevronDown className="h-3 w-3 text-muted-foreground" />
        </button>
      </div>
    </div>
  )
}

const agentPages = [
  { id: "overview", label: "Overview", icon: LayoutDashboard },
  { id: "sessions", label: "Sessions", icon: MessageSquare },
  { id: "files", label: "Files", icon: FolderOpen },
  { id: "runs", label: "Runs", icon: Activity },
  { id: "logs", label: "Logs", icon: ScrollText },
  { id: "skills", label: "Skills", icon: Zap },
  { id: "credentials", label: "Credentials", icon: Key },
  { id: "settings", label: "Settings", icon: Settings },
  { id: "debug", label: "Debug", icon: Bug },
  { id: "history", label: "History", icon: History },
]

const mockSessions = [
  { id: "1", title: "Campaign performance review", time: "2m ago", msgs: 12, active: true },
  { id: "2", title: "Budget optimization Q1", time: "1h ago", msgs: 28, active: false },
  { id: "3", title: "New keyword research", time: "3h ago", msgs: 8, active: true },
  { id: "4", title: "A/B test copy variants", time: "Yesterday", msgs: 15, active: false },
  { id: "5", title: "Competitor analysis report", time: "2 days ago", msgs: 34, active: false },
  { id: "6", title: "Landing page audit", time: "3 days ago", msgs: 9, active: false },
]

const mockMessages = [
  { role: "user", text: "Can you review the campaign performance for last week?", time: "14:02" },
  { role: "agent", text: "I'll analyze the Google Ads data for last week. Here's what I found:\n\n- Total spend: $2,450\n- Impressions: 125K (+12%)\n- CTR: 3.2% (above benchmark)\n- Conversions: 48 (+8%)\n- CPA: $51.04", time: "14:03" },
  { role: "user", text: "That looks good. What about the underperforming ad groups?", time: "14:05" },
  { role: "agent", text: "Three ad groups are below target:\n\n1. \"Brand awareness\" - CPA $89 (target $60)\n2. \"Retargeting cold\" - CTR 0.8%\n3. \"Display network\" - 0 conversions\n\nI recommend pausing Display network and reallocating budget to top performers.", time: "14:06" },
  { role: "user", text: "Go ahead and pause it. Also prepare a report.", time: "14:08" },
  { role: "agent", text: "Done. I've paused the \"Display network\" ad group. I'm now generating a detailed performance report with recommendations. It will be saved to your files when ready.", time: "14:09" },
]

function ChatMessages() {
  return (
    <div className="flex-1 overflow-y-auto px-4 py-4 space-y-4">
      {mockMessages.map((msg, i) => (
        <div key={i} className={cn("flex gap-3", msg.role === "user" ? "justify-end" : "justify-start")}>
          {msg.role === "agent" && (
            <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="" className="h-7 w-7 rounded-lg shrink-0 mt-0.5" />
          )}
          <div className={cn(
            "max-w-[70%] rounded-xl px-4 py-2.5 text-sm leading-relaxed",
            msg.role === "user" ? "bg-primary text-primary-foreground rounded-br-sm" : "bg-card border rounded-bl-sm"
          )}>
            <div className="whitespace-pre-line">{msg.text}</div>
            <div className={cn("text-[10px] mt-1.5 text-right", msg.role === "user" ? "text-primary-foreground/70" : "text-muted-foreground")}>{msg.time}</div>
          </div>
        </div>
      ))}
    </div>
  )
}

function ChatInput() {
  return (
    <div className="px-4 py-3 border-t bg-card shrink-0">
      <div className="flex items-center gap-2 bg-muted rounded-xl px-4 py-2.5">
        <Paperclip className="h-4 w-4 text-muted-foreground shrink-0" />
        <span className="text-sm text-muted-foreground flex-1">Message Pepicek...</span>
        <Send className="h-4 w-4 text-primary shrink-0" />
      </div>
    </div>
  )
}

function SessionSidebar({ width, onSelect }: { width: string; onSelect?: (id: string) => void }) {
  const [filter, setFilter] = useState<"mine" | "agent">("mine")
  return (
    <div className={cn("flex flex-col border-r shrink-0 overflow-hidden", width)}>
      <div className="px-3 pt-3 pb-2 shrink-0">
        <div className="flex items-center">
          <button onClick={() => setFilter("mine")} className={cn("flex-1 flex items-center justify-center gap-1.5 pb-2 text-[11px] font-medium border-b-2 transition-colors", filter === "mine" ? "border-foreground text-foreground" : "border-transparent text-muted-foreground")}>
            <User className="h-3 w-3" />Mine
          </button>
          <button onClick={() => setFilter("agent")} className={cn("flex-1 flex items-center justify-center gap-1.5 pb-2 text-[11px] font-medium border-b-2 transition-colors", filter === "agent" ? "border-foreground text-foreground" : "border-transparent text-muted-foreground")}>
            <Bot className="h-3 w-3" />Agent
          </button>
        </div>
      </div>
      <div className="px-3 pb-2 shrink-0">
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
          <input className="w-full h-7 rounded-md border bg-card pl-8 pr-3 text-xs outline-none placeholder:text-muted-foreground" placeholder="Search..." />
        </div>
      </div>
      <div className="flex-1 overflow-y-auto px-2 py-1 space-y-0.5">
        {mockSessions.map((s) => (
          <button key={s.id} onClick={() => onSelect?.(s.id)} className={cn("w-full text-left px-2.5 py-2 rounded-lg text-xs transition-colors flex items-center gap-2", s.id === "1" ? "bg-card text-foreground shadow-sm border" : "text-muted-foreground hover:bg-card/60")}>
            <div className="flex-1 min-w-0">
              <div className="truncate font-medium">{s.title}</div>
              <div className="text-[10px] text-muted-foreground mt-0.5">{s.msgs} msgs - {s.time}</div>
            </div>
            {s.active && <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse shrink-0" />}
          </button>
        ))}
      </div>
      <div className="p-2 border-t shrink-0">
        <button className="w-full flex items-center justify-center gap-1.5 h-8 rounded-lg bg-card text-xs font-medium text-muted-foreground hover:text-foreground border shadow-sm">
          <Plus className="h-3.5 w-3.5" />New Session
        </button>
      </div>
    </div>
  )
}

function RightPanel() {
  return (
    <div className="w-64 border-l flex flex-col shrink-0 overflow-hidden">
      <div className="flex shrink-0 border-b">
        {["Files", "Triggers", "Team", "Context"].map((t, i) => (
          <button key={t} className={cn("flex-1 py-2.5 text-[11px] font-medium transition-colors", i === 0 ? "text-primary border-b-2 border-primary" : "text-muted-foreground")}>{t}</button>
        ))}
      </div>
      <div className="flex-1 overflow-y-auto p-3 space-y-1.5">
        <div className="text-[11px] text-muted-foreground mb-1">/output/</div>
        {["report_q1.pdf", "keywords.csv", "ad_copy_v2.docx", "campaign_brief.md"].map((f) => (
          <div key={f} className="flex items-center gap-2 p-2 rounded-md hover:bg-accent/50 cursor-pointer text-xs">
            <div className="h-6 w-6 rounded bg-muted flex items-center justify-center text-[8px] font-bold text-muted-foreground">{f.split(".").pop()?.toUpperCase()}</div>
            <span className="truncate flex-1">{f}</span>
          </div>
        ))}
      </div>
      <div className="px-3 py-1.5 border-t text-[11px] text-muted-foreground">4 files</div>
    </div>
  )
}

function PlaceholderContent({ label }: { label: string }) {
  return (
    <div className="flex-1 flex items-center justify-center">
      <div className="text-sm text-muted-foreground">{label} content</div>
    </div>
  )
}

/* ============================================================
   VARIANTA A -- Vertikalni submenu vlevo (nahrazuje horni taby)
   Avatar + jmeno + status nahore, pod tim menu polozky.
   Obsah vpravo = sessions sidebar + chat + right panel.
   ============================================================ */
function VariantA() {
  const [activePage, setActivePage] = useState("sessions")

  return (
    <div className="w-full h-[700px] border rounded-xl overflow-hidden bg-background shadow-lg flex flex-col">
      <div className="flex flex-1 overflow-hidden">
        <MainSidebar />
        <div className="flex-1 flex flex-col overflow-hidden">
          <TopToolbar />
          {/* Content area with rounded top */}
          <div className="flex-1 flex overflow-hidden bg-background rounded-tl-2xl">
            {/* Left: Agent submenu */}
            <div className="w-44 bg-card border-r flex flex-col shrink-0">
              {/* Agent header */}
        <div className="flex items-center gap-3 px-3 py-3 border-b">
          <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="" className="h-9 w-9 rounded-xl shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="text-xs font-semibold truncate">Pepicek</div>
            <div className="text-[10px] text-muted-foreground">Google Ads</div>
            <span className="text-[9px] px-1.5 py-0.5 rounded-full bg-green-100 text-green-700 inline-block mt-0.5">RUNNING</span>
          </div>
        </div>

        {/* Navigation */}
        <div className="flex-1 overflow-y-auto py-1">
          {agentPages.map((page) => (
            <button
              key={page.id}
              onClick={() => setActivePage(page.id)}
              className={cn(
                "w-full flex items-center gap-2.5 px-3 py-2 text-xs font-medium transition-colors",
                page.id === activePage
                  ? "bg-accent text-primary border-r-2 border-primary"
                  : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
              )}
            >
              <page.icon className="h-3.5 w-3.5 shrink-0" />
              {page.label}
            </button>
          ))}
        </div>

        {/* Stop button */}
        <div className="p-2 border-t">
          <button className="w-full text-xs text-destructive font-medium py-1.5 rounded-md hover:bg-destructive/10 transition-colors">Stop Agent</button>
        </div>
      </div>

      {/* Right: Content area */}
      {activePage === "sessions" && (
        <div className="flex-1 flex overflow-hidden">
          <SessionSidebar width="w-48" />
          <div className="flex-1 flex flex-col overflow-hidden min-w-0">
            <ChatMessages />
            <ChatInput />
          </div>
          <RightPanel />
        </div>
      )}
      {activePage === "files" && (
        <div className="flex-1 p-6 overflow-y-auto">
          <h2 className="text-sm font-semibold mb-4">Agent Files</h2>
          <div className="grid grid-cols-2 gap-3">
            {["report_q1.pdf", "keywords.csv", "ad_copy_v2.docx", "campaign_brief.md", "analytics.xlsx", "competitors.json"].map((f) => (
              <div key={f} className="flex items-center gap-3 p-3 bg-card rounded-lg border hover:shadow-sm transition-shadow cursor-pointer">
                <div className="h-9 w-9 rounded-lg bg-muted flex items-center justify-center text-[10px] font-bold text-muted-foreground">{f.split(".").pop()?.toUpperCase()}</div>
                <div className="min-w-0 flex-1">
                  <div className="text-xs font-medium truncate">{f}</div>
                  <div className="text-[10px] text-muted-foreground">2.4 MB</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
      {activePage !== "sessions" && activePage !== "files" && <PlaceholderContent label={agentPages.find(p => p.id === activePage)?.label ?? ""} />}
          </div>
        </div>
      </div>
    </div>
  )
}

/* ============================================================
   VARIANTA B -- Collapsible icon rail + expanded panel
   Uzky icon rail (48px) s moznosti expandovat na 180px.
   Avatar vzdy videt. Obsah vyuziva maximum sirky.
   ============================================================ */
function VariantB() {
  const [activePage, setActivePage] = useState("sessions")
  const [sidebarExpanded, setSidebarExpanded] = useState(false)

  return (
    <div className="w-full h-[700px] border rounded-xl overflow-hidden bg-background shadow-lg flex flex-col">
      <div className="flex flex-1 overflow-hidden">
        <MainSidebar collapsed />
        <div className="flex-1 flex flex-col overflow-hidden">
          <TopToolbar />
          <div className="flex-1 flex overflow-hidden bg-background rounded-tl-2xl">
      {/* Left: Collapsible sidebar */}
      <div
        className={cn("bg-card border-r flex flex-col shrink-0 transition-all duration-200", sidebarExpanded ? "w-44" : "w-12")}
        onMouseEnter={() => setSidebarExpanded(true)}
        onMouseLeave={() => setSidebarExpanded(false)}
      >
        {/* Agent avatar */}
        <div className={cn("flex items-center border-b shrink-0 transition-all", sidebarExpanded ? "gap-2.5 px-3 py-3" : "justify-center py-3")}>
          <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="" className={cn("rounded-xl shrink-0 transition-all", sidebarExpanded ? "h-8 w-8" : "h-7 w-7")} />
          {sidebarExpanded && (
            <div className="min-w-0 flex-1">
              <div className="text-xs font-semibold truncate">Pepicek</div>
              <span className="text-[9px] px-1.5 py-0.5 rounded-full bg-green-100 text-green-700">RUNNING</span>
            </div>
          )}
        </div>

        {/* Navigation */}
        <div className="flex-1 overflow-y-auto py-1">
          {agentPages.map((page) => (
            <button
              key={page.id}
              onClick={() => setActivePage(page.id)}
              className={cn(
                "w-full flex items-center transition-colors",
                sidebarExpanded ? "gap-2.5 px-3 py-2 text-xs font-medium" : "justify-center py-2.5",
                page.id === activePage
                  ? "bg-accent text-primary"
                  : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
              )}
              title={!sidebarExpanded ? page.label : undefined}
            >
              <page.icon className="h-3.5 w-3.5 shrink-0" />
              {sidebarExpanded && <span className="truncate">{page.label}</span>}
            </button>
          ))}
        </div>
      </div>

      {/* Content */}
      {activePage === "sessions" && (
        <div className="flex-1 flex overflow-hidden">
          <SessionSidebar width="w-52" />
          <div className="flex-1 flex flex-col overflow-hidden min-w-0">
            <ChatMessages />
            <ChatInput />
          </div>
          <RightPanel />
        </div>
      )}
      {activePage === "files" && (
        <div className="flex-1 p-6 overflow-y-auto">
          <h2 className="text-sm font-semibold mb-4">Agent Files</h2>
          <div className="grid grid-cols-3 gap-3">
            {["report_q1.pdf", "keywords.csv", "ad_copy_v2.docx", "campaign_brief.md", "analytics.xlsx", "competitors.json"].map((f) => (
              <div key={f} className="flex items-center gap-3 p-3 bg-card rounded-lg border hover:shadow-sm transition-shadow cursor-pointer">
                <div className="h-9 w-9 rounded-lg bg-muted flex items-center justify-center text-[10px] font-bold text-muted-foreground">{f.split(".").pop()?.toUpperCase()}</div>
                <div className="min-w-0 flex-1">
                  <div className="text-xs font-medium truncate">{f}</div>
                  <div className="text-[10px] text-muted-foreground">2.4 MB</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
      {activePage !== "sessions" && activePage !== "files" && <PlaceholderContent label={agentPages.find(p => p.id === activePage)?.label ?? ""} />}
          </div>
        </div>
      </div>
    </div>
  )
}

/* ============================================================
   VARIANTA C -- Sessions sidebar integrovany s agent nav
   Levy panel = avatar nahore, pod nim pages, a pod "Sessions"
   se rovnou zobrazi session list. Vsechno v jednom sloupci.
   Zadne horizontalni taby. Maximum prostoru pro chat.
   ============================================================ */
function VariantC() {
  const [activePage, setActivePage] = useState("sessions")
  const [filter, setFilter] = useState<"mine" | "agent">("mine")

  return (
    <div className="w-full h-[700px] border rounded-xl overflow-hidden bg-background shadow-lg flex flex-col">
      <div className="flex flex-1 overflow-hidden">
        <MainSidebar />
        <div className="flex-1 flex flex-col overflow-hidden">
          <TopToolbar />
          <div className="flex-1 flex overflow-hidden bg-background rounded-tl-2xl">
      {/* Left: Combined agent nav + session list */}
      <div className="w-52 bg-card border-r flex flex-col shrink-0">
        {/* Agent header */}
        <div className="flex items-center gap-2.5 px-3 py-3 border-b">
          <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="" className="h-9 w-9 rounded-xl shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="text-xs font-semibold truncate">Pepicek</div>
            <div className="text-[10px] text-muted-foreground">Google Ads</div>
            <span className="text-[9px] px-1.5 py-0.5 rounded-full bg-green-100 text-green-700 inline-block mt-0.5">RUNNING</span>
          </div>
          <button className="h-6 w-6 flex items-center justify-center rounded hover:bg-accent shrink-0">
            <MoreVertical className="h-3.5 w-3.5 text-muted-foreground" />
          </button>
        </div>

        {/* Quick nav tabs -- compact horizontal */}
        <div className="flex overflow-x-auto scrollbar-none border-b px-1 py-1 gap-0.5 shrink-0">
          {agentPages.map((page) => (
            <button
              key={page.id}
              onClick={() => setActivePage(page.id)}
              className={cn(
                "shrink-0 px-2 py-1 rounded-md text-[10px] font-medium transition-colors",
                page.id === activePage
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:text-foreground hover:bg-accent"
              )}
            >
              {page.label}
            </button>
          ))}
        </div>

        {/* Dynamic content below nav */}
        {activePage === "sessions" && (
          <>
            <div className="px-3 pt-2 pb-1.5 shrink-0">
              <div className="flex items-center">
                <button onClick={() => setFilter("mine")} className={cn("flex-1 flex items-center justify-center gap-1 pb-1.5 text-[10px] font-medium border-b-2 transition-colors", filter === "mine" ? "border-foreground text-foreground" : "border-transparent text-muted-foreground")}>
                  <User className="h-3 w-3" />Mine
                </button>
                <button onClick={() => setFilter("agent")} className={cn("flex-1 flex items-center justify-center gap-1 pb-1.5 text-[10px] font-medium border-b-2 transition-colors", filter === "agent" ? "border-foreground text-foreground" : "border-transparent text-muted-foreground")}>
                  <Bot className="h-3 w-3" />Agent
                </button>
              </div>
            </div>
            <div className="px-2 pb-1.5 shrink-0">
              <div className="relative">
                <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
                <input className="w-full h-6 rounded border bg-background pl-7 pr-2 text-[10px] outline-none placeholder:text-muted-foreground" placeholder="Search..." />
              </div>
            </div>
            <div className="flex-1 overflow-y-auto px-1.5 py-0.5 space-y-0.5">
              {mockSessions.map((s) => (
                <button key={s.id} className={cn("w-full text-left px-2 py-1.5 rounded-md text-[11px] transition-colors", s.id === "1" ? "bg-accent text-foreground font-medium" : "text-muted-foreground hover:bg-accent/50")}>
                  <div className="truncate">{s.title}</div>
                  <div className="text-[9px] text-muted-foreground mt-0.5">{s.msgs} msgs - {s.time}</div>
                </button>
              ))}
            </div>
            <div className="p-1.5 border-t shrink-0">
              <button className="w-full flex items-center justify-center gap-1 h-7 rounded-md bg-background text-[10px] font-medium text-muted-foreground hover:text-foreground border shadow-sm">
                <Plus className="h-3 w-3" />New
              </button>
            </div>
          </>
        )}
        {activePage === "files" && (
          <div className="flex-1 overflow-y-auto p-2 space-y-1">
            {["report_q1.pdf", "keywords.csv", "ad_copy_v2.docx", "campaign_brief.md"].map((f) => (
              <div key={f} className="flex items-center gap-2 p-2 rounded-md hover:bg-accent/50 cursor-pointer">
                <div className="h-6 w-6 rounded bg-muted flex items-center justify-center text-[8px] font-bold text-muted-foreground">{f.split(".").pop()?.toUpperCase()}</div>
                <span className="text-[11px] truncate flex-1">{f}</span>
              </div>
            ))}
          </div>
        )}
        {activePage !== "sessions" && activePage !== "files" && (
          <div className="flex-1 flex items-center justify-center text-[11px] text-muted-foreground">{agentPages.find(p => p.id === activePage)?.label}</div>
        )}
      </div>

      {/* Main content */}
      <div className="flex-1 flex flex-col overflow-hidden min-w-0">
        {activePage === "sessions" && (
          <>
            {/* Session header */}
            <div className="flex items-center justify-between px-4 py-2 border-b bg-card shrink-0">
              <div className="flex items-center gap-2">
                <span className="h-2 w-2 rounded-full bg-green-500" />
                <span className="text-xs font-medium">Campaign performance review</span>
              </div>
              <span className="text-[10px] text-muted-foreground">12 msgs</span>
            </div>
            <ChatMessages />
            <ChatInput />
          </>
        )}
        {activePage === "files" && (
          <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">Select a file to preview</div>
        )}
        {activePage !== "sessions" && activePage !== "files" && (
          <PlaceholderContent label={agentPages.find(p => p.id === activePage)?.label ?? ""} />
        )}
      </div>

      {/* Right panel - only on sessions */}
      {activePage === "sessions" && <RightPanel />}
          </div>
        </div>
      </div>
    </div>
  )
}

/* ============================================================
   PREVIEW PAGE
   ============================================================ */
export default function PreviewAgentDesktopPage() {
  return (
    <div className="p-8 space-y-24 max-w-[1200px] mx-auto">
      <div className="mb-8">
        <h1 className="text-lg font-bold mb-1">Agent Desktop Layout -- Vertikalni submenu</h1>
        <p className="text-sm text-muted-foreground">Nahrazeni horizontalnich tabu vertikalnim submenu vlevo. Avatar + jmeno pod nim, pod tim navigace. Chat zabira maximum mista nahoru. Klikej na polozky v menu.</p>
      </div>

      <div>
        <h2 className="text-sm font-semibold mb-1">Varianta A -- Klasicky sidebar (176px)</h2>
        <p className="text-xs text-muted-foreground mb-6">Fixni levy sidebar s avatarem, jmenem, statusem a vertikalnimi polozkami. Aktivni polozka ma modry border vpravo. Chat ukazuje sessions sidebar + zpravy + pravy panel. Zadne horizontalni taby.</p>
        <VariantA />
      </div>

      <div>
        <h2 className="text-sm font-semibold mb-1">Varianta B -- Collapsible icon rail (48px → 176px on hover)</h2>
        <p className="text-xs text-muted-foreground mb-6">Uzky icon rail (48px) ktery se expandne na 176px pri hoveru. Maximalizuje prostor pro chat. Avatar vzdy videt. Najedete mysi na levy okraj pro rozsireni.</p>
        <VariantB />
      </div>

      <div>
        <h2 className="text-sm font-semibold mb-1">Varianta C -- Kombinovany panel (nav + sessions v jednom sloupci)</h2>
        <p className="text-xs text-muted-foreground mb-6">Jeden levy sloupec (208px) obsahuje avatar, horizontalni pill taby pro stranky, a pod nimi dynamicky obsah (session list, file list). Chat zabira maximum prostoru -- zadny extra sessions sidebar.</p>
        <VariantC />
      </div>
    </div>
  )
}
