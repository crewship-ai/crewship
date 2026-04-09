"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import {
  Bot, Network, Zap, Key, Activity, Shield, Settings,
  LayoutDashboard, Plus, ShieldCheck, Store, CircleDot, FolderKanban,
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
  CommandSeparator,
} from "@/components/ui/command"
import { useWorkspace } from "@/hooks/use-workspace"
import { getCrewDotColor, getGradientPalette } from "@/lib/crew-icon"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"

interface AgentResult {
  id: string
  name: string
  slug: string
  role_title: string | null
  status: string
  avatar_seed: string | null
  avatar_style: string | null
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
  NONE: "Custom",
}

const NAV_ITEMS = [
  { title: "Dashboard", href: "/", icon: LayoutDashboard },
  { title: "Orchestration", href: "/orchestration", icon: CircleDot },
  { title: "Crews", href: "/crews", icon: Network },
  { title: "Agents", href: "/agents", icon: Bot },
  { title: "Skills", href: "/skills", icon: Zap },
  { title: "Credentials", href: "/credentials", icon: Key },
  { title: "Runs", href: "/runs", icon: Activity },
  { title: "Audit Log", href: "/audit", icon: Shield },
  { title: "Settings", href: "/settings", icon: Settings },
  { title: "Admin", href: "/admin", icon: ShieldCheck },
  { title: "Marketplace", href: "/marketplace", icon: Store },
]

const QUICK_ACTIONS = [
  { title: "Create new agent", href: "/agents/new", icon: Plus, keywords: ["add", "new", "agent"] },
  { title: "Create new crew", href: "/crews/new", icon: Plus, keywords: ["add", "new", "crew", "team"] },
]

interface CommandPaletteProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function CommandPalette({ open, onOpenChange }: CommandPaletteProps) {
  const router = useRouter()
  const { workspaceId } = useWorkspace()

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
      fetch(`/api/v1/agents?${qs}`, opts),
      fetch(`/api/v1/crews?${qs}`, opts),
      fetch(`/api/v1/skills?${qs}`, opts),
      fetch(`/api/v1/credentials?${qs}`, opts),
      fetch(`/api/v1/issues?${qs}&limit=50`, opts),
      fetch(`/api/v1/projects?${qs}`, opts),
    ]).then(async ([agentsRes, crewsRes, skillsRes, credsRes, issuesRes, projectsRes]) => {
      if (ac.signal.aborted) return
      if (agentsRes.status === "fulfilled" && agentsRes.value.ok)
        setAgents(await agentsRes.value.json())
      if (crewsRes.status === "fulfilled" && crewsRes.value.ok)
        setCrews(await crewsRes.value.json())
      if (skillsRes.status === "fulfilled" && skillsRes.value.ok)
        setSkills(await skillsRes.value.json())
      if (credsRes.status === "fulfilled" && credsRes.value.ok)
        setCredentials(await credsRes.value.json())
      if (issuesRes.status === "fulfilled" && issuesRes.value.ok)
        setIssues(await issuesRes.value.json())
      if (projectsRes.status === "fulfilled" && projectsRes.value.ok)
        setProjects(await projectsRes.value.json())
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
    >
      <CommandInput placeholder="Search issues, projects, agents..." />
      <CommandList>
        <CommandEmpty>No results found.</CommandEmpty>

        <CommandGroup heading="Quick Actions">
          {QUICK_ACTIONS.map((action) => (
            <CommandItem
              key={action.href}
              value={action.title}
              keywords={action.keywords}
              onSelect={() => runCommand(() => router.push(action.href))}
            >
              <action.icon className="h-4 w-4 text-muted-foreground" />
              <span>{action.title}</span>
            </CommandItem>
          ))}
        </CommandGroup>

        {filteredIssues.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Issues">
              {filteredIssues.map((issue) => (
                <CommandItem
                  key={issue.id}
                  value={`issue ${issue.identifier} ${issue.title}`}
                  keywords={[issue.status, issue.priority, issue.assignee_name ?? "", issue.crew_name ?? ""]}
                  onSelect={() => runCommand(() => router.push(`/orchestration/issues/${issue.identifier}`))}
                >
                  <StatusIcon status={issue.status} className="h-4 w-4 shrink-0" />
                  <span className="text-xs font-mono text-muted-foreground shrink-0">{issue.identifier}</span>
                  <span className="flex-1 truncate">{issue.title}</span>
                  <PriorityIcon priority={issue.priority as "urgent" | "high" | "medium" | "low" | "none"} className="h-3.5 w-3.5 shrink-0" />
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        {projects.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Projects">
              {projects.map((project) => (
                <CommandItem
                  key={project.id}
                  value={`project ${project.name} ${project.slug}`}
                  keywords={[project.status]}
                  onSelect={() => runCommand(() => router.push(`/orchestration/projects/${project.id}`))}
                >
                  <div className={cn("h-4 w-4 rounded shrink-0 flex items-center justify-center bg-gradient-to-br", getGradientPalette(project.color).from, getGradientPalette(project.color).to)}>
                    <FolderKanban className={cn("h-2.5 w-2.5", getGradientPalette(project.color).text)} />
                  </div>
                  <span className="flex-1 truncate">{project.name}</span>
                  <span className="text-xs text-muted-foreground">{project.issue_count} issues</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        {agents.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Agents">
              {agents.map((agent) => (
                <CommandItem
                  key={agent.id}
                  value={`agent ${agent.name} ${agent.slug}`}
                  keywords={[agent.role_title ?? "", agent.crew?.name ?? "", agent.status]}
                  onSelect={() => runCommand(() => router.push(`/agents/${agent.id}`))}
                >
                  <img
                    src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
                    alt=""
                    className="h-5 w-5 rounded-full shrink-0"
                  />
                  <span className="flex-1 truncate">{agent.name}</span>
                  {agent.crew && (
                    <span className="flex items-center gap-1.5 text-xs text-muted-foreground truncate max-w-[140px]">
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
          </>
        )}

        {crews.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Crews">
              {crews.map((crew) => (
                <CommandItem
                  key={crew.id}
                  value={`crew ${crew.name} ${crew.slug}`}
                  onSelect={() => runCommand(() => router.push(`/crews/${crew.id}`))}
                >
                  <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="sm" className="h-5 w-5 rounded-md [&>svg]:h-3 [&>svg]:w-3" />
                  <span className="flex-1 truncate">{crew.name}</span>
                  <span className="text-xs text-muted-foreground">
                    {crew._count.agents} agent{crew._count.agents !== 1 ? "s" : ""}
                  </span>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        {skills.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Skills">
              {skills.map((skill) => (
                <CommandItem
                  key={skill.id}
                  value={`skill ${skill.display_name ?? skill.name} ${skill.slug}`}
                  keywords={[skill.category]}
                  onSelect={() => runCommand(() => router.push("/skills"))}
                >
                  <Zap className="h-4 w-4 text-muted-foreground" />
                  <span className="flex-1 truncate">{skill.display_name ?? skill.name}</span>
                  <span className="text-xs text-muted-foreground capitalize">{skill.category.toLowerCase()}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        {credentials.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Credentials">
              {credentials.map((cred) => (
                <CommandItem
                  key={cred.id}
                  value={`credential ${cred.name}`}
                  keywords={[cred.provider, cred.type]}
                  onSelect={() => runCommand(() => router.push("/credentials"))}
                >
                  <Key className="h-4 w-4 text-muted-foreground" />
                  <span className="flex-1 truncate">{cred.name}</span>
                  <span className="text-xs text-muted-foreground">
                    {PROVIDER_LABELS[cred.provider] ?? cred.provider}
                  </span>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        <CommandSeparator />
        <CommandGroup heading="Navigation">
          {NAV_ITEMS.map((item) => (
            <CommandItem
              key={item.href}
              value={`go to ${item.title}`}
              keywords={["navigate", "page", item.title.toLowerCase()]}
              onSelect={() => runCommand(() => router.push(item.href))}
            >
              <item.icon className="h-4 w-4 text-muted-foreground" />
              <span>{item.title}</span>
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  )
}
