"use client"

import { useState } from "react"
import { cn } from "@/lib/utils"
import {
  Menu, X, Search, ChevronRight,
  LayoutDashboard, Bot, Network, Zap, Key, Activity,
  Shield, Settings, Home, LayoutGrid,
} from "lucide-react"
import { navItems } from "./mocks"
import {
  SessionChatContent, NavMenuContent, UserFooter,
  AgentHeaderBlock, MobileViewSwitcher,
} from "./shared"

export function VariantA() {
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
export function VariantB() {
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
export function VariantC() {
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
export function VariantD() {
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
export function VariantE() {
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

