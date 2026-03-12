"use client"

import { useState } from "react"
import { cn } from "@/lib/utils"
import {
  Menu, Search, ChevronRight, Send,
  LayoutDashboard, Bot, Network, Zap, Key, Activity,
  Settings, Bug, History, MessageSquare,
  FolderOpen, ScrollText, Paperclip, ChevronLeft,
} from "lucide-react"

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
]

const mockMessages = [
  { role: "user", text: "Can you review the campaign performance for last week?", time: "14:02" },
  { role: "agent", text: "I'll analyze the Google Ads data for last week. Here's what I found:\n\n- Total spend: $2,450\n- Impressions: 125K (+12%)\n- CTR: 3.2% (above benchmark)\n- Conversions: 48 (+8%)", time: "14:03" },
  { role: "user", text: "What about the underperforming ad groups?", time: "14:05" },
  { role: "agent", text: "Three ad groups are below target:\n\n1. \"Brand awareness\" - CPA $89\n2. \"Retargeting cold\" - CTR 0.8%\n3. \"Display network\" - 0 conversions\n\nI recommend pausing Display network.", time: "14:06" },
]

function ChatContent() {
  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      <div className="flex-1 overflow-y-auto px-3 py-3 space-y-3">
        {mockMessages.map((msg, i) => (
          <div key={i} className={cn("flex gap-2", msg.role === "user" ? "justify-end" : "justify-start")}>
            {msg.role === "agent" && (
              <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="" className="h-6 w-6 rounded-md shrink-0 mt-0.5" />
            )}
            <div className={cn(
              "max-w-[80%] rounded-xl px-3 py-2 text-xs leading-relaxed",
              msg.role === "user" ? "bg-primary text-primary-foreground rounded-br-sm" : "bg-card border rounded-bl-sm"
            )}>
              <div className="whitespace-pre-line">{msg.text}</div>
              <div className={cn("text-[9px] mt-1 text-right", msg.role === "user" ? "text-primary-foreground/70" : "text-muted-foreground")}>{msg.time}</div>
            </div>
          </div>
        ))}
      </div>
      <div className="px-3 py-2 border-t bg-card shrink-0">
        <div className="flex items-center gap-2 bg-muted rounded-xl px-3 py-2">
          <Paperclip className="h-4 w-4 text-muted-foreground shrink-0" />
          <span className="text-xs text-muted-foreground flex-1">Message Pepicek...</span>
          <Send className="h-4 w-4 text-primary shrink-0" />
        </div>
      </div>
    </div>
  )
}

function SessionsList({ onSelect }: { onSelect: () => void }) {
  return (
    <div className="flex-1 overflow-y-auto">
      {mockSessions.map((s) => (
        <button
          key={s.id}
          onClick={onSelect}
          className={cn(
            "w-full text-left px-3 py-2.5 border-b border-border/50 transition-colors",
            s.id === "1" ? "bg-card shadow-sm" : "hover:bg-card/50"
          )}
        >
          <div className="flex items-center gap-2 mb-0.5">
            <span className={cn("h-1.5 w-1.5 rounded-full shrink-0", s.active ? "bg-green-500" : "bg-muted-foreground/30")} />
            <span className="text-xs font-medium truncate flex-1">{s.title}</span>
          </div>
          <div className="flex items-center gap-2 pl-3.5">
            <span className="text-[10px] text-muted-foreground">{s.msgs} msgs</span>
            <span className="text-[10px] text-muted-foreground ml-auto">{s.time}</span>
          </div>
        </button>
      ))}
    </div>
  )
}

/* ============================================================
   VARIANTA A -- Collapsible icon sidebar vlevo
   Ikonky vertikalne, po kliknuti se expandne nazev + obsah doprava
   ============================================================ */
function VariantA() {
  const [activePage, setActivePage] = useState("sessions")
  const [expanded, setExpanded] = useState(false)
  const [mainMenuOpen, setMainMenuOpen] = useState(false)

  return (
    <div className="relative w-[375px] h-[740px] border rounded-2xl overflow-hidden bg-background mx-auto shadow-xl flex flex-col">
      {/* Top bar - minimal */}
      <div className="flex h-11 items-center justify-between px-3 bg-card border-b shrink-0">
        <div className="flex items-center gap-1.5 min-w-0">
          <span className="text-xs text-muted-foreground">Crews</span>
          <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          <span className="text-xs font-semibold truncate">Pepicek</span>
        </div>
        <div className="flex items-center gap-0.5 shrink-0">
          <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
            <Search className="h-4 w-4 text-muted-foreground" />
          </button>
          <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent" onClick={() => setMainMenuOpen(true)}>
            <Menu className="h-4 w-4 text-muted-foreground" />
          </button>
        </div>
      </div>

      {/* Main area: icon sidebar + content */}
      <div className="flex flex-1 overflow-hidden">
        {/* Icon sidebar */}
        <div className="w-12 bg-card border-r flex flex-col shrink-0 overflow-y-auto">
          {/* Agent avatar */}
          <div className="flex flex-col items-center pt-2 pb-1 border-b">
            <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="" className="h-8 w-8 rounded-lg" />
            <span className="text-[8px] text-muted-foreground mt-0.5 truncate w-full text-center">Pepicek</span>
          </div>
          {/* Page icons */}
          {agentPages.map((page) => (
            <button
              key={page.id}
              onClick={() => { setActivePage(page.id); setExpanded(true) }}
              className={cn(
                "flex flex-col items-center py-2 gap-0.5 transition-colors",
                page.id === activePage ? "text-primary bg-accent" : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
              )}
            >
              <page.icon className="h-4 w-4" />
              <span className="text-[7px] font-medium leading-tight">{page.label}</span>
            </button>
          ))}
        </div>

        {/* Content area */}
        <div className="flex-1 flex flex-col overflow-hidden">
          {activePage === "sessions" && !expanded && <ChatContent />}
          {activePage === "sessions" && expanded && (
            <div className="flex-1 flex flex-col overflow-hidden">
              <div className="flex items-center justify-between px-3 py-2 border-b bg-card shrink-0">
                <span className="text-xs font-semibold">Sessions</span>
                <button onClick={() => setExpanded(false)} className="text-[10px] text-primary font-medium">Back to chat</button>
              </div>
              <SessionsList onSelect={() => setExpanded(false)} />
            </div>
          )}
          {activePage === "overview" && (
            <div className="flex-1 p-4">
              <div className="text-xs text-muted-foreground text-center pt-12">Overview content</div>
            </div>
          )}
          {activePage !== "sessions" && activePage !== "overview" && (
            <div className="flex-1 p-4">
              <div className="text-xs text-muted-foreground text-center pt-12">{agentPages.find(p => p.id === activePage)?.label} content</div>
            </div>
          )}
        </div>
      </div>

      {/* Main nav sheet */}
      {mainMenuOpen && (
        <>
          <div className="absolute inset-0 bg-black/50 z-40" onClick={() => setMainMenuOpen(false)} />
          <div className="absolute bottom-0 left-0 right-0 bg-card z-50 rounded-t-2xl shadow-2xl flex flex-col animate-in slide-in-from-bottom duration-300 max-h-[70%]">
            <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-2" />
            <div className="px-4 py-2 border-b text-sm font-semibold">Crewship</div>
            <div className="flex-1 overflow-y-auto py-2 px-2">
              {[{ l: "Dashboard", i: LayoutDashboard }, { l: "Crews", i: Network }, { l: "Agents", i: Bot }, { l: "Skills", i: Zap }, { l: "Credentials", i: Key }, { l: "Runs", i: Activity }, { l: "Settings", i: Settings }].map((n) => (
                <button key={n.l} className="w-full flex items-center gap-3 px-3 py-2.5 text-sm text-muted-foreground hover:bg-accent/50 rounded-lg"><n.i className="h-4 w-4" />{n.l}</button>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  )
}

/* ============================================================
   VARIANTA B -- Sliding panels (Telegram/WhatsApp styl)
   Avatar + vertikalni menu je prvni "screen",
   po kliknuti se obsah slideuje doprava jako dalsi screen
   ============================================================ */
function VariantB() {
  const [activePage, setActivePage] = useState<string | null>(null)
  const [mainMenuOpen, setMainMenuOpen] = useState(false)

  return (
    <div className="relative w-[375px] h-[740px] border rounded-2xl overflow-hidden bg-background mx-auto shadow-xl flex flex-col">
      {/* Top bar */}
      <div className="flex h-11 items-center justify-between px-3 bg-card border-b shrink-0">
        {activePage ? (
          <>
            <button onClick={() => setActivePage(null)} className="flex items-center gap-1 text-primary">
              <ChevronLeft className="h-4 w-4" />
              <span className="text-xs font-medium">Back</span>
            </button>
            <span className="text-xs font-semibold">{agentPages.find(p => p.id === activePage)?.label}</span>
            <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent" onClick={() => setMainMenuOpen(true)}>
              <Menu className="h-4 w-4 text-muted-foreground" />
            </button>
          </>
        ) : (
          <>
            <div className="flex items-center gap-1.5">
              <span className="text-xs text-muted-foreground">Crews</span>
              <ChevronRight className="h-3 w-3 text-muted-foreground" />
              <span className="text-xs font-semibold">Pepicek</span>
            </div>
            <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent" onClick={() => setMainMenuOpen(true)}>
              <Menu className="h-4 w-4 text-muted-foreground" />
            </button>
          </>
        )}
      </div>

      {/* Content: either agent menu or page content */}
      {!activePage ? (
        <div className="flex-1 overflow-y-auto">
          {/* Agent card */}
          <div className="flex items-center gap-3 px-4 py-4 bg-card border-b">
            <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="" className="h-12 w-12 rounded-xl shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5">
                <span className="text-sm font-semibold">Pepicek</span>
                <span className="text-[9px] px-1.5 py-0.5 rounded-full bg-green-100 text-green-700">RUNNING</span>
              </div>
              <span className="text-xs text-muted-foreground">Google Ads Specialist</span>
            </div>
          </div>

          {/* Vertical page list */}
          <div className="py-1">
            {agentPages.map((page) => (
              <button
                key={page.id}
                onClick={() => setActivePage(page.id)}
                className="w-full flex items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-accent/50 active:bg-accent"
              >
                <page.icon className="h-4.5 w-4.5 text-muted-foreground" />
                <span className="flex-1 text-left font-medium">{page.label}</span>
                <ChevronRight className="h-4 w-4 text-muted-foreground/50" />
              </button>
            ))}
          </div>
        </div>
      ) : (
        <div className="flex-1 flex flex-col overflow-hidden">
          {activePage === "sessions" && (
            <>
              {/* Inline tabs: Mine / Agent */}
              <div className="flex px-3 pt-2 pb-1 gap-2 shrink-0">
                <button className="px-3 py-1.5 text-xs font-medium bg-card shadow-sm border rounded-lg">Mine</button>
                <button className="px-3 py-1.5 text-xs font-medium text-muted-foreground rounded-lg">Agent</button>
                <div className="flex-1" />
                <button className="text-xs text-primary font-medium">+ New</button>
              </div>
              <ChatContent />
            </>
          )}
          {activePage !== "sessions" && (
            <div className="flex-1 p-4">
              <div className="text-xs text-muted-foreground text-center pt-12">{agentPages.find(p => p.id === activePage)?.label} content</div>
            </div>
          )}
        </div>
      )}

      {/* Main nav bottom sheet */}
      {mainMenuOpen && (
        <>
          <div className="absolute inset-0 bg-black/50 z-40" onClick={() => setMainMenuOpen(false)} />
          <div className="absolute bottom-0 left-0 right-0 bg-card z-50 rounded-t-2xl shadow-2xl flex flex-col animate-in slide-in-from-bottom duration-300 max-h-[70%]">
            <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-2" />
            <div className="px-4 py-2 border-b text-sm font-semibold">Crewship</div>
            <div className="flex-1 overflow-y-auto py-2 px-2">
              {[{ l: "Dashboard", i: LayoutDashboard }, { l: "Crews", i: Network }, { l: "Agents", i: Bot }, { l: "Skills", i: Zap }, { l: "Credentials", i: Key }, { l: "Runs", i: Activity }, { l: "Settings", i: Settings }].map((n) => (
                <button key={n.l} className="w-full flex items-center gap-3 px-3 py-2.5 text-sm text-muted-foreground hover:bg-accent/50 rounded-lg"><n.i className="h-4 w-4" />{n.l}</button>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  )
}

/* ============================================================
   VARIANTA C -- Compact sidebar s avatarem + scrollable menu
   Sidebar je vzdy videt (uzka), ukazuje avatar nahore
   a pod nim vertikalni menu. Obsah je vedle.
   Klik na polozku = obsah se meni vpravo.
   Na uzkych telefonech sidebar minimalizovan na ikony.
   ============================================================ */
function VariantC() {
  const [activePage, setActivePage] = useState("sessions")
  const [sessionView, setSessionView] = useState<"list" | "chat">("chat")
  const [mainMenuOpen, setMainMenuOpen] = useState(false)

  return (
    <div className="relative w-[375px] h-[740px] border rounded-2xl overflow-hidden bg-background mx-auto shadow-xl flex flex-col">
      {/* Top bar */}
      <div className="flex h-11 items-center justify-between px-3 bg-card border-b shrink-0">
        <div className="flex items-center gap-1.5 min-w-0">
          <span className="text-xs text-muted-foreground">Crews</span>
          <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          <span className="text-xs font-semibold truncate">Pepicek</span>
        </div>
        <div className="flex items-center gap-0.5 shrink-0">
          <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent">
            <Search className="h-4 w-4 text-muted-foreground" />
          </button>
          <button className="h-8 w-8 flex items-center justify-center rounded-md hover:bg-accent" onClick={() => setMainMenuOpen(true)}>
            <Menu className="h-4 w-4 text-muted-foreground" />
          </button>
        </div>
      </div>

      {/* Main area: compact sidebar + content */}
      <div className="flex flex-1 overflow-hidden">
        {/* Compact sidebar */}
        <div className="w-[72px] bg-card border-r flex flex-col shrink-0 overflow-hidden">
          {/* Agent avatar card */}
          <div className="flex flex-col items-center px-1 pt-3 pb-2 border-b">
            <img src="https://api.dicebear.com/9.x/bottts-neutral/svg?seed=pepicek" alt="" className="h-10 w-10 rounded-xl mb-1" />
            <span className="text-[9px] font-semibold text-center leading-tight">Pepicek</span>
            <span className="text-[8px] text-muted-foreground">Google Ads</span>
            <span className="text-[8px] px-1.5 py-0.5 rounded-full bg-green-100 text-green-700 mt-1">RUNNING</span>
          </div>
          {/* Scrollable pages */}
          <div className="flex-1 overflow-y-auto py-1">
            {agentPages.map((page) => (
              <button
                key={page.id}
                onClick={() => setActivePage(page.id)}
                className={cn(
                  "w-full flex flex-col items-center py-1.5 gap-0.5 transition-colors",
                  page.id === activePage ? "text-primary bg-accent" : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
                )}
              >
                <page.icon className="h-3.5 w-3.5" />
                <span className="text-[8px] font-medium leading-tight">{page.label}</span>
              </button>
            ))}
          </div>
        </div>

        {/* Content */}
        <div className="flex-1 flex flex-col overflow-hidden">
          {activePage === "sessions" && sessionView === "chat" && (
            <>
              {/* Mini session header */}
              <div className="flex items-center justify-between px-3 py-1.5 border-b bg-card shrink-0">
                <div className="flex items-center gap-2">
                  <span className="h-1.5 w-1.5 rounded-full bg-green-500" />
                  <span className="text-[11px] font-medium truncate">Campaign performance review</span>
                </div>
                <button onClick={() => setSessionView("list")} className="text-[10px] text-primary font-medium shrink-0">All</button>
              </div>
              <ChatContent />
            </>
          )}
          {activePage === "sessions" && sessionView === "list" && (
            <div className="flex-1 flex flex-col overflow-hidden">
              <div className="flex items-center justify-between px-3 py-2 border-b bg-card shrink-0">
                <span className="text-xs font-semibold">Sessions</span>
                <button className="text-[10px] text-primary font-medium">+ New</button>
              </div>
              <SessionsList onSelect={() => setSessionView("chat")} />
            </div>
          )}
          {activePage === "files" && (
            <div className="flex-1 p-3 overflow-y-auto">
              <div className="space-y-2">
                {["report_q1.pdf", "keywords.csv", "ad_copy_v2.docx", "campaign_brief.md"].map((f) => (
                  <div key={f} className="flex items-center gap-3 p-2.5 bg-card rounded-lg border">
                    <div className="h-7 w-7 rounded-lg bg-muted flex items-center justify-center text-[9px] font-bold text-muted-foreground">{f.split(".").pop()?.toUpperCase()}</div>
                    <div className="min-w-0 flex-1">
                      <div className="text-[11px] font-medium truncate">{f}</div>
                      <div className="text-[9px] text-muted-foreground">2.4 MB</div>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
          {activePage !== "sessions" && activePage !== "files" && (
            <div className="flex-1 p-4">
              <div className="text-xs text-muted-foreground text-center pt-8">{agentPages.find(p => p.id === activePage)?.label} content</div>
            </div>
          )}
        </div>
      </div>

      {/* Main nav bottom sheet */}
      {mainMenuOpen && (
        <>
          <div className="absolute inset-0 bg-black/50 z-40" onClick={() => setMainMenuOpen(false)} />
          <div className="absolute bottom-0 left-0 right-0 bg-card z-50 rounded-t-2xl shadow-2xl flex flex-col animate-in slide-in-from-bottom duration-300 max-h-[70%]">
            <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-2" />
            <div className="px-4 py-2 border-b text-sm font-semibold">Crewship</div>
            <div className="flex-1 overflow-y-auto py-2 px-2">
              {[{ l: "Dashboard", i: LayoutDashboard }, { l: "Crews", i: Network }, { l: "Agents", i: Bot }, { l: "Skills", i: Zap }, { l: "Credentials", i: Key }, { l: "Runs", i: Activity }, { l: "Settings", i: Settings }].map((n) => (
                <button key={n.l} className="w-full flex items-center gap-3 px-3 py-2.5 text-sm text-muted-foreground hover:bg-accent/50 rounded-lg"><n.i className="h-4 w-4" />{n.l}</button>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  )
}

/* ============================================================
   PREVIEW PAGE
   ============================================================ */
export default function PreviewAgentNavPage() {
  return (
    <div className="p-6 space-y-20 max-w-5xl mx-auto">
      <div className="mb-8">
        <h1 className="text-lg font-bold mb-1">Agent Mobile Navigation -- Sidebar Varianty</h1>
        <p className="text-sm text-muted-foreground">Vsechny varianty maji vertikalni agent submenu po leve strane. Chat zabira maximum mista. Klikej na polozky menu a hamburger pro interakci.</p>
      </div>

      <div>
        <h2 className="text-sm font-semibold mb-1">Varianta A -- Icon sidebar (iOS tab bar vertikalne)</h2>
        <p className="text-xs text-muted-foreground mb-6">Uzky sidebar (48px) s ikonkami + miniaturnimi popisky. Avatar agenta nahore. Chat zabira zbytek sirky. Klik na Sessions = seznam sessions, pak zpet na chat.</p>
        <VariantA />
      </div>

      <div className="mt-16">
        <h2 className="text-sm font-semibold mb-1">Varianta B -- Sliding screens (Telegram/iOS styl)</h2>
        <p className="text-xs text-muted-foreground mb-6">Prvni screen = velka karta agenta + seznam stranek vertikalne. Klik na stranku = slide doprava na obsah s Back tlacitkem. Mobilni styl drill-down navigace.</p>
        <VariantB />
      </div>

      <div className="mt-16">
        <h2 className="text-sm font-semibold mb-1">Varianta C -- Compact sidebar (72px) s avatarem + popisky</h2>
        <p className="text-xs text-muted-foreground mb-6">Sirsi sidebar (72px) s avatarem, jmenem, statusem a vertikalnimi polozkami. Chat vedle. Mini session header ukazuje aktualni session s tlacitkem "All" pro seznam.</p>
        <VariantC />
      </div>
    </div>
  )
}
