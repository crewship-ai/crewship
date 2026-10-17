"use client"

import Link from "next/link"
import { Activity, ChevronRight, Copy, Link2, MessageSquare, MoreHorizontal, RotateCcw, Square, SquareTerminal, Trash2 } from "lucide-react"
import { toast } from "@/lib/toast"

import { SubBar, SubBarIconButton, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useIsMobile } from "@/hooks/use-mobile"
import { activityUrl } from "@/lib/activity-url"

import { CrewsNewMenu } from "./crews-create"

/** Activity filtered to one crew or one agent (?walk=crew:<id>). */
export function activityHrefFor(kind: "crew" | "agent", id: string, label: string): string {
  return activityUrl("/activity", { path: { stops: [{ kind, id, label }], dropped: 0 }, lens: "workflows" })
}

async function copy(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success(`${what} copied`)
  } catch {
    toast.error(`Could not copy the ${what.toLowerCase()}`)
  }
}

/** The /crews top bar with nothing open: the area name and New. */
export function CrewsRootBar() {
  return <SubBar title="Crews" ariaLabel="Crews" actions={<CrewsNewMenu />} />
}

/**
 * Crew page actions in the top bar: Activity, Terminal, Chat with the
 * lead, the crew menu and New. On a phone only Chat stays.
 */
export function CrewPageBar({
  crew,
  lead,
  onTerminal,
  onHire,
  onRestartAgents,
  onDelete,
}: {
  crew: { id: string; name: string; slug: string }
  lead: { name: string; slug: string } | null
  onTerminal: () => void
  onHire?: () => void
  onRestartAgents?: () => void
  onDelete?: () => void
}) {
  const isMobile = useIsMobile()
  const chat = lead ? (
    isMobile ? (
      <Button asChild size="icon-sm" aria-label={`Chat with ${lead.name}`}><Link href={`/chat/${encodeURIComponent(lead.slug)}`}><MessageSquare className="h-3.5 w-3.5" /></Link></Button>
    ) : (
      <SubBarSecondary asChild icon={MessageSquare} className="border border-border"><Link href={`/chat/${encodeURIComponent(lead.slug)}`}>Chat with {lead.name}</Link></SubBarSecondary>
    )
  ) : null
  return (
    <SubBar
      title="Crews"
      section={crew.name}
      heading={false}
      ariaLabel={`Crews, ${crew.name}`}
      actions={isMobile ? chat : (
        <>
          <SubBarSecondary asChild icon={Activity}><Link href={activityHrefFor("crew", crew.id, crew.name)}>Activity</Link></SubBarSecondary>
          <SubBarSecondary icon={SquareTerminal} onClick={onTerminal}>Terminal</SubBarSecondary>
          {chat}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <SubBarIconButton icon={MoreHorizontal} aria-label="Crew options" title="Crew options" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              {onRestartAgents && <DropdownMenuItem onSelect={onRestartAgents}><RotateCcw /> Restart agents</DropdownMenuItem>}
              <DropdownMenuItem onSelect={() => void copy(`crewship crew get ${crew.slug}`, "CLI command")}><Copy /> Copy CLI command</DropdownMenuItem>
              <DropdownMenuItem onSelect={() => void copy(`${window.location.origin}/crews?crew=${encodeURIComponent(crew.slug)}`, "Link")}><Link2 /> Copy link</DropdownMenuItem>
              {onDelete && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem variant="destructive" onSelect={onDelete}><Trash2 /> Delete crew…</DropdownMenuItem>
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
          <CrewsNewMenu crewName={crew.name} crewSlug={crew.slug} onHire={onHire} />
        </>
      )}
    />
  )
}

/**
 * Agent page actions: Activity, Terminal, Stop while it runs, and
 * Chat with the agent as the primary. On a phone only Chat stays.
 */
export function AgentPageBar({
  agent,
  crewName,
  running,
  onTerminal,
  onStop,
}: {
  agent: { id: string; name: string; slug: string }
  crewName: string | null
  running: boolean
  onTerminal?: () => void
  onStop?: () => void
}) {
  const isMobile = useIsMobile()
  const chatHref = `/chat/${encodeURIComponent(agent.slug)}`
  return (
    <SubBar
      title="Crews"
      section={crewName ? <>{crewName}<ChevronRight className="mx-1 inline size-3.5 text-muted-foreground-soft" aria-hidden /><span className="sr-only"> › </span>{agent.name}</> : agent.name}
      heading={false}
      ariaLabel={`Crews, ${crewName ? `${crewName}, ` : ""}${agent.name}`}
      actions={isMobile ? (
        <Button asChild size="icon-sm" aria-label={`Chat with ${agent.name}`}><Link href={chatHref}><MessageSquare className="h-3.5 w-3.5" /></Link></Button>
      ) : (
        <>
          <SubBarSecondary asChild icon={Activity}><Link href={activityHrefFor("agent", agent.id, agent.name)}>Activity</Link></SubBarSecondary>
          {onTerminal && <SubBarSecondary icon={SquareTerminal} onClick={onTerminal}>Terminal</SubBarSecondary>}
          {running && onStop && <SubBarSecondary icon={Square} onClick={onStop} className="border border-border">Stop</SubBarSecondary>}
          <SubBarPrimary asChild icon={MessageSquare}><Link href={chatHref}>Chat with {agent.name}</Link></SubBarPrimary>
        </>
      )}
    />
  )
}
