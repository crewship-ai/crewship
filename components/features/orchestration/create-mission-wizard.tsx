"use client"

import { useCallback, useEffect, useState } from "react"
import { Plus, Trash2, GripVertical, Loader2, Link2, Diamond, ArrowRight, GitFork, Network } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Checkbox } from "@/components/ui/checkbox"
import { toast } from "sonner"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

interface Crew { id: string; name: string; slug: string }
interface Agent {
  id: string; name: string; slug: string; agent_role: string; crew_id: string
  avatar_seed?: string | null; avatar_style?: string | null
  crew?: { name: string; slug: string; avatar_style?: string | null } | null
}
interface TaskDraft {
  key: string; title: string; description: string; agentId: string
  dependsOnKeys: string[]; complexity: string; tokenBudget: number; needsReview: boolean
}
interface CreateMissionWizardProps { workspaceId: string; onCreated: () => void }

const COMPLEXITIES = ["SIMPLE", "MEDIUM", "COMPLEX"] as const
const cxColor: Record<string, string> = { SIMPLE: "border-l-emerald-500", MEDIUM: "border-l-amber-500", COMPLEX: "border-l-red-500" }
const cxBadge: Record<string, string> = {
  SIMPLE: "bg-emerald-500/15 text-emerald-400 border-emerald-500/30",
  MEDIUM: "bg-amber-500/15 text-amber-400 border-amber-500/30",
  COMPLEX: "bg-red-500/15 text-red-400 border-red-500/30",
}
const cxDot: Record<string, string> = { SIMPLE: "bg-emerald-500", MEDIUM: "bg-amber-500", COMPLEX: "bg-red-500" }

function Ava({ agent, size = 20 }: { agent: Agent; size?: number }) {
  return <img src={getAgentAvatarUrl(agent.avatar_seed || agent.slug, agent.avatar_style ?? agent.crew?.avatar_style ?? null)} alt={agent.name} width={size} height={size} className="rounded-full shrink-0" />
}

let nextKey = 1
function genKey() { return `task-${nextKey++}` }
function empty(): TaskDraft {
  return { key: genKey(), title: "", description: "", agentId: "", dependsOnKeys: [], complexity: "MEDIUM", tokenBudget: 5000, needsReview: false }
}

const patternCards = [
  { value: "CHAIN", label: "Chain", icon: <ArrowRight className="h-5 w-5" /> },
  { value: "PARALLEL", label: "Parallel", icon: <GitFork className="h-5 w-5" /> },
  { value: "ORCHESTRATOR", label: "Orchestrator", icon: <Network className="h-5 w-5" /> },
]

export function CreateMissionWizard({ workspaceId, onCreated }: CreateMissionWizardProps) {
  const [open, setOpen] = useState(false)
  const [crews, setCrews] = useState<Crew[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [crewId, setCrewId] = useState("")
  const [title, setTitle] = useState("")
  const [description, setDescription] = useState("")
  const [missionComplexity, setMissionComplexity] = useState("")
  const [missionPattern, setMissionPattern] = useState("")
  const [tasks, setTasks] = useState<TaskDraft[]>([])
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!open) return
    Promise.all([
      fetch(`/api/v1/crews?workspace_id=${workspaceId}`).then((r) => r.ok ? r.json() : []),
      fetch(`/api/v1/agents?workspace_id=${workspaceId}`).then((r) => r.ok ? r.json() : []),
    ]).then(([c, a]) => { setCrews(c); setAgents(a) }).catch(() => {})
  }, [open, workspaceId])

  const leadAgent = agents.find((a) => a.crew_id === crewId && a.agent_role === "LEAD")
  const byCrewId = new Map<string, Agent[]>()
  for (const a of agents) { const l = byCrewId.get(a.crew_id) || []; l.push(a); byCrewId.set(a.crew_id, l) }

  const addTask = useCallback(() => setTasks((p) => [...p, empty()]), [])
  const removeTask = useCallback((key: string) => {
    setTasks((p) => p.filter((t) => t.key !== key).map((t) => ({ ...t, dependsOnKeys: t.dependsOnKeys.filter((k) => k !== key) })))
  }, [])
  const updateTask = useCallback((key: string, u: Partial<TaskDraft>) => {
    setTasks((p) => p.map((t) => t.key === key ? { ...t, ...u } : t))
  }, [])
  const toggleDep = useCallback((tk: string, dk: string) => {
    setTasks((p) => p.map((t) => {
      if (t.key !== tk) return t
      const has = t.dependsOnKeys.includes(dk)
      return { ...t, dependsOnKeys: has ? t.dependsOnKeys.filter((k) => k !== dk) : [...t.dependsOnKeys, dk] }
    }))
  }, [])

  async function handleSubmit() {
    if (!title.trim()) { toast.error("Mission title is required"); return }
    if (!crewId) { toast.error("Select a crew"); return }
    if (!leadAgent) { toast.error("Selected crew has no LEAD agent"); return }
    if (tasks.some((t) => !t.title.trim())) { toast.error("All tasks need a title"); return }
    setSaving(true)
    try {
      const mRes = await fetch(`/api/v1/crews/${crewId}/missions?workspace_id=${workspaceId}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title: title.trim(), description: description.trim() || undefined, lead_agent_id: leadAgent.id, complexity: missionComplexity || undefined, pattern: missionPattern || undefined }),
      })
      if (!mRes.ok) { const b = await mRes.json().catch(() => null); toast.error(b?.detail ?? "Failed to create mission"); return }
      const mission = await mRes.json()
      const keyToId = new Map<string, string>()
      for (let i = 0; i < tasks.length; i++) {
        const t = tasks[i]
        const depIds = t.dependsOnKeys.map((k) => keyToId.get(k)).filter(Boolean) as string[]
        const tb: Record<string, unknown> = { title: t.title.trim(), task_order: i + 1, complexity: t.complexity || "MEDIUM", token_budget: t.tokenBudget || 5000, needs_review: t.needsReview || false }
        if (t.description.trim()) tb.description = t.description.trim()
        if (t.agentId) tb.assigned_agent_id = t.agentId
        if (depIds.length > 0) tb.depends_on = depIds
        const tRes = await fetch(`/api/v1/crews/${crewId}/missions/${mission.id}/tasks?workspace_id=${workspaceId}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(tb) })
        if (tRes.ok) { const c = await tRes.json(); keyToId.set(t.key, c.id) }
      }
      toast.success(`Mission created with ${tasks.length} tasks`)
      setOpen(false); resetForm(); onCreated()
    } catch { toast.error("Failed to create mission") } finally { setSaving(false) }
  }

  function resetForm() { setTitle(""); setDescription(""); setCrewId(""); setMissionComplexity(""); setMissionPattern(""); setTasks([]) }

  function AgentDropdown({ value, onChange }: { value: string; onChange: (v: string) => void }) {
    return (
      <Select value={value || "none"} onValueChange={(v) => onChange(v === "none" ? "" : v)}>
        <SelectTrigger className="h-8 text-xs bg-card border-border"><SelectValue placeholder="Unassigned" /></SelectTrigger>
        <SelectContent className="bg-card border-border">
          <SelectItem value="none">Unassigned</SelectItem>
          {[...byCrewId.entries()].map(([cId, agts]) => {
            const crew = crews.find((c) => c.id === cId)
            return (<SelectGroup key={cId}>
              <SelectLabel className="text-[10px] uppercase tracking-wider text-muted-foreground">{crew?.name ?? "Unknown"}</SelectLabel>
              {agts.map((a) => (<SelectItem key={a.id} value={a.id}><span className="flex items-center gap-1.5"><Ava agent={a} size={16} />{a.name} <span className="text-muted-foreground">@{a.slug}</span></span></SelectItem>))}
            </SelectGroup>)
          })}
        </SelectContent>
      </Select>
    )
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { setOpen(v); if (!v) resetForm() }}>
      <DialogTrigger asChild>
        <Button size="sm" className="gap-1.5"><Plus className="h-3.5 w-3.5" />New Mission</Button>
      </DialogTrigger>
      <DialogContent className="max-w-2xl max-h-[85vh] flex flex-col bg-card text-foreground border-border">
        <DialogHeader>
          <DialogTitle className="text-foreground">Create Mission</DialogTitle>
          <DialogDescription className="text-muted-foreground">Define a mission with tasks, assign agents, and set dependencies.</DialogDescription>
        </DialogHeader>
        <ScrollArea className="flex-1 -mx-6 px-6">
          <div className="space-y-5 py-2">
            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label className="text-muted-foreground text-xs uppercase tracking-wider">Crew</Label>
                <Select value={crewId} onValueChange={setCrewId}>
                  <SelectTrigger className="bg-card border-border"><SelectValue placeholder="Select crew" /></SelectTrigger>
                  <SelectContent className="bg-card border-border">{crews.map((c) => <SelectItem key={c.id} value={c.id}>{c.name} ({c.slug})</SelectItem>)}</SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label className="text-muted-foreground text-xs uppercase tracking-wider">Lead Agent</Label>
                <div className="h-9 px-3 flex items-center gap-2 rounded-md border border-border bg-muted/30 text-sm">
                  {leadAgent ? <><Ava agent={leadAgent} size={18} /><span>@{leadAgent.slug} — {leadAgent.name}</span></> : <span className="text-muted-foreground">{crewId ? "No LEAD agent in crew" : "Select a crew first"}</span>}
                </div>
              </div>
            </div>
            <div className="space-y-2">
              <Label className="text-muted-foreground text-xs uppercase tracking-wider">Mission Title</Label>
              <Input placeholder="e.g. Build authentication system" value={title} onChange={(e) => setTitle(e.target.value)} className="bg-card border-border" />
            </div>
            <div className="space-y-2">
              <Label className="text-muted-foreground text-xs uppercase tracking-wider">Description</Label>
              <Textarea placeholder="Describe what this mission should accomplish..." value={description} onChange={(e) => setDescription(e.target.value)} rows={2} className="bg-card border-border" />
            </div>
            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label className="text-muted-foreground text-xs uppercase tracking-wider">Complexity</Label>
                <Select value={missionComplexity || "none"} onValueChange={(v) => setMissionComplexity(v === "none" ? "" : v)}>
                  <SelectTrigger className="bg-card border-border"><SelectValue placeholder="Not set" /></SelectTrigger>
                  <SelectContent className="bg-card border-border">
                    <SelectItem value="none">Not set</SelectItem>
                    {COMPLEXITIES.map((c) => <SelectItem key={c} value={c}><span className="flex items-center gap-2"><span className={`inline-block w-2 h-2 rounded-full ${cxDot[c]}`} />{c}</span></SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label className="text-muted-foreground text-xs uppercase tracking-wider">Execution Pattern</Label>
                <div className="flex gap-2">
                  {patternCards.map((p) => (
                    <button key={p.value} type="button" onClick={() => setMissionPattern(missionPattern === p.value ? "" : p.value)}
                      className={`flex-1 flex flex-col items-center gap-1 p-2 rounded-md border text-xs transition-colors ${missionPattern === p.value ? "border-primary bg-primary/10 text-primary" : "border-border text-muted-foreground hover:border-muted-foreground/50 hover:text-foreground"}`}>
                      {p.icon}<span className="font-medium">{p.label}</span>
                    </button>
                  ))}
                </div>
              </div>
            </div>
            <Separator className="bg-border" />
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label className="text-sm font-semibold text-foreground">Tasks ({tasks.length})</Label>
                <Button size="sm" variant="outline" onClick={addTask} className="gap-1.5 h-7 text-xs border-border"><Plus className="h-3 w-3" /> Add Task</Button>
              </div>
              {tasks.length === 0 && <div className="text-center py-8 text-sm text-muted-foreground border border-dashed border-border rounded-lg">No tasks yet. Click &quot;Add Task&quot; to build the workflow.</div>}
              {tasks.map((task, idx) => (
                <div key={task.key} className={`rounded-lg border border-border bg-muted/20 p-3 space-y-2.5 border-l-[3px] ${cxColor[task.complexity] || "border-l-border"}`}>
                  <div className="flex items-center gap-2">
                    <GripVertical className="h-4 w-4 text-muted-foreground shrink-0 cursor-grab" />
                    <Badge variant="outline" className="shrink-0 text-[10px] border-border">#{idx + 1}</Badge>
                    <Input placeholder="Task title" value={task.title} onChange={(e) => updateTask(task.key, { title: e.target.value })} className="h-8 text-sm bg-card border-border flex-1" />
                    {task.needsReview && <Diamond className="h-3.5 w-3.5 text-amber-400 shrink-0" />}
                    <Badge variant="outline" className={`text-[10px] shrink-0 ${cxBadge[task.complexity] || ""}`}>{task.complexity}</Badge>
                    <Button size="icon" variant="ghost" className="h-7 w-7 shrink-0 text-muted-foreground hover:text-red-400" onClick={() => removeTask(task.key)}><Trash2 className="h-3.5 w-3.5" /></Button>
                  </div>
                  <div className="grid grid-cols-3 gap-3 pl-6">
                    <div className="space-y-1">
                      <Label className="text-[10px] text-muted-foreground uppercase tracking-wider">Assign to</Label>
                      <AgentDropdown value={task.agentId} onChange={(v) => updateTask(task.key, { agentId: v })} />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-[10px] text-muted-foreground uppercase tracking-wider">Complexity</Label>
                      <Select value={task.complexity} onValueChange={(v) => updateTask(task.key, { complexity: v })}>
                        <SelectTrigger className="h-8 text-xs bg-card border-border"><SelectValue /></SelectTrigger>
                        <SelectContent className="bg-card border-border">{COMPLEXITIES.map((c) => <SelectItem key={c} value={c}>{c}</SelectItem>)}</SelectContent>
                      </Select>
                    </div>
                    <div className="space-y-1">
                      <Label className="text-[10px] text-muted-foreground uppercase tracking-wider">Token Budget</Label>
                      <div className="relative">
                        <Input type="number" value={task.tokenBudget} onChange={(e) => updateTask(task.key, { tokenBudget: parseInt(e.target.value) || 0 })} className="h-8 text-xs bg-card border-border pr-10" />
                        <span className="absolute right-3 top-1/2 -translate-y-1/2 text-[10px] text-muted-foreground">tok</span>
                      </div>
                    </div>
                  </div>
                  <div className="flex items-start gap-6 pl-6">
                    <label className="flex items-center gap-2 text-xs cursor-pointer text-muted-foreground hover:text-foreground transition-colors">
                      <Checkbox checked={task.needsReview} onCheckedChange={(v) => updateTask(task.key, { needsReview: !!v })} className="h-3.5 w-3.5" />
                      Requires human approval
                    </label>
                    {idx > 0 && <div className="space-y-1">
                      <Label className="text-[10px] text-muted-foreground flex items-center gap-1 uppercase tracking-wider"><Link2 className="h-3 w-3" /> Depends on</Label>
                      <div className="flex flex-wrap gap-x-3 gap-y-1">
                        {tasks.slice(0, idx).map((prev) => (
                          <label key={prev.key} className="flex items-center gap-1.5 text-xs cursor-pointer text-muted-foreground">
                            <Checkbox checked={task.dependsOnKeys.includes(prev.key)} onCheckedChange={() => toggleDep(task.key, prev.key)} className="h-3.5 w-3.5" />
                            <span className="truncate max-w-[120px]">#{tasks.indexOf(prev) + 1} {prev.title || "Untitled"}</span>
                          </label>
                        ))}
                      </div>
                    </div>}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </ScrollArea>
        <DialogFooter className="pt-4 border-t border-border">
          <Button variant="outline" onClick={() => setOpen(false)} className="border-border">Cancel</Button>
          <Button onClick={handleSubmit} disabled={saving || !title.trim() || !crewId || !leadAgent}>
            {saving ? <><Loader2 className="h-3.5 w-3.5 animate-spin mr-1.5" /> Creating...</> : <>Create Mission{tasks.length > 0 && ` with ${tasks.length} tasks`}</>}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
