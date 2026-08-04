"use client"

import { useEffect, useMemo, useState } from "react"
import { useRouter } from "next/navigation"
import {
  Network, Zap, Key, Activity, Settings, LayoutDashboard, Plus, ShieldCheck,
  CircleDot, Inbox, ClipboardCheck, CalendarClock, Plug, History,
} from "lucide-react"
import { StatusIcon } from "@/components/features/issues/status-icon"
import { PriorityIcon } from "@/components/features/issues/priority-icon"
import { visibleSettingsSections } from "@/components/features/settings/settings-nav"
import { MCPLogo } from "@/components/icons/mcp-logos"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import { paletteFilter } from "@/lib/palette-filter"
import { routineHref } from "@/lib/routine-href"
import { useAbilities } from "@/hooks/use-abilities"
import { UserAvatar } from "@/components/ui/user-avatar"
import {
  CommandDialog,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
} from "@/components/ui/command"
import { useWorkspace } from "@/hooks/use-workspace"
import { getCrewDotColor } from "@/lib/entities"
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
  /** Named glyph, same vocabulary crews use. Null = the folder default. */
  icon: string | null
  status: string
  issue_count: number
}

interface RoutineResult {
  id: string
  slug: string
  name: string
  description: string
  status: string
}

interface MemberResult {
  id: string
  role: string
  user: { id: string; email: string; full_name: string | null; avatar_url: string | null }
}

interface IntegrationResult {
  id: string
  name: string
  display_name: string
  transport: string
  icon: string | null
  crew_name?: string
  enabled: boolean
}

const PROVIDER_LABELS: Record<string, string> = {
  ANTHROPIC: "Anthropic",
  OPENAI: "OpenAI",
  GOOGLE: "Google",
  CURSOR: "Cursor",
  FACTORY: "Factory",
  NONE: "Custom",
}

// Every destination the sidebar offers, plus the few surfaces that have a
// page but no sidebar row. "Agents" is deliberately absent: /crews/agents was
// deleted by the crews redesign, agents live in the /crews canvas, and typing
// "agents" reaches the Crews row by keyword and the agents themselves by name.
const NAV_ITEMS: Array<{ title: string; href: string; icon: typeof Network; keywords: string[] }> = [
  { title: "Dashboard", href: "/", icon: LayoutDashboard, keywords: [] },
  { title: "Issues", href: "/issues", icon: CircleDot, keywords: [] },
  { title: "Crews", href: "/crews", icon: Network, keywords: ["agents", "roster", "canvas"] },
  { title: "Inbox", href: "/inbox", icon: Inbox, keywords: [] },
  { title: "Approvals", href: "/approvals", icon: ClipboardCheck, keywords: [] },
  { title: "Activity", href: "/activity", icon: Activity, keywords: ["runs", "traces"] },
  { title: "Routines", href: "/routines", icon: CalendarClock, keywords: ["pipelines"] },
  { title: "Integrations", href: "/integrations", icon: Plug, keywords: ["mcp", "notifications", "channels"] },
  { title: "Skills", href: "/skills", icon: Zap, keywords: [] },
  { title: "Credentials", href: "/credentials", icon: Key, keywords: ["secrets", "tokens"] },
  { title: "Journal", href: "/journal", icon: Activity, keywords: ["audit"] },
  { title: "Runs", href: "/journal?tab=runs", icon: Activity, keywords: [] },
  { title: "Settings", href: "/settings", icon: Settings, keywords: [] },
  { title: "Admin", href: "/admin", icon: ShieldCheck, keywords: [] },
]

// Creating a crew or an agent is a DIALOG on /crews, not a route — the
// redesign deleted /crews/new and /crews/agents/new, and the palette went on
// pointing at both, so its two most prominent rows landed on an empty page
// carrying nothing but the sidebar. The `?new=` param opens the same dialog
// the crews sub-bar does.
//
// Gated on CASL, which starts both at MANAGER. A VIEWER offered "Create new
// agent" is being told to try something the server will refuse.
const QUICK_ACTIONS = [
  { title: "Create new agent", href: "/crews?new=agent", icon: Plus, subject: "Agent", keywords: ["add", "new", "agent"] },
  { title: "Create new crew", href: "/crews?new=crew", icon: Plus, subject: "Crew", keywords: ["add", "new", "crew", "team"] },
] as const

// The issue list is fetched whole and filtered in the browser — there is no
// server-side search route. 50 was a page size masquerading as a search: the
// 51st issue simply did not exist as far as ⌘K was concerned, and the palette
// answered "No results found", which was not true. This clears a real
// workspace; if one ever outgrows it, the fix is a search endpoint, not a
// bigger number.
const ISSUE_FETCH_LIMIT = 500

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

// ── Recent ─────────────────────────────────────────────────────────────────
//
// The palette had no memory: every open showed the same alphabet, so it was a
// tool for when you did not know where something was, rather than one you
// reach for constantly. The last few things you opened, newest first, at the
// top.
//
// Local to the browser on purpose — this is a UI convenience, not workspace
// state, and shipping it to the server would mean a table, a migration and a
// sync story for something a user would not miss on a new machine.

const RECENT_KEY = "crewship.palette.recent"
const RECENT_MAX = 5

interface RecentEntry {
  href: string
  label: string
  group: string
}

function readRecent(): RecentEntry[] {
  if (typeof window === "undefined") return []
  try {
    const raw = window.localStorage.getItem(RECENT_KEY)
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    // Trust nothing from storage: another tab, an older build or a user with
    // devtools can all have written something else here, and a palette that
    // throws on open is worse than one with no history.
    return parsed
      .filter(
        (e): e is RecentEntry =>
          !!e && typeof e === "object" &&
          typeof (e as RecentEntry).href === "string" &&
          typeof (e as RecentEntry).label === "string" &&
          typeof (e as RecentEntry).group === "string",
      )
      .slice(0, RECENT_MAX)
  } catch {
    return []
  }
}

function pushRecent(entry: RecentEntry) {
  if (typeof window === "undefined") return
  try {
    const next = [entry, ...readRecent().filter((e) => e.href !== entry.href)].slice(0, RECENT_MAX)
    window.localStorage.setItem(RECENT_KEY, JSON.stringify(next))
  } catch {
    // A full or blocked store costs the history, never the navigation.
  }
}

/**
 * A credential's provider mark, from the same registry /credentials draws
 * from. Falls back to the registry's generic entry, so an unknown provider
 * still gets a glyph rather than a hole.
 */
function CredentialBrand({ provider }: { provider: string }) {
  const brand = getBrand(provider)
  const Icon = brand.Icon
  return (
    <span data-credential-brand={provider} className="flex h-4 w-4 shrink-0 items-center justify-center">
      <Icon className="h-4 w-4" style={{ color: brandColor(brand) }} aria-label={brand.label} />
    </span>
  )
}

/** Group label, in the kit's section-header type role. */
function GroupLabel({ children }: { children: React.ReactNode }) {
  return <span className="type-meta uppercase tracking-wider text-foreground/40">{children}</span>
}

export function CommandPalette({ open, onOpenChange }: CommandPaletteProps) {
  const router = useRouter()
  const { workspaceId, role } = useWorkspace()
  const { abilities } = useAbilities()
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
  const [routines, setRoutines] = useState<RoutineResult[]>([])
  const [members, setMembers] = useState<MemberResult[]>([])
  const [integrations, setIntegrations] = useState<IntegrationResult[]>([])
  const [recent, setRecent] = useState<RecentEntry[]>([])
  const filteredIssues = issues.filter((issue) => issue.identifier)

  // The settings sections come from the settings nav itself, filtered by the
  // same predicate it uses, so the palette can never advertise a pane the
  // nav hides or the layout would bounce the caller out of.
  const settingsLinks = useMemo(() => visibleSettingsSections(role), [role])

  // CASL starts "create Crew" and "create Agent" at MANAGER. Offering either
  // to a MEMBER is telling them to try something the server refuses.
  const allowedQuickActions = useMemo(
    () => QUICK_ACTIONS.filter((a) => abilities.can("create", a.subject)),
    [abilities],
  )

  // Read once per open, not on every render: the list must not reshuffle
  // under the cursor while the palette is up.
  useEffect(() => {
    if (open) setRecent(readRecent())
  }, [open])

  useEffect(() => {
    if (!open || !workspaceId) return
    const ac = new AbortController()
    const qs = `workspace_id=${workspaceId}`
    const ws = encodeURIComponent(workspaceId)

    setAgents([])
    setCrews([])
    setSkills([])
    setCredentials([])
    setIssues([])
    setProjects([])
    setRoutines([])
    setMembers([])
    setIntegrations([])

    const opts = { signal: ac.signal }
    // Every list is RBAC-filtered server-side, so what comes back is already
    // what this caller may see. Only the STATIC rows above need a gate.
    Promise.allSettled([
      apiFetch(`/api/v1/agents?${qs}`, opts),
      apiFetch(`/api/v1/crews?${qs}`, opts),
      apiFetch(`/api/v1/skills?${qs}`, opts),
      apiFetch(`/api/v1/credentials?${qs}`, opts),
      apiFetch(`/api/v1/issues?${qs}&limit=${ISSUE_FETCH_LIMIT}`, opts),
      apiFetch(`/api/v1/projects?${qs}`, opts),
      apiFetch(`/api/v1/workspaces/${ws}/pipelines`, opts),
      apiFetch(`/api/v1/workspaces/${ws}/members`, opts),
      apiFetch(`/api/v1/integrations?${qs}`, opts),
    ]).then(async (settled) => {
      if (ac.signal.aborted) return
      const safeJson = async (r: PromiseSettledResult<Response>) =>
        r.status === "fulfilled" && r.value.ok ? r.value.json() : null
      const [agentsData, crewsData, skillsData, credsData, issuesData, projectsData, routinesData, membersData, integrationsData] =
        await Promise.all(settled.map(safeJson))
      if (ac.signal.aborted) return
      if (agentsData) setAgents(agentsData)
      if (crewsData) setCrews(crewsData)
      if (skillsData) setSkills(skillsData)
      if (credsData) setCredentials(credsData)
      if (issuesData) setIssues(issuesData)
      if (projectsData) setProjects(projectsData)
      if (routinesData) setRoutines(routinesData)
      if (membersData) setMembers(membersData)
      if (integrationsData) setIntegrations(integrationsData)
    })

    return () => ac.abort()
  }, [open, workspaceId])

  function runCommand(fn: () => void) {
    onOpenChange(false)
    fn()
  }

  /** Navigate, and remember it for the Recent section. */
  function go(href: string, label: string, group: string) {
    pushRecent({ href, label, group })
    runCommand(() => router.push(href))
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
      filter={paletteFilter}
      showCloseButton={false}
    >
      <CommandInput placeholder="Search issues, projects, agents..." />
      <CommandList className="max-h-[420px]">
        <CommandEmpty>
          <span className="type-row text-muted-foreground">No results found.</span>
        </CommandEmpty>

        {recent.length > 0 && (
          <CommandGroup heading={<GroupLabel>Recent</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {recent.map((entry) => (
              <CommandItem
                key={entry.href}
                value={`recent ${entry.label} ${entry.group}`}
                className={PALETTE_ITEM_CLASS}
                data-href={entry.href}
                onSelect={() => go(entry.href, entry.label, entry.group)}
              >
                <History className="h-4 w-4 text-muted-foreground" />
                <span className="type-row flex-1 truncate">{entry.label}</span>
                <span className="type-meta text-muted-foreground-soft">{entry.group}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {allowedQuickActions.length > 0 && (
          <CommandGroup heading={<GroupLabel>Quick actions</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {allowedQuickActions.map((action) => (
              <CommandItem
                key={action.href}
                value={action.title}
                keywords={[...action.keywords]}
                data-href={action.href}
                className={PALETTE_ITEM_CLASS}
                onSelect={() => go(action.href, action.title, "Quick actions")}
              >
                <action.icon className="h-4 w-4 text-muted-foreground" />
                <span className="type-row">{action.title}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {filteredIssues.length > 0 && (
          <CommandGroup heading={<GroupLabel>Issues</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {filteredIssues.map((issue) => (
              <CommandItem
                key={issue.id}
                value={`issue ${issue.identifier} ${issue.title}`}
                keywords={[issue.status, issue.priority, issue.assignee_name ?? "", issue.crew_name ?? ""]}
                className={PALETTE_ITEM_CLASS}
                data-href={`/issues/${issue.identifier}`}
                onSelect={() => go(`/issues/${issue.identifier}`, issue.title, "Issues")}
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
                data-href={`/issues?project=${project.id}`}
                onSelect={() => go(`/issues?project=${project.id}`, project.name, "Projects")}
              >
                {/* A project carries a real icon + colour, and every other
                    surface draws them (project-detail-inline). The palette
                    used to paint one generic folder for all of them, which
                    made a list of projects look like a list of one thing. */}
                <span data-project-icon={project.icon ?? "folder"}>
                  <CrewIcon
                    icon={project.icon || "folder"}
                    color={project.color || "blue"}
                    size="sm"
                    className="h-5 w-5 rounded-md [&>svg]:h-3 [&>svg]:w-3"
                  />
                </span>
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
                data-href={`/crews?agent=${encodeURIComponent(agent.slug)}`}
                onSelect={() => go(`/crews?agent=${encodeURIComponent(agent.slug)}`, agent.name, "Agents")}
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
                data-href={`/crews?crew=${encodeURIComponent(crew.slug)}`}
                onSelect={() => go(`/crews?crew=${encodeURIComponent(crew.slug)}`, crew.name, "Crews")}
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
                data-href="/skills"
                onSelect={() => go("/skills", skill.display_name ?? skill.name, "Skills")}
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
                data-href="/credentials"
                onSelect={() => go("/credentials", cred.name, "Credentials")}
              >
                {/* Same brand registry /credentials draws from, so a GitHub
                    token wears the GitHub mark here too. */}
                <CredentialBrand provider={cred.provider} />
                <span className="type-row flex-1 truncate">{cred.name}</span>
                <span className="type-meta text-muted-foreground-soft">
                  {PROVIDER_LABELS[cred.provider] ?? cred.provider}
                </span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {routines.length > 0 && (
          <CommandGroup heading={<GroupLabel>Routines</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {routines.map((routine) => (
              <CommandItem
                key={routine.id}
                value={`routine ${routine.name} ${routine.slug}`}
                keywords={[routine.status, routine.description ?? ""]}
                className={PALETTE_ITEM_CLASS}
                data-href={routineHref(routine.slug)}
                onSelect={() => go(routineHref(routine.slug), routine.name || routine.slug, "Routines")}
              >
                <CalendarClock className="h-4 w-4 text-muted-foreground" />
                <span className="type-row flex-1 truncate">{routine.name || routine.slug}</span>
                <span className="type-meta font-mono text-muted-foreground-soft">{routine.slug}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {members.length > 0 && (
          <CommandGroup heading={<GroupLabel>People</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {members.map((m) => {
              const name = m.user.full_name || m.user.email
              // Deep-link to the person, not just the roster: the settings
              // layout reads ?member= and opens that row (and only that row).
              const href = `/settings?tab=members&member=${encodeURIComponent(m.user.id)}`
              return (
                <CommandItem
                  key={m.id}
                  value={`member ${name} ${m.user.email}`}
                  keywords={[m.role.toLowerCase()]}
                  className={PALETTE_ITEM_CLASS}
                  data-href={href}
                  onSelect={() => go(href, name, "People")}
                >
                  <UserAvatar
                    name={name}
                    email={m.user.email}
                    src={m.user.avatar_url ?? ""}
                    className="h-5 w-5"
                    textClassName="text-[9px]"
                  />
                  <span className="type-row flex-1 truncate">{name}</span>
                  <span className="type-meta text-muted-foreground-soft">{m.role}</span>
                </CommandItem>
              )
            })}
          </CommandGroup>
        )}

        {integrations.length > 0 && (
          <CommandGroup heading={<GroupLabel>Integrations</GroupLabel>} className={PALETTE_GROUP_CLASS}>
            {integrations.map((it) => (
              <CommandItem
                key={it.id}
                value={`integration ${it.display_name || it.name} ${it.name}`}
                keywords={[it.transport, it.crew_name ?? ""]}
                className={PALETTE_ITEM_CLASS}
                data-href="/integrations?tab=tools"
                onSelect={() => go("/integrations?tab=tools", it.display_name || it.name, "Integrations")}
              >
                <MCPLogo name={it.icon || it.name} transport={it.transport} className="h-4 w-4 shrink-0" />
                <span className="type-row flex-1 truncate">{it.display_name || it.name}</span>
                <span className="type-meta text-muted-foreground-soft">
                  {it.crew_name || it.transport}
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
              keywords={["navigate", "page", item.title.toLowerCase(), ...item.keywords]}
              className={PALETTE_ITEM_CLASS}
              data-href={item.href}
              onSelect={() => go(item.href, item.title, "Navigation")}
            >
              <item.icon className="h-4 w-4 text-muted-foreground" />
              <span className="type-row">{item.title}</span>
            </CommandItem>
          ))}
        </CommandGroup>

        <CommandGroup heading={<GroupLabel>Settings</GroupLabel>} className={PALETTE_GROUP_CLASS}>
          {settingsLinks.map((item) => {
            const href = `/settings?tab=${item.key}`
            return (
              <CommandItem
                key={item.key}
                value={`settings ${item.label}`}
                keywords={["settings", "preferences", item.label.toLowerCase()]}
                className={PALETTE_ITEM_CLASS}
                data-href={href}
                onSelect={() => go(href, item.label, "Settings")}
              >
                <item.icon className="h-4 w-4 text-muted-foreground" />
                <span className="type-row">{item.label}</span>
              </CommandItem>
            )
          })}
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
