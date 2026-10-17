"use client"

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Bot, ChevronDown, Clock, Layers, Plus, Upload } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { CreateCrewDialog, type CrewStart } from "./create-crew-dialog"
import { CreateAgentDialog } from "./create-agent"

export interface CreateCrewLite {
  id: string
  name: string
  slug: string
  icon?: string | null
  color?: string | null
  _count?: { agents: number }
}

export interface CrewsCreateApi {
  /** Opens New crew on the given way to start. */
  newCrew: (start?: CrewStart) => void
  /** Opens New agent, the given crew picked (the open one by default). */
  newAgent: (crewSlug?: string | null) => void
}

const CrewsCreateContext = React.createContext<CrewsCreateApi | null>(null)

export function useCrewsCreate(): CrewsCreateApi {
  const api = React.useContext(CrewsCreateContext)
  return api ?? { newCrew: () => {}, newAgent: () => {} }
}

function typing(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null
  return !!el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable)
}

/**
 * Hosts New crew and New agent for /crews. Opened from the New menu, the
 * panel's +, the keys C and A, and ?new=crew | ?new=agent (the command
 * palette's deep link; the param is consumed so a reload does not reopen).
 */
export function CrewsCreateProvider({
  workspaceId,
  crews,
  currentCrewSlug = null,
  onCrewCreated,
  onAgentCreated,
  children,
}: {
  workspaceId: string
  crews: CreateCrewLite[]
  /** The open crew; New agent and the A key put the agent there. */
  currentCrewSlug?: string | null
  onCrewCreated: () => void
  onAgentCreated: (slug: string) => void
  children?: React.ReactNode
}) {
  const router = useRouter()
  const params = useSearchParams()
  const [crewOpen, setCrewOpen] = React.useState(false)
  const [crewStart, setCrewStart] = React.useState<CrewStart>("suggest")
  const [agentOpen, setAgentOpen] = React.useState(false)
  const [agentCrew, setAgentCrew] = React.useState<string | null>(null)

  const currentRef = React.useRef(currentCrewSlug)
  currentRef.current = currentCrewSlug

  const api = React.useMemo<CrewsCreateApi>(() => ({
    newCrew: (start = "suggest") => { setCrewStart(start); setCrewOpen(true) },
    newAgent: (crewSlug) => { setAgentCrew(crewSlug === undefined ? currentRef.current : crewSlug); setAgentOpen(true) },
  }), [])

  const newParam = params.get("new")
  const crewParam = params.get("crew")
  React.useEffect(() => {
    if (newParam !== "crew" && newParam !== "agent") return
    if (newParam === "crew") api.newCrew("suggest")
    else api.newAgent(crewParam)
    const url = new URL(window.location.href)
    url.searchParams.delete("new")
    router.replace(`${url.pathname}${url.search}`, { scroll: false })
  }, [newParam, crewParam, router, api])

  // C = New crew, A = New agent in the open crew (Harbor 2 New menu).
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || typing(e.target)) return
      if (document.querySelector("[role=dialog],[role=alertdialog]")) return
      const k = e.key.toLowerCase()
      if (k === "c") { e.preventDefault(); api.newCrew("suggest") }
      else if (k === "a") { e.preventDefault(); api.newAgent() }
    }
    document.addEventListener("keydown", onKey)
    return () => document.removeEventListener("keydown", onKey)
  }, [api])

  return (
    <CrewsCreateContext.Provider value={api}>
      {children}
      <CreateCrewDialog
        workspaceId={workspaceId}
        open={crewOpen}
        onOpenChange={setCrewOpen}
        initialStart={crewStart}
        onCreated={onCrewCreated}
      />
      <CreateAgentDialog
        workspaceId={workspaceId}
        open={agentOpen}
        onOpenChange={setAgentOpen}
        defaultCrewSlug={agentCrew}
        crews={crews.map((c) => ({ id: c.id, slug: c.slug, name: c.name, icon: c.icon, color: c.color, agentCount: c._count?.agents }))}
        onCreated={onAgentCreated}
      />
    </CrewsCreateContext.Provider>
  )
}

/** The page's one primary action: New ▾ (Harbor 2). */
export function CrewsNewMenu({ crewName, crewSlug, onHire, compact = false }: {
  crewName?: string | null
  crewSlug?: string | null
  /** Hire a temporary agent into the open crew; hidden without one. */
  onHire?: () => void
  compact?: boolean
}) {
  const { newCrew, newAgent } = useCrewsCreate()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button size={compact ? "icon-sm" : "sm"} className={compact ? undefined : "gap-1.5 px-3"} aria-label="New">
          <Plus className="h-3 w-3" aria-hidden />
          {!compact && <span>New</span>}
          {!compact && <ChevronDown className="h-3 w-3 opacity-70" aria-hidden />}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        <DropdownMenuLabel>Create</DropdownMenuLabel>
        <MenuRow icon={Plus} title="New crew" sub="Describe it; the lead proposes the team" shortcut="C" onSelect={() => newCrew("suggest")} />
        <MenuRow icon={Layers} title="Crew from a template" sub="Tested teams with their tools" onSelect={() => newCrew("template")} />
        <MenuRow icon={Upload} title="Import crewship.yaml" sub="A crew you exported or wrote" onSelect={() => newCrew("yaml")} />
        {crewSlug && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel>In {crewName ?? crewSlug}</DropdownMenuLabel>
            <MenuRow icon={Bot} title="New agent" sub={`Joins ${crewName ?? crewSlug} for good`} shortcut="A" onSelect={() => newAgent(crewSlug)} />
            {onHire && <MenuRow icon={Clock} title="Hire a temporary agent" sub="30 minutes to 24 hours" onSelect={onHire} />}
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function MenuRow({ icon: Icon, title, sub, shortcut, onSelect }: {
  icon: typeof Plus
  title: string
  sub: string
  shortcut?: string
  onSelect: () => void
}) {
  return (
    <DropdownMenuItem onSelect={onSelect} className="items-start gap-2.5 py-2">
      <span className="icon-tile mt-0.5 grid size-6 shrink-0 place-items-center rounded-md" aria-hidden>
        <Icon className="size-3.5" />
      </span>
      <span className="min-w-0 flex-1">
        <b className="block text-body font-medium text-foreground">{title}</b>
        <span className="block text-label text-muted-foreground">{sub}</span>
      </span>
      {shortcut && <DropdownMenuShortcut>{shortcut}</DropdownMenuShortcut>}
    </DropdownMenuItem>
  )
}
