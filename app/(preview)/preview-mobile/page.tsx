"use client"

import { useState } from "react"
import { cn } from "@/lib/utils"
import {
  Menu, X, Search, ChevronRight, Send,
  LayoutDashboard, Bot, Network, Zap, Key, Activity,
  Shield, Settings, Home, LayoutGrid,
  Paperclip, MoreVertical,
} from "lucide-react"

const navItems = [
  { label: "Dashboard", icon: LayoutDashboard, href: "/" },
  { label: "Crews", icon: Network, href: "/crews" },
  { label: "Agents", icon: Bot, href: "/agents", active: true },
  { label: "Skills", icon: Zap, href: "/skills" },
  { label: "Credentials", icon: Key, href: "/credentials" },
  { label: "Runs", icon: Activity, href: "/runs" },
  { label: "Audit Log", icon: Shield, href: "/audit" },
  { label: "Settings", icon: Settings, href: "/settings" },
]

const agentTabs = ["Overview", "Sessions", "Files", "Runs", "Logs", "Skills", "Credentials", "Settings"]

const mockSessions = [
  { id: "1", title: "Campaign performance review", status: "active", time: "2m ago", msgs: 12, unread: true },
  { id: "2", title: "Budget optimization Q1", status: "completed", time: "1h ago", msgs: 28, unread: false },
  { id: "3", title: "New keyword research", status: "active", time: "3h ago", msgs: 8, unread: true },
  { id: "4", title: "A/B test copy variants", status: "completed", time: "Yesterday", msgs: 15, unread: false },
  { id: "5", title: "Competitor analysis report", status: "completed", time: "2 days ago", msgs: 34, unread: false },
]

const mockMessages = [
  { role: "user", text: "Can you review the campaign performance for last week?", time: "14:02" },
  { role: "agent", text: "I'll analyze the Google Ads data for last week. Here's what I found:\n\n- Total spend: $2,450\n- Impressions: 125K (+12%)\n- CTR: 3.2% (above benchmark)\n- Conversions: 48 (+8%)\n- CPA: $51.04", time: "14:03" },
  { role: "user", text: "That looks good. What about the underperforming ad groups?", time: "14:05" },
  { role: "agent", text: "Three ad groups are below target:\n\n1. \"Brand awareness\" - CPA $89 (target $60)\n2. \"Retargeting cold\" - CTR 0.8%\n3. \"Display network\" - 0 conversions\n\nI recommend pausing Display network and reallocating budget to top performers.", time: "14:06" },
]

/* Shared: session chat content */
function SessionChatContent({ showSessionList, onBack }: { showSessionList?: boolean; onBack?: () => void }) {
  const [activeSession, setActiveSession] = useState("1")

  if (showSessionList) {
    return (
      <div className="flex-1 flex flex-col overflow-hidden bg-background">
        {/* Session list header */}
        <div className="px-3 py-2 flex items-center gap-2 border-b bg-card">
          <div className="flex bg-muted rounded-lg p-0.5 text-[10px] font-medium">
            <button className="px-2.5 py-1 rounded-md bg-card shadow-sm text-foreground">Mine</button>
            <button className="px-2.5 py-1 rounded-md text-muted-foreground">Agent</button>
          </div>
          <div className="flex-1" />
          <button className="text-[10px] text-primary font-medium">+ New</button>
        </div>
        {/* Search */}
        <div className="px-3 py-2 bg-card">
          <div className="flex items-center gap-2 px-2.5 py-1.5 bg-muted rounded-lg">
            <Search className="h-3 w-3 text-muted-foreground shrink-0" />
            <span className="text-[11px] text-muted-foreground">Search sessions...</span>
          </div>
        </div>
        {/* Session list */}
        <div className="flex-1 overflow-y-auto">
          {mockSessions.map((s) => (
            <button
              key={s.id}
              onClick={() => {
                setActiveSession(s.id)
                onBack?.()
              }}
              className={cn(
                "w-full text-left px-3 py-2.5 border-b border-border/50 transition-colors",
                s.id === activeSession ? "bg-card shadow-sm" : "hover:bg-card/50"
              )}
            >
              <div className="flex items-center gap-2 mb-0.5">
                <span className={cn("h-1.5 w-1.5 rounded-full shrink-0", s.status === "active" ? "bg-green-500" : "bg-muted-foreground/30")} />
                <span className="text-xs font-medium truncate flex-1">{s.title}</span>
                {s.unread && <span className="h-2 w-2 rounded-full bg-primary shrink-0" />}
              </div>
              <div className="flex items-center gap-2 pl-3.5">
                <span className="text-[10px] text-muted-foreground">{s.msgs} msgs</span>
                <span className="text-[10px] text-muted-foreground ml-auto">{s.time}</span>
              </div>
            </button>
          ))}
        </div>
      </div>
    )
  }

  return (
    <div className="flex-1 flex flex-col overflow-hidden bg-background">
      {/* Messages */}
      <div className="flex-1 overflow-y-auto px-3 py-3 space-y-3">
        {mockMessages.map((msg, i) => (
          <div key={i} className={cn("flex gap-2", msg.role === "user" ? "justify-end" : "justify-start")}>
            {msg.role === "agent" && (
              <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="" className="h-6 w-6 rounded-md shrink-0 mt-0.5" />
            )}
            <div className={cn(
              "max-w-[80%] rounded-xl px-3 py-2 text-xs leading-relaxed",
              msg.role === "user"
                ? "bg-primary text-primary-foreground rounded-br-sm"
                : "bg-card border rounded-bl-sm"
            )}>
              <div className="whitespace-pre-line">{msg.text}</div>
              <div className={cn("text-[9px] mt-1 text-right", msg.role === "user" ? "text-primary-foreground/70" : "text-muted-foreground")}>{msg.time}</div>
            </div>
          </div>
        ))}
      </div>
      {/* Input */}
      <div className="px-3 py-2 border-t bg-card">
        <div className="flex items-center gap-2 bg-muted rounded-xl px-3 py-2">
          <Paperclip className="h-4 w-4 text-muted-foreground shrink-0" />
          <span className="text-xs text-muted-foreground flex-1">Message Pepicek...</span>
          <Send className="h-4 w-4 text-primary shrink-0" />
        </div>
      </div>
    </div>
  )
}

/* Shared: navigation menu content (for bottom sheet and side panels) */
function NavMenuContent({ sections = true }: { sections?: boolean }) {
  if (sections) {
    return (
      <div className="py-2">
        <div className="px-3 py-1 text-[10px] uppercase tracking-wider font-semibold text-muted-foreground">Work</div>
        {navItems.slice(0, 3).map((item) => (
          <button key={item.label} className={cn("w-full flex items-center gap-3 px-4 py-2.5 text-sm transition-colors", item.active ? "bg-accent text-foreground font-medium" : "text-muted-foreground hover:text-foreground hover:bg-accent/50")}>
            <item.icon className="h-4 w-4" />
            {item.label}
          </button>
        ))}
        <div className="px-3 py-1 mt-2 text-[10px] uppercase tracking-wider font-semibold text-muted-foreground">Configure</div>
        {navItems.slice(3, 5).map((item) => (
          <button key={item.label} className="w-full flex items-center gap-3 px-4 py-2.5 text-sm text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors">
            <item.icon className="h-4 w-4" />
            {item.label}
          </button>
        ))}
        <div className="px-3 py-1 mt-2 text-[10px] uppercase tracking-wider font-semibold text-muted-foreground">Monitor</div>
        {navItems.slice(5, 7).map((item) => (
          <button key={item.label} className="w-full flex items-center gap-3 px-4 py-2.5 text-sm text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors">
            <item.icon className="h-4 w-4" />
            {item.label}
          </button>
        ))}
        <div className="px-3 py-1 mt-2 text-[10px] uppercase tracking-wider font-semibold text-muted-foreground">System</div>
        {navItems.slice(7).map((item) => (
          <button key={item.label} className="w-full flex items-center gap-3 px-4 py-2.5 text-sm text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors">
            <item.icon className="h-4 w-4" />
            {item.label}
          </button>
        ))}
      </div>
    )
  }
  return (
    <div className="py-2">
      {navItems.map((item) => (
        <button key={item.label} className={cn("w-full flex items-center gap-3 px-4 py-2.5 text-sm transition-colors", item.active ? "bg-accent text-foreground font-medium" : "text-muted-foreground hover:text-foreground hover:bg-accent/50")}>
          <item.icon className="h-4 w-4" />
          {item.label}
        </button>
      ))}
    </div>
  )
}

function UserFooter() {
  return (
    <div className="border-t p-4">
      <div className="flex items-center gap-3">
        <div className="h-8 w-8 rounded-full bg-primary text-[10px] font-bold text-primary-foreground flex items-center justify-center">PS</div>
        <div className="flex-1 min-w-0">
          <div className="text-xs font-medium">Pavel Srba</div>
          <div className="text-[10px] text-muted-foreground">pavel@unify.tech</div>
        </div>
      </div>
    </div>
  )
}

/* Shared: agent header + tabs */
function AgentHeaderBlock({ compact }: { compact?: boolean }) {
  return (
    <>
      <div className={cn("flex items-center gap-3 px-4 bg-card", compact ? "py-2" : "py-3")}>
        <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="Pepicek" className="h-8 w-8 rounded-lg shrink-0" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="text-sm font-semibold">Pepicek</span>
            <span className="text-[9px] px-1.5 py-0.5 rounded-full bg-green-100 text-green-700">RUNNING</span>
          </div>
          <span className="text-[11px] text-muted-foreground">Google Ads Specialist</span>
        </div>
        <button className="h-7 w-7 flex items-center justify-center rounded-md hover:bg-accent shrink-0">
          <MoreVertical className="h-3.5 w-3.5 text-muted-foreground" />
        </button>
      </div>
      <div className="flex overflow-x-auto scrollbar-none px-4 border-b border-t bg-card">
        {agentTabs.map((tab) => (
          <button key={tab} className={cn("shrink-0 px-3 py-2 text-xs font-medium border-b-2 transition-colors", tab === "Sessions" ? "border-primary text-primary" : "border-transparent text-muted-foreground")}>
            {tab}
          </button>
        ))}
      </div>
    </>
  )
}

/* Mobile view switcher for chat/sessions/files */
function MobileViewSwitcher({ active, onChange }: { active: string; onChange: (v: string) => void }) {
  const views = [
    { id: "chat", label: "Chat" },
    { id: "sessions", label: "Sessions" },
    { id: "files", label: "Files" },
  ]
  return (
    <div className="flex bg-muted/80 rounded-lg p-0.5 mx-3 mt-2 mb-1">
      {views.map((v) => (
        <button
          key={v.id}
          onClick={() => onChange(v.id)}
          className={cn(
            "flex-1 text-center py-1.5 text-[11px] font-medium rounded-md transition-all",
            v.id === active ? "bg-card shadow-sm text-foreground" : "text-muted-foreground"
          )}
        >
          {v.label}
        </button>
      ))}
    </div>
  )
}

/* ============================================================
   VARIANTA A -- Hamburger vpravo, slide-over zprava
   ============================================================ */
function VariantA() {
  const [menuOpen, setMenuOpen] = useState(false)
  const [mobileView, setMobileView] = useState("chat")

  return (
    <div className="relative w-[375px] h-[740px] border rounded-2xl overflow-hidden bg-background mx-auto shadow-xl flex flex-col">
      <div className="flex h-12 items-center justify-between px-3 bg-card border-b shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <div className="flex h-6 w-6 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground shrink-0">U</div>
          <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          <span className="text-xs text-muted-foreground">Agents</span>
          <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          <span className="text-xs font-semibold truncate">Pepicek</span>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
            <Search className="h-4 w-4 text-muted-foreground" />
          </button>
          <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent" onClick={() => setMenuOpen(!menuOpen)}>
            {menuOpen ? <X className="h-4 w-4" /> : <Menu className="h-4 w-4" />}
          </button>
        </div>
      </div>

      <AgentHeaderBlock />
      <MobileViewSwitcher active={mobileView} onChange={setMobileView} />

      {mobileView === "chat" && <SessionChatContent />}
      {mobileView === "sessions" && <SessionChatContent showSessionList onBack={() => setMobileView("chat")} />}
      {mobileView === "files" && (
        <div className="flex-1 p-4">
          <div className="space-y-2">
            {["report_q1.pdf", "keywords.csv", "ad_copy_v2.docx"].map((f) => (
              <div key={f} className="flex items-center gap-3 p-3 bg-card rounded-lg border">
                <div className="h-8 w-8 rounded-lg bg-muted flex items-center justify-center text-[10px] font-bold text-muted-foreground">{f.split(".").pop()?.toUpperCase()}</div>
                <div className="min-w-0 flex-1">
                  <div className="text-xs font-medium truncate">{f}</div>
                  <div className="text-[10px] text-muted-foreground">2.4 MB - Today</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {menuOpen && (
        <>
          <div className="absolute inset-0 bg-black/40 z-40" onClick={() => setMenuOpen(false)} />
          <div className="absolute top-0 right-0 bottom-0 w-72 bg-card z-50 shadow-2xl flex flex-col animate-in slide-in-from-right duration-200">
            <div className="flex items-center justify-between px-4 h-12 border-b shrink-0">
              <span className="text-sm font-semibold">Menu</span>
              <button onClick={() => setMenuOpen(false)} className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent"><X className="h-4 w-4" /></button>
            </div>
            <div className="flex-1 overflow-y-auto">
              <NavMenuContent sections={false} />
            </div>
            <UserFooter />
          </div>
        </>
      )}
    </div>
  )
}

/* ============================================================
   VARIANTA B -- Bottom tab bar + sheet zdola pro More
   ============================================================ */
function VariantB() {
  const [menuOpen, setMenuOpen] = useState(false)
  const [activeBottom, setActiveBottom] = useState("agents")
  const [mobileView, setMobileView] = useState("chat")

  const bottomTabs = [
    { id: "home", label: "Home", icon: Home },
    { id: "crews", label: "Crews", icon: Network },
    { id: "agents", label: "Agents", icon: Bot },
    { id: "runs", label: "Runs", icon: Activity },
    { id: "more", label: "More", icon: Menu },
  ]

  return (
    <div className="relative w-[375px] h-[740px] border rounded-2xl overflow-hidden bg-background mx-auto shadow-xl flex flex-col">
      <div className="flex h-12 items-center justify-between px-3 bg-card border-b shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <div className="flex h-6 w-6 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground shrink-0">U</div>
          <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          <span className="text-xs font-semibold truncate">Pepicek</span>
        </div>
        <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
          <Search className="h-4 w-4 text-muted-foreground" />
        </button>
      </div>

      <AgentHeaderBlock compact />
      <MobileViewSwitcher active={mobileView} onChange={setMobileView} />

      {mobileView === "chat" && <SessionChatContent />}
      {mobileView === "sessions" && <SessionChatContent showSessionList onBack={() => setMobileView("chat")} />}
      {mobileView === "files" && (
        <div className="flex-1 p-4">
          <div className="space-y-2">
            {["report_q1.pdf", "keywords.csv", "ad_copy_v2.docx"].map((f) => (
              <div key={f} className="flex items-center gap-3 p-3 bg-card rounded-lg border">
                <div className="h-8 w-8 rounded-lg bg-muted flex items-center justify-center text-[10px] font-bold text-muted-foreground">{f.split(".").pop()?.toUpperCase()}</div>
                <div className="min-w-0 flex-1">
                  <div className="text-xs font-medium truncate">{f}</div>
                  <div className="text-[10px] text-muted-foreground">2.4 MB - Today</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Bottom tab bar */}
      <div className="flex items-center border-t bg-card shrink-0">
        {bottomTabs.map((tab) => (
          <button key={tab.id} onClick={() => { if (tab.id === "more") setMenuOpen(true); else setActiveBottom(tab.id) }}
            className={cn("flex-1 flex flex-col items-center gap-0.5 py-2 text-[10px] font-medium transition-colors", tab.id === activeBottom ? "text-primary" : "text-muted-foreground")}>
            <tab.icon className="h-5 w-5" />
            {tab.label}
          </button>
        ))}
      </div>

      {menuOpen && (
        <>
          <div className="absolute inset-0 bg-black/40 z-40" onClick={() => setMenuOpen(false)} />
          <div className="absolute bottom-0 left-0 right-0 bg-card z-50 rounded-t-2xl shadow-2xl animate-in slide-in-from-bottom duration-200">
            <div className="w-12 h-1 rounded-full bg-border mx-auto mt-3 mb-2" />
            <div className="px-2 pb-4 max-h-[60vh] overflow-y-auto">
              {navItems.filter((n) => !["Dashboard", "Agents", "Crews", "Runs"].includes(n.label)).map((item) => (
                <button key={item.label} className="w-full flex items-center gap-3 px-4 py-3 text-sm text-muted-foreground hover:text-foreground hover:bg-accent/50 rounded-lg transition-colors">
                  <item.icon className="h-4 w-4" />
                  {item.label}
                </button>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  )
}

/* ============================================================
   VARIANTA C -- Hamburger vlevo, slide-over zleva
   ============================================================ */
function VariantC() {
  const [menuOpen, setMenuOpen] = useState(false)
  const [mobileView, setMobileView] = useState("chat")

  return (
    <div className="relative w-[375px] h-[740px] border rounded-2xl overflow-hidden bg-background mx-auto shadow-xl flex flex-col">
      <div className="flex h-12 items-center justify-between px-2 bg-card border-b shrink-0">
        <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent shrink-0" onClick={() => setMenuOpen(!menuOpen)}>
          <Menu className="h-4 w-4" />
        </button>
        <div className="flex items-center gap-1.5 min-w-0 mx-2 flex-1 justify-center">
          <span className="text-xs text-muted-foreground truncate">Agents</span>
          <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          <span className="text-xs font-semibold truncate">Pepicek</span>
        </div>
        <div className="flex items-center gap-0.5 shrink-0">
          <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
            <Search className="h-4 w-4 text-muted-foreground" />
          </button>
          <div className="h-6 w-6 rounded-full bg-primary text-[8px] font-bold text-primary-foreground flex items-center justify-center">PS</div>
        </div>
      </div>

      <AgentHeaderBlock compact />
      <MobileViewSwitcher active={mobileView} onChange={setMobileView} />

      {mobileView === "chat" && <SessionChatContent />}
      {mobileView === "sessions" && <SessionChatContent showSessionList onBack={() => setMobileView("chat")} />}
      {mobileView === "files" && (
        <div className="flex-1 p-4">
          <div className="space-y-2">
            {["report_q1.pdf", "keywords.csv", "ad_copy_v2.docx"].map((f) => (
              <div key={f} className="flex items-center gap-3 p-3 bg-card rounded-lg border">
                <div className="h-8 w-8 rounded-lg bg-muted flex items-center justify-center text-[10px] font-bold text-muted-foreground">{f.split(".").pop()?.toUpperCase()}</div>
                <div className="min-w-0 flex-1">
                  <div className="text-xs font-medium truncate">{f}</div>
                  <div className="text-[10px] text-muted-foreground">2.4 MB - Today</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {menuOpen && (
        <>
          <div className="absolute inset-0 bg-black/40 z-40" onClick={() => setMenuOpen(false)} />
          <div className="absolute top-0 left-0 bottom-0 w-72 bg-card z-50 shadow-2xl flex flex-col animate-in slide-in-from-left duration-200">
            <div className="flex items-center gap-2 px-4 h-12 border-b shrink-0">
              <div className="flex h-6 w-6 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground">U</div>
              <span className="text-sm font-semibold flex-1">Crewship</span>
              <button onClick={() => setMenuOpen(false)} className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent"><X className="h-4 w-4" /></button>
            </div>
            <div className="flex-1 overflow-y-auto">
              <NavMenuContent sections />
            </div>
            <UserFooter />
          </div>
        </>
      )}
    </div>
  )
}

/* ============================================================
   VARIANTA D -- Supabase: hamburger vpravo, menu vyjede ZDOLA
   (bottom sheet fullscreen s animaci slide-up)
   ============================================================ */
function VariantD() {
  const [menuOpen, setMenuOpen] = useState(false)
  const [mobileView, setMobileView] = useState("chat")

  return (
    <div className="relative w-[375px] h-[740px] border rounded-2xl overflow-hidden bg-background mx-auto shadow-xl flex flex-col">
      {/* Top bar */}
      <div className="flex h-12 items-center justify-between px-3 bg-card border-b shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <div className="flex h-6 w-6 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground shrink-0">U</div>
          <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          <span className="text-xs text-muted-foreground">Agents</span>
          <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          <span className="text-xs font-semibold truncate">Pepicek</span>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
            <Search className="h-4 w-4 text-muted-foreground" />
          </button>
          <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent" onClick={() => setMenuOpen(!menuOpen)}>
            {menuOpen ? <X className="h-4 w-4" /> : <Menu className="h-4 w-4" />}
          </button>
        </div>
      </div>

      <AgentHeaderBlock />
      <MobileViewSwitcher active={mobileView} onChange={setMobileView} />

      {mobileView === "chat" && <SessionChatContent />}
      {mobileView === "sessions" && <SessionChatContent showSessionList onBack={() => setMobileView("chat")} />}
      {mobileView === "files" && (
        <div className="flex-1 p-4">
          <div className="space-y-2">
            {["report_q1.pdf", "keywords.csv", "ad_copy_v2.docx"].map((f) => (
              <div key={f} className="flex items-center gap-3 p-3 bg-card rounded-lg border">
                <div className="h-8 w-8 rounded-lg bg-muted flex items-center justify-center text-[10px] font-bold text-muted-foreground">{f.split(".").pop()?.toUpperCase()}</div>
                <div className="min-w-0 flex-1">
                  <div className="text-xs font-medium truncate">{f}</div>
                  <div className="text-[10px] text-muted-foreground">2.4 MB - Today</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Bottom sheet menu (Supabase style - slides up from bottom) */}
      {menuOpen && (
        <>
          <div className="absolute inset-0 bg-black/50 z-40" onClick={() => setMenuOpen(false)} />
          <div className="absolute bottom-0 left-0 right-0 bg-card z-50 rounded-t-2xl shadow-2xl flex flex-col animate-in slide-in-from-bottom duration-300 max-h-[85%]">
            {/* Drag handle */}
            <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-1 shrink-0" />

            {/* Header */}
            <div className="flex items-center justify-between px-4 py-2 border-b shrink-0">
              <div className="flex items-center gap-2">
                <div className="flex h-6 w-6 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground">U</div>
                <span className="text-sm font-semibold">Crewship</span>
              </div>
              <button onClick={() => setMenuOpen(false)} className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
                <X className="h-4 w-4" />
              </button>
            </div>

            {/* Nav with sections */}
            <div className="flex-1 overflow-y-auto">
              <NavMenuContent sections />
            </div>

            {/* User */}
            <UserFooter />
          </div>
        </>
      )}
    </div>
  )
}

/* ============================================================
   VARIANTA E -- Hamburger v baru Chat/Sessions/Files (vlevo) +
   agent podstranky ze spodu (bottom sheet) +
   hlavni navigace pres pravy hamburger taky ze spodu +
   breadcrumb: Crews > Pepicek
   ============================================================ */
function VariantE() {
  const [mainMenuOpen, setMainMenuOpen] = useState(false)
  const [agentMenuOpen, setAgentMenuOpen] = useState(false)
  const [mobileView, setMobileView] = useState("chat")

  const agentSubPages = [
    { label: "Overview", icon: LayoutDashboard },
    { label: "Sessions", icon: Activity, active: true },
    { label: "Files", icon: LayoutDashboard },
    { label: "Runs", icon: Activity },
    { label: "Logs", icon: Shield },
    { label: "Skills", icon: Zap },
    { label: "Credentials", icon: Key },
    { label: "Settings", icon: Settings },
    { label: "Debug", icon: Search },
    { label: "History", icon: Activity },
  ]

  const views = [
    { id: "chat", label: "Chat" },
    { id: "sessions", label: "Sessions" },
    { id: "files", label: "Files" },
  ]

  return (
    <div className="relative w-[375px] h-[740px] border rounded-2xl overflow-hidden bg-background mx-auto shadow-xl flex flex-col">
      {/* Top bar */}
      <div className="flex h-12 items-center justify-between px-2 bg-card border-b shrink-0">
        <div className="flex items-center gap-1.5 min-w-0">
          <div className="flex h-6 w-6 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground shrink-0">U</div>
          <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          <span className="text-xs text-muted-foreground truncate">Crews</span>
          <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          <span className="text-xs font-semibold truncate">Pepicek</span>
        </div>
        <div className="flex items-center gap-0.5 shrink-0">
          <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
            <Search className="h-4 w-4 text-muted-foreground" />
          </button>
          <button
            className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent"
            onClick={() => setMainMenuOpen(!mainMenuOpen)}
          >
            <Menu className="h-4 w-4 text-muted-foreground" />
          </button>
        </div>
      </div>

      {/* Agent header with avatar */}
      <div className="flex items-center gap-3 px-4 py-2.5 bg-card">
        <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="Pepicek" className="h-8 w-8 rounded-lg shrink-0" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="text-sm font-semibold">Pepicek</span>
            <span className="text-[9px] px-1.5 py-0.5 rounded-full bg-green-100 text-green-700">RUNNING</span>
          </div>
          <span className="text-[11px] text-muted-foreground">Google Ads Specialist</span>
        </div>
      </div>

      {/* Bar: [hamburger] Chat  Sessions  Files */}
      <div className="flex items-center bg-card border-t border-b shrink-0">
        <button
          className="h-10 w-10 flex items-center justify-center hover:bg-accent shrink-0 border-r"
          onClick={() => setAgentMenuOpen(true)}
        >
          <LayoutGrid className="h-4 w-4 text-muted-foreground" />
        </button>
        {views.map((v) => (
          <button
            key={v.id}
            onClick={() => setMobileView(v.id)}
            className={cn(
              "flex-1 text-center pt-2.5 pb-2 text-xs font-medium border-b-2 mb-[-1px] transition-colors",
              v.id === mobileView
                ? "border-primary text-primary"
                : "border-transparent text-muted-foreground"
            )}
          >
            {v.label}
          </button>
        ))}
      </div>

      {/* Content */}
      {mobileView === "chat" && <SessionChatContent />}
      {mobileView === "sessions" && <SessionChatContent showSessionList onBack={() => setMobileView("chat")} />}
      {mobileView === "files" && (
        <div className="flex-1 p-4 overflow-y-auto">
          <div className="space-y-2">
            {["report_q1.pdf", "keywords.csv", "ad_copy_v2.docx", "campaign_brief.md", "analytics_export.xlsx"].map((f) => (
              <div key={f} className="flex items-center gap-3 p-3 bg-card rounded-lg border">
                <div className="h-8 w-8 rounded-lg bg-muted flex items-center justify-center text-[10px] font-bold text-muted-foreground">{f.split(".").pop()?.toUpperCase()}</div>
                <div className="min-w-0 flex-1">
                  <div className="text-xs font-medium truncate">{f}</div>
                  <div className="text-[10px] text-muted-foreground">2.4 MB - Today</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* BOTTOM sheet: Agent sub-pages (from hamburger in the bar) */}
      {agentMenuOpen && (
        <>
          <div className="absolute inset-0 bg-black/50 z-40" onClick={() => setAgentMenuOpen(false)} />
          <div className="absolute bottom-0 left-0 right-0 bg-card z-50 rounded-t-2xl shadow-2xl flex flex-col animate-in slide-in-from-bottom duration-300 max-h-[85%]">
            <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-1 shrink-0" />
            <div className="flex items-center gap-2.5 px-4 py-2.5 border-b shrink-0">
              <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="" className="h-7 w-7 rounded-lg shrink-0" />
              <div className="min-w-0 flex-1">
                <div className="text-xs font-semibold truncate">Pepicek</div>
                <div className="text-[10px] text-muted-foreground">Google Ads Specialist</div>
              </div>
              <button onClick={() => setAgentMenuOpen(false)} className="h-7 w-7 flex items-center justify-center rounded-md hover:bg-accent shrink-0">
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
            <div className="flex-1 overflow-y-auto py-1">
              {agentSubPages.map((page) => (
                <button
                  key={page.label}
                  onClick={() => setAgentMenuOpen(false)}
                  className={cn(
                    "w-full flex items-center gap-3 px-4 py-2.5 text-sm transition-colors",
                    page.active
                      ? "bg-accent text-foreground font-medium"
                      : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
                  )}
                >
                  <page.icon className="h-4 w-4" />
                  {page.label}
                </button>
              ))}
            </div>
            <div className="border-t px-4 py-3">
              <button className="w-full flex items-center justify-center gap-2 px-3 py-2 bg-destructive/10 text-destructive text-xs font-medium rounded-lg hover:bg-destructive/20 transition-colors">
                Stop Agent
              </button>
            </div>
          </div>
        </>
      )}

      {/* BOTTOM sheet: Main navigation (from top-right dots) */}
      {mainMenuOpen && (
        <>
          <div className="absolute inset-0 bg-black/50 z-40" onClick={() => setMainMenuOpen(false)} />
          <div className="absolute bottom-0 left-0 right-0 bg-card z-50 rounded-t-2xl shadow-2xl flex flex-col animate-in slide-in-from-bottom duration-300 max-h-[85%]">
            <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-1 shrink-0" />
            <div className="flex items-center justify-between px-4 py-2 border-b shrink-0">
              <div className="flex items-center gap-2">
                <div className="flex h-6 w-6 items-center justify-center rounded bg-primary text-[8px] font-bold text-primary-foreground">U</div>
                <span className="text-sm font-semibold">Crewship</span>
              </div>
              <button onClick={() => setMainMenuOpen(false)} className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
                <X className="h-4 w-4" />
              </button>
            </div>
            <div className="flex-1 overflow-y-auto">
              <NavMenuContent sections />
            </div>
            <UserFooter />
          </div>
        </>
      )}
    </div>
  )
}

/* ============================================================
   PREVIEW PAGE
   ============================================================ */
export default function PreviewMobilePage() {
  return (
    <div className="p-6 space-y-20 max-w-5xl mx-auto">
      <div className="mb-8">
        <h1 className="text-lg font-bold mb-1">Mobile Navigation Preview</h1>
        <p className="text-sm text-muted-foreground">Klikej na hamburger ikony a prepinac Chat/Sessions/Files pro interakci. Vsechny varianty ukazuji realisticky sessions chat obsah.</p>
      </div>

      {/* Varianta E first -- recommended */}
      <div className="mb-12">
        <div className="inline-block px-2 py-0.5 bg-primary text-primary-foreground text-[10px] font-bold rounded mb-2">DOPORUCENA</div>
        <h2 className="text-sm font-semibold mb-1">Varianta E -- Levy hamburger (agent menu) + pravy hamburger (hlavni nav ze spoda) + Chat/Sessions/Files prepinac</h2>
        <p className="text-xs text-muted-foreground mb-6">Levy hamburger otevre agent podstranky zleva (Overview, Sessions, Files, Runs, Logs, Skills, Credentials, Settings, Debug, History). Pravy hamburger otevre hlavni navigaci ze spoda (Supabase bottom sheet). Breadcrumb: Crews &gt; Pepicek.</p>
        <VariantE />
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-16">
        <div>
          <h2 className="text-sm font-semibold mb-1">Varianta A -- Hamburger vpravo, slide-over zprava</h2>
          <p className="text-xs text-muted-foreground mb-6">Breadcrumb vlevo, hamburger vpravo. Menu vyjede zprava pres obsah.</p>
          <VariantA />
        </div>

        <div>
          <h2 className="text-sm font-semibold mb-1">Varianta B -- Bottom tab bar + sheet zdola</h2>
          <p className="text-xs text-muted-foreground mb-6">iOS styl: hlavni navigace dole (Home, Crews, Agents, Runs, More). More otevre sheet zdola.</p>
          <VariantB />
        </div>

        <div>
          <h2 className="text-sm font-semibold mb-1">Varianta C -- Hamburger vlevo, slide-over zleva</h2>
          <p className="text-xs text-muted-foreground mb-6">Supabase hybrid: hamburger vlevo otevre plne menu zleva se sekcemi. Breadcrumb uprostred.</p>
          <VariantC />
        </div>

        <div>
          <h2 className="text-sm font-semibold mb-1">Varianta D -- Hamburger vpravo, menu ZDOLA (Supabase)</h2>
          <p className="text-xs text-muted-foreground mb-6">Jako Supabase: hamburger vpravo nahore, menu vyjede zespoda nahoru jako bottom sheet. Se sekcemi a profilem.</p>
          <VariantD />
        </div>
      </div>
    </div>
  )
}
