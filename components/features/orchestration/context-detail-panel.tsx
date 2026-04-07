"use client"

import { useState, useEffect } from "react"
import {
  X, CheckCircle2, XCircle, Clock, AlertTriangle, ArrowRight,
  GitBranch, Users, Box, ChevronDown, ChevronRight, RotateCcw,
  SkipForward, MousePointerClick,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Progress } from "@/components/ui/progress"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@/components/ui/collapsible"
import type { Mission, MissionTask, MissionTaskStatus, TaskComplexity, EvaluationStatus } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"

const crewBgClass: Record<string, string> = {
  blue: "bg-blue-500", emerald: "bg-emerald-500", violet: "bg-violet-500", amber: "bg-amber-500",
  rose: "bg-rose-500", cyan: "bg-cyan-500", lime: "bg-lime-500", fuchsia: "bg-fuchsia-500",
}

export type DetailContext =
  | { type: "task"; task: MissionTask; mission: Mission; allTasks: MissionTask[] }
  | { type: "crew"; crew: CrewSummary; agents: AgentSummary[]; connections: CrewConnection[] }
  | { type: "mission"; mission: Mission }
  | { type: "none" }

export interface ContextDetailPanelProps {
  context: DetailContext
  onTaskAction?: (action: "edit" | "retry" | "skip", taskId: string, missionId: string) => void
  onClose?: () => void
}

const STATUS_COLORS: Record<MissionTaskStatus, string> = {
  PENDING: "bg-muted text-muted-foreground",
  BLOCKED: "bg-amber-500/20 text-amber-400",
  IN_PROGRESS: "bg-cyan-500/20 text-cyan-400",
  COMPLETED: "bg-emerald-500/20 text-emerald-400",
  FAILED: "bg-red-500/20 text-red-400",
  SKIPPED: "bg-muted text-muted-foreground",
}

const COMPLEXITY_COLORS: Record<TaskComplexity, string> = {
  SIMPLE: "bg-emerald-500/20 text-emerald-400",
  MEDIUM: "bg-amber-500/20 text-amber-400",
  COMPLEX: "bg-red-500/20 text-red-400",
}

function formatCost(cost: number | null): string {
  if (cost == null) return "--"
  return `$${cost.toFixed(4)}`
}

function formatTokens(tokens: number | null): string {
  if (tokens == null) return "--"
  if (tokens >= 1000) return `${(tokens / 1000).toFixed(1)}k`
  return String(tokens)
}

function parseDependsOn(raw: string): string[] {
  try {
    const parsed = JSON.parse(raw) as unknown
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === "string") : []
  } catch {
    return []
  }
}

function EvalBadge({ status }: { status: EvaluationStatus | null }) {
  if (!status) return null
  if (status === "PASSED") return <span className="inline-flex items-center gap-1 text-emerald-400 text-xs"><CheckCircle2 className="size-3" /> Passed</span>
  if (status === "FAILED") return <span className="inline-flex items-center gap-1 text-red-400 text-xs"><XCircle className="size-3" /> Failed</span>
  return <span className="inline-flex items-center gap-1 text-muted-foreground text-xs"><Clock className="size-3" /> Pending</span>
}

function CollapsibleSection({ title, children, defaultOpen = false, tinted }: {
  title: string; children: React.ReactNode; defaultOpen?: boolean; tinted?: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground hover:text-foreground/80 transition-colors w-full py-1">
        {open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
        {title}
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className={cn("mt-1 rounded-md p-2 text-xs font-mono whitespace-pre-wrap", tinted ? "bg-red-500/10 text-red-300" : "bg-accent/50 text-muted-foreground")}>
          {children}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

function TaskDetail({ task, mission, allTasks, onAction }: {
  task: MissionTask; mission: Mission; allTasks: MissionTask[]
  onAction?: (action: "edit" | "retry" | "skip", taskId: string, missionId: string) => void
}) {
  const deps = parseDependsOn(task.depends_on)
  const blockedBy = deps.map(id => allTasks.find(t => t.id === id)).filter(Boolean) as MissionTask[]
  const blocks = allTasks.filter(t => parseDependsOn(t.depends_on).includes(task.id))
  const budgetPct = task.token_budget != null && task.token_budget > 0 && task.tokens_used != null ? Math.min(100, Math.round((task.tokens_used / task.token_budget) * 100)) : null

  return (
    <div className="space-y-4">
      <div>
        <h3 className="text-sm font-semibold text-foreground leading-tight">{task.title}</h3>
        <div className="flex items-center gap-2 mt-1.5 flex-wrap">
          <Badge className={cn("text-[10px]", STATUS_COLORS[task.status])}>{task.status.replace("_", " ")}</Badge>
          {task.complexity && <Badge className={cn("text-[10px]", COMPLEXITY_COLORS[task.complexity])}>{task.complexity}</Badge>}
          {task.iteration != null && task.max_iterations != null && task.max_iterations > 1 && (
            <span className="text-[10px] text-muted-foreground">Iter {task.iteration}/{task.max_iterations}</span>
          )}
        </div>
      </div>

      {(task.agent_name || task.agent_slug) && (
        <div className="text-xs text-muted-foreground">
          <span className="text-foreground/80">{task.agent_name ?? task.agent_slug}</span>
          {task.agent_slug && <span className="text-muted-foreground/70 ml-1">@{task.agent_slug}</span>}
        </div>
      )}

      {budgetPct != null && (
        <div className="space-y-1">
          <div className="flex justify-between text-[10px] text-muted-foreground">
            <span>Tokens</span>
            <span>{formatTokens(task.tokens_used)} / {formatTokens(task.token_budget)} ({budgetPct}%)</span>
          </div>
          <Progress value={budgetPct} className="h-1.5 bg-accent" />
        </div>
      )}

      <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs">
        <div className="text-muted-foreground">Cost</div>
        <div className="text-foreground/80">{formatCost(task.estimated_cost)}</div>
        {task.confidence != null && (<>
          <div className="text-muted-foreground">Confidence</div>
          <div className="text-foreground/80">{Math.round(task.confidence * 100)}%</div>
        </>)}
        <div className="text-muted-foreground">Evaluation</div>
        <div><EvalBadge status={task.evaluation_status} /></div>
        {task.duration_ms != null && (<>
          <div className="text-muted-foreground">Duration</div>
          <div className="text-foreground/80">{(task.duration_ms / 1000).toFixed(1)}s</div>
        </>)}
      </div>

      {blockedBy.length > 0 && (
        <div className="space-y-1">
          <div className="text-[10px] font-medium text-muted-foreground uppercase tracking-wider">Dependencies</div>
          {blockedBy.map(d => (
            <div key={d.id} className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <GitBranch className="size-3" />
              <span className="truncate">{d.title}</span>
              <Badge className={cn("text-[9px] ml-auto", STATUS_COLORS[d.status])}>{d.status}</Badge>
            </div>
          ))}
        </div>
      )}

      {blocks.length > 0 && (
        <div className="space-y-1">
          <div className="text-[10px] font-medium text-muted-foreground uppercase tracking-wider">Blocks</div>
          {blocks.map(b => (
            <div key={b.id} className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <ArrowRight className="size-3" />
              <span className="truncate">{b.title}</span>
              <Badge className={cn("text-[9px] ml-auto", STATUS_COLORS[b.status])}>{b.status}</Badge>
            </div>
          ))}
        </div>
      )}

      {task.result_summary && <CollapsibleSection title="Result summary">{task.result_summary}</CollapsibleSection>}
      {task.error_message && <CollapsibleSection title="Error" tinted>{task.error_message}</CollapsibleSection>}

      {onAction && (
        <div className="flex gap-2 pt-1">
          {task.status === "FAILED" && (
            <Button variant="outline" size="xs" onClick={() => onAction("retry", task.id, mission.id)}><RotateCcw className="size-3" /> Retry</Button>
          )}
          {(task.status === "PENDING" || task.status === "BLOCKED" || task.status === "FAILED") && (
            <Button variant="ghost" size="xs" onClick={() => onAction("skip", task.id, mission.id)}><SkipForward className="size-3" /> Skip</Button>
          )}
        </div>
      )}
    </div>
  )
}

function CrewDetail({ crew, agents, connections }: { crew: CrewSummary; agents: AgentSummary[]; connections: CrewConnection[] }) {
  const crewAgents = agents.filter(a => a.crew_id === crew.id)
  const crewConns = connections.filter(c => c.from_crew_id === crew.id || c.to_crew_id === crew.id)

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        {crew.color && <span className={cn("size-2.5 rounded-full", crewBgClass[crew.color] || "bg-muted-foreground")} />}
        <h3 className="text-sm font-semibold text-foreground">{crew.name}</h3>
      </div>
      <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs">
        <div className="text-muted-foreground">Agents</div>
        <div className="text-foreground/80">{crew._count?.agents ?? crewAgents.length}</div>
        <div className="text-muted-foreground">Container</div>
        <div className="text-foreground/80 font-mono text-[11px]">crewship-team-{crew.slug}</div>
      </div>
      {crewConns.length > 0 && (
        <div className="space-y-1">
          <div className="text-[10px] font-medium text-muted-foreground uppercase tracking-wider">Connections</div>
          {crewConns.map(c => {
            const other = c.from_crew_id === crew.id ? c.to_crew_name : c.from_crew_name
            const dir = c.direction === "bidirectional" ? "Bidirectional" : (c.from_crew_id === crew.id ? "Outgoing" : "Incoming")
            return (
              <div key={c.id} className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <ArrowRight className="size-3" />
                <span>{other}</span>
                <span className="text-muted-foreground/70 ml-auto">{dir}</span>
              </div>
            )
          })}
        </div>
      )}
      {crewAgents.length > 0 && (
        <div className="space-y-1">
          <div className="text-[10px] font-medium text-muted-foreground uppercase tracking-wider">Agents</div>
          {crewAgents.map(a => (
            <div key={a.id} className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Users className="size-3" />
              <span className="text-foreground/80">{a.name}</span>
              <span className="text-muted-foreground/70">@{a.slug}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function MissionDetail({ mission }: { mission: Mission }) {
  const completed = mission.tasks.filter(t => t.status === "COMPLETED").length
  const total = mission.tasks.length
  const pct = total > 0 ? Math.round((completed / total) * 100) : 0

  return (
    <div className="space-y-4">
      <h3 className="text-sm font-semibold text-foreground">{mission.title}</h3>
      <Badge className={cn("text-[10px]", STATUS_COLORS[mission.status as MissionTaskStatus] ?? "bg-muted text-muted-foreground")}>
        {mission.status}
      </Badge>
      <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs">
        <div className="text-muted-foreground">Lead</div>
        <div className="text-foreground/80">@{mission.lead_agent_slug}</div>
        {mission.pattern && (<>
          <div className="text-muted-foreground">Pattern</div>
          <div className="text-foreground/80">{mission.pattern}</div>
        </>)}
        {mission.complexity && (<>
          <div className="text-muted-foreground">Complexity</div>
          <div><Badge className={cn("text-[10px]", COMPLEXITY_COLORS[mission.complexity])}>{mission.complexity}</Badge></div>
        </>)}
        <div className="text-muted-foreground">Total tokens</div>
        <div className="text-foreground/80">{formatTokens(mission.total_token_count)}</div>
        <div className="text-muted-foreground">Total cost</div>
        <div className="text-foreground/80">{formatCost(mission.total_estimated_cost)}</div>
      </div>
      <div className="space-y-1">
        <div className="flex justify-between text-[10px] text-muted-foreground">
          <span>Tasks</span>
          <span>{completed}/{total} ({pct}%)</span>
        </div>
        <Progress value={pct} className="h-1.5 bg-accent" />
      </div>
    </div>
  )
}

export function ContextDetailPanel({ context, onTaskAction, onClose }: ContextDetailPanelProps) {
  const [activeDetailTab, setActiveDetailTab] = useState("detail")

  useEffect(() => {
    setActiveDetailTab("detail")
  }, [context])

  return (
    <div className="h-full flex flex-col bg-card border-l border-border">
      <div className="flex items-center justify-between px-3 py-2 border-b border-border">
        <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
          {context.type === "task" ? "Task" : context.type === "crew" ? "Crew" : context.type === "mission" ? "Mission" : "Details"}
        </span>
        {onClose && (
          <Button variant="ghost" size="icon-xs" onClick={onClose} aria-label="Close details" className="text-muted-foreground/70 hover:text-foreground/70">
            <X className="size-3.5" />
          </Button>
        )}
      </div>

      <Tabs value={activeDetailTab} onValueChange={setActiveDetailTab} className="flex-1 min-h-0">
        <TabsList variant="line" className="px-3 border-b border-border">
          <TabsTrigger value="detail" className="text-xs">Detail</TabsTrigger>
          <TabsTrigger value="logs" className="text-xs">Logs</TabsTrigger>
          <TabsTrigger value="trace" className="text-xs">Trace</TabsTrigger>
        </TabsList>

        <TabsContent value="detail" className="flex-1 min-h-0">
          <ScrollArea className="h-full">
            <div className="p-3">
              {context.type === "task" && (
                <TaskDetail task={context.task} mission={context.mission} allTasks={context.allTasks} onAction={onTaskAction} />
              )}
              {context.type === "crew" && (
                <CrewDetail crew={context.crew} agents={context.agents} connections={context.connections} />
              )}
              {context.type === "mission" && <MissionDetail mission={context.mission} />}
              {context.type === "none" && (
                <div className="flex flex-col items-center justify-center py-12 text-muted-foreground/70">
                  <MousePointerClick className="size-8 mb-2" />
                  <p className="text-xs">Select a node to view details</p>
                </div>
              )}
            </div>
          </ScrollArea>
        </TabsContent>

        <TabsContent value="logs" className="flex-1 min-h-0">
          <div className="flex flex-col items-center justify-center h-full py-12 text-muted-foreground/70">
            <AlertTriangle className="size-6 mb-2" />
            <p className="text-xs">Coming soon</p>
          </div>
        </TabsContent>

        <TabsContent value="trace" className="flex-1 min-h-0">
          <div className="flex flex-col items-center justify-center h-full py-12 text-muted-foreground/70">
            <Box className="size-6 mb-2" />
            <p className="text-xs">Coming soon</p>
          </div>
        </TabsContent>
      </Tabs>
    </div>
  )
}
