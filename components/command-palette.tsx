"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import {
  Bot, Network, Zap, Key, Activity, Shield, Settings,
  LayoutDashboard, Plus, ShieldCheck, CircleDot, FolderKanban,
  Inbox, ClipboardCheck, CalendarClock, Plug, Users, Lock, User,
} from "lucide-react"
import { StatusIcon } from "@/components/features/issues/status-icon"
import { PriorityIcon } from "@/components/features/issues/priority-icon"
import {
  CommandDialog,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
} from "@/components/ui/command"
import { useWorkspace } from "@/hooks/use-workspace"
import { getCrewDotColor, getGradientPalette } from "@/lib/entities"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"

interface AgentResult {
  id: string
  name: string
  slug: string
  role_title: string | null
  status: string
  avatar_seed: string | null
  avatar_style: string | null
  /** Stored avatar render (#1297); null means generate from the seed. */
  avatar_url?: string | null
  crew: { name: string; slug: string; color: string | null; avatar_style?: string | null } | null
}

interface CrewResult {
  id: string
  name: string
  slug: string
  color: string | null
  icon: string | null
  _count: { agents: number; members: number }
}

interface SkillResult {
  id: string
  name: string
  slug: string
  display_name: string | null
  category: string
}

interface CredentialResult {
  id: string
  name: string
  provider: string
  type: string
}

interface IssueResult {
  id: string
  identifier: string | null
  title: string
  status: string
  priority: string
  assignee_name: string | null
  crew_name: string | null
  crew_slug: string | null
}

interface ProjectResult {
  id: string
  name: string
  slug: string
  color: string
  status: string
  issue_count: number
}

const PROVIDER_LABELS: Record<string, string> = {
  ANTHROPIC: "Anthropic",
  OPENAI: "OpenAI",
  GOOGLE: "Google",
  CURSOR: "Cursor",
  FACTORY: "Factory",
  NONE: "Custom",
}

const NAV_ITEMS = [
  { title: "Dashboard", href: "/", icon: LayoutDashboard },
  { title: "Issues", href: "/issues", icon: CircleDot },
  { title: "Crews", href: "/crews", icon: Network },
  { title: "Agents", href: "/crews/agents", icon: Bot },
  { title: "Inbox", href: "/inbox", icon: Inbox },
  { title: "Approvals", href: "/approvals", icon: ClipboardCheck },
  { title: "Activity", href: "/activity", icon: Activity },
  { title: "Routines", href: "/routines", icon: CalendarClock },
  { title: "Integrations", href: "/integrations", icon: Plug },
  { title: "Skills", href: "/skills", icon: Zap },
  { title: "Credentials", href: "/credentials", icon: Key },
  { title: "Journal", href: "/journal", icon: Activity },
  { title: "Runs", href: "/journal?tab=runs", icon: Activity },
  { title: "Settings", href: "/settings", icon: Settings },
  { title: "Admin", href: "/admin", icon: ShieldCheck },
]

// Deep-links into the settings sub-tabs. These rely on SettingsLayout reading
// the `?tab=` param on mount (see initialSettingsTab in settings-layout.tsx).
const SETTINGS_LINKS = [
  { title: "Profile", href: "/settings?tab=profile", icon: User },
  { title: "General", href: "/settings?tab=general", icon: Settings },
  { title: "Members", href: "/settings?tab=members", icon: Users },
  { title: "Privacy & Memory", href: "/settings?tab=privacy", icon: Lock },
  // "Crew links" here and in the nav; Integrations owns the word "connections"
  // for the services this instance is hooked up to.
  { title: "Crew links", href: "/settings?tab=connections", icon: Network },
  { title: "Audit Log", href: "/settings?tab=audit", icon: Shield },
]

const QUICK_ACTIONS = [
  { title: "Create new agent", href: "/crews/agents/new", icon: Plus, keywords: ["add", "new", "agent"] },
  { title: "Create new crew", href: "/crews/new", icon: Plus, keywords: ["add", "new", "crew", "team"] },
]

interface CommandPaletteProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

// =============================================================================
// The search bar's panel, drawn from the top bar's popover kit.
//
// ⌘K is the fourth surface in that strip and was the one that looked least
// like the other three: a centred dialog with 12px muted group labels, 12px
// rows on 6px padding, no frame in common with the Inbox popover it sits two
// icons away from. It carries the same kind of content — grouped rows of
// things you can open — so it now carries the same chrome:
//
//   frame    — the popover's border/card/shadow, dropped from the top of the
//              viewport rather than centred, so it reads as coming out of the
//              search field it was opened from.
//   heading  — the kit's section header: uppercase, tracked, on the subtle
//              fill, with the group's own name.
//   row      — the kit's row: identity on the left, what it IS in the middle,
//              what it is ABOUT on the right.
//   footer   — the kit's action strip, here carrying the keys.
//
// The classes below are the arbitrary-variant form of the same values in
// components/layout/bar-menu.tsx, because cmdk owns the elements they land on
// and the kit's components cannot be substituted for them.
// =============================================================================

const PALETTE_COMMAND_CLASS = [
  "[&_[data-slot=command-input-wrapper]]:h-11",
  "[&_[data-slot=command-input-wrapper]]:border-white/[0.06]",
  "[&_[data-slot=command-input-wrapper]]:px-3",
  "[&_[cmdk-item]_svg]:h-4 [&_[cmdk-item]_svg]:w-4",
].join(" ")

// The kit's BarMenuSection header. Layout lands on cmdk's heading element;
// the type role rides on the ReactNode passed as `heading` (below), because a
// class from @layer utilities cannot be used inside an arbitrary variant.
const PALETTE_GROUP_CLASS = [
  "p-0",
  "[&_[cmdk-group-heading]]:flex [&_[cmdk-group-heading]]:items-center",
  "[&_[cmdk-group-heading]]:border-b [&_[cmdk-group-heading]]:border-white/[0.04]",
  "[&_[cmdk-group-heading]]:bg-surface-subtle/60",
  "[&_[cmdk-group-heading]]:px-3 [&_[cmdk-group-heading]]:py-1",
  "[&_[cmdk-group-heading]]:font-normal",
].join(" ")

// The kit's BarMenuRow: same padding, same gap, same hover fill.
const PALETTE_ITEM_CLASS =
  "gap-2.5 rounded-none px-3 py-2 data-[selected=true]:bg-white/[0.04] data-[selected=true]:text-foreground"

/** Group label, in the kit's section-header type role. */
function GroupLabel({ children }: { children: React.ReactNode }) {
  return <span className="type-meta uppercase tracking-wider text-foreground/40">{children}</span>
}

export function CommandPalette({ open, onOpenChange }: CommandPaletteProps) {
  const router = useRouter()
  const { workspaceId, role } = useWorkspace()
  // The Admin console is ADMIN+ (#865); the sidebar/toolbar already filter it,
  // so the palette must too — otherwise a MEMBER sees an "Admin" command that
  // just bounces them off /admin.
  const isAdmin = role === "OWNER" || role === "ADMIN"

  const [agents, setAgents] = useState<AgentResult[]>([])
  const [crews, setCrews] = useState<CrewResult[]>([])
  const [skills, setSkills] = useState<SkillResult[]>([])
  const [credentials, setCredentials] = useState<CredentialResult[]>([])
  const [issues, setIssues] = useState<IssueResult[]>([])
  const [projects, setProjects] = useState<ProjectResult[]>([])
  const filteredIssues = issues.filter((issue) => issue.identifier)

  useEffect(() => {
    if (!open || !workspaceId) return
    const ac = new AbortController()
    const qs = `workspace_id=${workspaceId}`

    setAgents([])
    setCrews([])
    setSkills([])
    setCredentials([])
    setIssues([])
    setProjects([])

    const opts = { signal: ac.signal }
    Promise.allSettled([
      apiFetch(`/api/v1/agents?${qs}`, opts),
      apiFetch(`/api/v1/crews?${qs}`, opts),
      apiFetch(`/api/v1/skills?${qs}`, opts),
      apiFetch(`/api/v1/credentials?${qs}`, opts),
      apiFetch(`/api/v1/issues?${qs}&limit=50`, opts),
      apiFetch(`/api/v1/projects?${qs}`, opts),
    ]).then(async ([agentsRes, crewsRes, skillsRes, credsRes, issuesRes, projectsRes]) => {
      if (ac.signal.aborted) return
      const safeJson = async (r: PromiseSettledResult<Response>) =>
        r.status === "fulfilled" && r.value.ok ? r.value.json() : null
      const [agentsData, crewsData, skillsData, credsData, issuesData, projectsData] =
        await Promise.all([safeJson(agentsRes), safeJson(crewsRes), safeJson(skillsRes), safeJson(credsRes), safeJson(issuesRes), safeJson(projectsRes)])
      if (ac.signal.aborted) return
      if (agentsData) setAgents(agentsData)
      if (crewsData) setCrews(crewsData)
      if (skillsData) setSkills(skillsData)
      if (credsData) setCredentials(credsData)
      if (issuesData) setIssues(issuesData)
      if (projectsData) setProjects(projectsData)
    })

    return () => ac.abort()
  }, [open, workspaceId])

  function runCommand(fn: () => void) {
    onOpenChange(false)
    fn()
  }

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Command Palette"
      description="Search agents, crews, skills, and more..."
      // Dropped from the top rather than centred: it belongs to the search
      // field in the bar, and the bar is where the eye already is.
      className="top-[12vh] translate-y-0 gap-0 rounded-lg border-white/[0.1] bg-card p-0 shadow-xl sm:max-w-[600px]"
      commandClassName={PALETTE_COMMAND_CLASS}
      showCloseButton={false}
    >
      <CommandInput placeholder="Search issues, projects, agents..." />
      <CommandList className="max-h-[420px]">
        <CommandEmpty>
          <span className="type-row text-muted-foreground">No results found.</span>
        </CommandEmpty>

        <CommandGroup heading={<GroupLabel>Quick actions</GroupLabel>} className={PALETTE_GROUP_CLASS}>
          {QUICK_ACTIONS.map((action) => (
            <CommandItem
              key={action.href}
              value={action.title}
              keywords={action.keywords}
              className={PALETTE_ITEM_CLASS}
              onSelect={() => runCommand(() => router.push(action.href))}
            >
              <action.icon className="h-4 w-4 text-muted-foreground" />
              <span className="type-row">{action.title}</span>
            </CommandItem>
          ))}
        </CommandGroup>

        {filteredIssues.length > 0 && (
          <CommandGroup heading={<GroupLabel>Issues</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {filteredIssues.map((issue) => (
              <CommandItem
                key={issue.id}
                value={`issue ${issue.identifier} ${issue.title}`}
                keywords={[issue.status, issue.priority, issue.assignee_name ?? "", issue.crew_name ?? ""]}
                className={PALETTE_ITEM_CLASS}
                onSelect={() => runCommand(() => router.push(`/issues/${issue.identifier}`))}
              >
                <StatusIcon status={issue.status} className="h-4 w-4 shrink-0" />
                <span className="type-meta shrink-0 font-mono text-muted-foreground-soft">{issue.identifier}</span>
                <span className="type-row flex-1 truncate">{issue.title}</span>
                <PriorityIcon priority={issue.priority as "urgent" | "high" | "medium" | "low" | "none"} className="h-3.5 w-3.5 shrink-0" />
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {projects.length > 0 && (
          <CommandGroup heading={<GroupLabel>Projects</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {projects.map((project) => (
              <CommandItem
                key={project.id}
                value={`project ${project.name} ${project.slug}`}
                keywords={[project.status]}
                className={PALETTE_ITEM_CLASS}
                onSelect={() => runCommand(() => router.push(`/issues?project=${project.id}`))}
              >
                <div className={cn("h-4 w-4 rounded shrink-0 flex items-center justify-center bg-gradient-to-br", getGradientPalette(project.color).from, getGradientPalette(project.color).to)}>
                  <FolderKanban className={cn("h-2.5 w-2.5", getGradientPalette(project.color).text)} />
                </div>
                <span className="type-row flex-1 truncate">{project.name}</span>
                <span className="type-meta text-muted-foreground-soft">{project.issue_count} issues</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {agents.length > 0 && (
          <CommandGroup heading={<GroupLabel>Agents</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {agents.map((agent) => (
              <CommandItem
                key={agent.id}
                value={`agent ${agent.name} ${agent.slug}`}
                keywords={[agent.role_title ?? "", agent.crew?.name ?? "", agent.status]}
                className={PALETTE_ITEM_CLASS}
                onSelect={() => runCommand(() => router.push(`/crews/agents/${agent.id}`))}
              >
                <AgentAvatar
                  seed={agent.avatar_seed || agent.name}
                  style={agent.avatar_style || agent.crew?.avatar_style}
                  agentId={agent.id}
                  avatarUrl={agent.avatar_url}
                  className="h-5 w-5 rounded-full shrink-0"
                />
                <span className="type-row flex-1 truncate">{agent.name}</span>
                {agent.crew && (
                  <span className="type-meta flex max-w-[140px] items-center gap-1.5 truncate text-muted-foreground-soft">
                    <span
                      className="h-2 w-2 rounded-full shrink-0"
                      style={{ backgroundColor: getCrewDotColor(agent.crew.color) }}
                    />
                    {agent.crew.name}
                  </span>
                )}
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {crews.length > 0 && (
          <CommandGroup heading={<GroupLabel>Crews</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {crews.map((crew) => (
              <CommandItem
                key={crew.id}
                value={`crew ${crew.name} ${crew.slug}`}
                className={PALETTE_ITEM_CLASS}
                onSelect={() => runCommand(() => router.push(`/crews/${crew.id}`))}
              >
                <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="sm" className="h-5 w-5 rounded-md [&>svg]:h-3 [&>svg]:w-3" />
                <span className="type-row flex-1 truncate">{crew.name}</span>
                <span className="type-meta text-muted-foreground-soft">
                  {crew._count.agents} agent{crew._count.agents !== 1 ? "s" : ""}
                </span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {skills.length > 0 && (
          <CommandGroup heading={<GroupLabel>Skills</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {skills.map((skill) => (
              <CommandItem
                key={skill.id}
                value={`skill ${skill.display_name ?? skill.name} ${skill.slug}`}
                keywords={[skill.category]}
                className={PALETTE_ITEM_CLASS}
                onSelect={() => runCommand(() => router.push("/skills"))}
              >
                <Zap className="h-4 w-4 text-muted-foreground" />
                <span className="type-row flex-1 truncate">{skill.display_name ?? skill.name}</span>
                <span className="type-meta capitalize text-muted-foreground-soft">{skill.category.toLowerCase()}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {credentials.length > 0 && (
          <CommandGroup heading={<GroupLabel>Credentials</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {credentials.map((cred) => (
              <CommandItem
                key={cred.id}
                value={`credential ${cred.name}`}
                keywords={[cred.provider, cred.type]}
                className={PALETTE_ITEM_CLASS}
                onSelect={() => runCommand(() => router.push("/credentials"))}
              >
                <Key className="h-4 w-4 text-muted-foreground" />
                <span className="type-row flex-1 truncate">{cred.name}</span>
                <span className="type-meta text-muted-foreground-soft">
                  {PROVIDER_LABELS[cred.provider] ?? cred.provider}
                </span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        <CommandGroup heading={<GroupLabel>Navigation</GroupLabel>} className={PALETTE_GROUP_CLASS}>
          {NAV_ITEMS.filter((item) => item.href !== "/admin" || isAdmin).map((item) => (
            <CommandItem
              key={item.href}
              value={`go to ${item.title}`}
              keywords={["navigate", "page", item.title.toLowerCase()]}
              className={PALETTE_ITEM_CLASS}
              onSelect={() => runCommand(() => router.push(item.href))}
            >
              <item.icon className="h-4 w-4 text-muted-foreground" />
              <span className="type-row">{item.title}</span>
            </CommandItem>
          ))}
        </CommandGroup>

        <CommandGroup heading={<GroupLabel>Settings</GroupLabel>} className={PALETTE_GROUP_CLASS}>
          {SETTINGS_LINKS.map((item) => (
            <CommandItem
              key={item.href}
              value={`settings ${item.title}`}
              keywords={["settings", "preferences", item.title.toLowerCase()]}
              className={PALETTE_ITEM_CLASS}
              onSelect={() => runCommand(() => router.push(item.href))}
            >
              <item.icon className="h-4 w-4 text-muted-foreground" />
              <span className="type-row">{item.title}</span>
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>

      {/* The kit's footer strip. The palette is the one surface here whose
          whole interaction is the keyboard, and it was the one that never
          said so. */}
      <div className="flex items-center gap-3 border-t border-white/[0.06] px-3 py-1.5">
        <PaletteHint keys={["↑", "↓"]}>navigate</PaletteHint>
        <PaletteHint keys={["↵"]}>open</PaletteHint>
        <PaletteHint keys={["esc"]}>close</PaletteHint>
      </div>
    </CommandDialog>
  )
}

function PaletteHint({ keys, children }: { keys: string[]; children: React.ReactNode }) {
  return (
    <span className="type-meta flex items-center gap-1 text-muted-foreground-soft">
      {keys.map((k) => (
        <kbd
          key={k}
          className="flex h-4 min-w-[16px] items-center justify-center rounded border border-white/[0.08] bg-white/[0.03] px-1 font-mono text-[10px] leading-none"
        >
          {k}
        </kbd>
      ))}
      {children}
    </span>
  )
}
