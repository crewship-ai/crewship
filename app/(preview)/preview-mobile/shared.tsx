"use client"

import { useState } from "react"
import { cn } from "@/lib/utils"
import {
  Search, Send,
  Paperclip, MoreVertical,
} from "lucide-react"
import {
  navItems, agentTabs, mockSessions, mockMessages,
} from "./mocks"

export function SessionChatContent({ showSessionList, onBack }: { showSessionList?: boolean; onBack?: () => void }) {
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
export function NavMenuContent({ sections = true }: { sections?: boolean }) {
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

export function UserFooter() {
  return (
    <div className="border-t p-4">
      <div className="flex items-center gap-3">
        <div className="h-8 w-8 rounded-full bg-primary text-[10px] font-bold text-primary-foreground flex items-center justify-center">DU</div>
        <div className="flex-1 min-w-0">
          <div className="text-xs font-medium">Demo User</div>
          <div className="text-[10px] text-muted-foreground">demo@crewship.local</div>
        </div>
      </div>
    </div>
  )
}

/* Shared: agent header + tabs */
export function AgentHeaderBlock({ compact }: { compact?: boolean }) {
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
        <button
          aria-label="Open agent actions"
          className="h-7 w-7 flex items-center justify-center rounded-md hover:bg-accent shrink-0"
        >
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
export function MobileViewSwitcher({ active, onChange }: { active: string; onChange: (v: string) => void }) {
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
