"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { EmptyState } from "@/components/layout/empty-state"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  LayoutTemplate,
  Plus,
  Download,
  Upload,
  Trash2,
  Copy,
  ArrowRight,
  GitBranch,
  Repeat,
  GitMerge,
} from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import type { WorkflowTemplate, TemplateDefinition } from "@/lib/types/template"

interface TemplateGalleryProps {
  workspaceId: string
}

const iconMap: Record<string, React.ElementType> = {
  "arrow-right": ArrowRight,
  "git-branch": GitBranch,
  repeat: Repeat,
  "git-merge": GitMerge,
}

function TemplateMiniGraph({ steps }: { steps: TemplateDefinition["steps"] }) {
  if (steps.length === 0) return null

  // Compute levels
  const levels = new Map<string, number>()
  function getLevel(stepId: string): number {
    if (levels.has(stepId)) return levels.get(stepId)!
    const step = steps.find((s) => s.id === stepId)
    const deps = step?.depends_on || []
    if (deps.length === 0) { levels.set(stepId, 0); return 0 }
    const level = Math.max(...deps.map(getLevel)) + 1
    levels.set(stepId, level)
    return level
  }
  for (const step of steps) getLevel(step.id)

  const levelGroups = new Map<number, typeof steps>()
  for (const step of steps) {
    const level = levels.get(step.id) || 0
    if (!levelGroups.has(level)) levelGroups.set(level, [])
    levelGroups.get(level)!.push(step)
  }

  const maxLevel = Math.max(...levels.values(), 0)
  const nodePositions = new Map<string, { x: number; y: number }>()

  const colWidth = 80
  const rowHeight = 30
  const nodeR = 8

  for (const [level, groupSteps] of levelGroups) {
    groupSteps.forEach((step, idx) => {
      nodePositions.set(step.id, {
        x: 20 + level * colWidth,
        y: 15 + idx * rowHeight,
      })
    })
  }

  const maxRows = Math.max(...[...levelGroups.values()].map((g) => g.length))
  const svgW = 40 + (maxLevel + 1) * colWidth
  const svgH = Math.max(40, maxRows * rowHeight + 10)

  return (
    <svg width="100%" height={svgH} viewBox={`0 0 ${svgW} ${svgH}`} className="overflow-visible">
      {/* Edges */}
      {steps.flatMap((step) =>
        (step.depends_on || []).map((depId) => {
          const from = nodePositions.get(depId)
          const to = nodePositions.get(step.id)
          if (!from || !to) return null
          return (
            <line
              key={`${depId}-${step.id}`}
              x1={from.x + nodeR} y1={from.y}
              x2={to.x - nodeR} y2={to.y}
              stroke="hsl(var(--border))"
              strokeWidth={1.5}
              markerEnd="url(#mini-arrow)"
            />
          )
        })
      )}
      {/* Nodes */}
      {steps.map((step) => {
        const pos = nodePositions.get(step.id)
        if (!pos) return null
        const hasLoop = step.max_iterations && step.max_iterations > 1
        return (
          <g key={step.id}>
            <circle
              cx={pos.x} cy={pos.y} r={nodeR}
              fill={hasLoop ? "hsl(var(--chart-4))" : "hsl(var(--chart-1))"}
              stroke="hsl(var(--border))"
              strokeWidth={1}
            />
            <text
              x={pos.x} y={pos.y + nodeR + 10}
              textAnchor="middle"
              className="fill-muted-foreground text-[8px]"
            >
              {step.agent_role || step.id}
            </text>
          </g>
        )
      })}
      <defs>
        <marker id="mini-arrow" markerWidth="6" markerHeight="6" refX="6" refY="3" orient="auto">
          <path d="M0,0 L6,3 L0,6" fill="none" stroke="hsl(var(--border))" strokeWidth="1" />
        </marker>
      </defs>
    </svg>
  )
}

export function TemplateGallery({ workspaceId }: TemplateGalleryProps) {
  const [templates, setTemplates] = useState<WorkflowTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [createOpen, setCreateOpen] = useState(false)
  const [newName, setNewName] = useState("")
  const [newDesc, setNewDesc] = useState("")
  const [newJson, setNewJson] = useState("")
  const [saving, setSaving] = useState(false)
  const importRef = useRef<HTMLInputElement>(null)

  const fetchTemplates = useCallback(async () => {
    try {
      const res = await fetch(`/api/v1/templates?workspace_id=${workspaceId}`)
      if (res.ok) {
        setTemplates(await res.json())
      }
    } catch {
      // ignore
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    fetchTemplates()
  }, [fetchTemplates])

  async function handleCreate() {
    if (!newName.trim()) { toast.error("Name is required"); return }
    let parsedJson: TemplateDefinition
    try {
      parsedJson = JSON.parse(newJson)
      if (!parsedJson.steps || !Array.isArray(parsedJson.steps)) throw new Error("steps required")
    } catch {
      toast.error("Invalid template JSON. Must have { name, description, steps: [...] }")
      return
    }

    setSaving(true)
    try {
      const res = await fetch(`/api/v1/templates?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: newName.trim(),
          description: newDesc.trim() || null,
          template_json: parsedJson,
        }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to create template")
        return
      }
      toast.success("Template created")
      setCreateOpen(false)
      setNewName("")
      setNewDesc("")
      setNewJson("")
      fetchTemplates()
    } catch {
      toast.error("Failed to create template")
    } finally {
      setSaving(false)
    }
  }

  async function handleDelete(id: string) {
    try {
      const res = await fetch(`/api/v1/templates/${id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (res.ok || res.status === 204) {
        toast.success("Template deleted")
        fetchTemplates()
      } else {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Cannot delete this template")
      }
    } catch {
      toast.error("Failed to delete")
    }
  }

  function handleExport(tmpl: WorkflowTemplate) {
    const blob = new Blob([JSON.stringify(tmpl.template_json, null, 2)], { type: "application/json" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `${tmpl.name}-template.json`
    a.click()
    URL.revokeObjectURL(url)
    toast.success("Template exported")
  }

  function handleImport(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = () => {
      try {
        const parsed = JSON.parse(reader.result as string) as TemplateDefinition
        setNewName(parsed.name || file.name.replace(/\.json$/, ""))
        setNewDesc(parsed.description || "")
        setNewJson(JSON.stringify(parsed, null, 2))
        setCreateOpen(true)
      } catch {
        toast.error("Invalid template JSON file")
      }
    }
    reader.readAsText(file)
    e.target.value = ""
  }

  function handleDuplicate(tmpl: WorkflowTemplate) {
    setNewName(`${tmpl.name} (copy)`)
    setNewDesc(tmpl.description || "")
    setNewJson(JSON.stringify(tmpl.template_json, null, 2))
    setCreateOpen(true)
  }

  if (loading) {
    return (
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <Card key={i} className="animate-pulse"><CardContent className="h-48" /></Card>
        ))}
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          {templates.length} template{templates.length !== 1 ? "s" : ""} available
        </p>
        <div className="flex items-center gap-2">
          <input
            ref={importRef}
            type="file"
            accept=".json"
            className="hidden"
            onChange={handleImport}
          />
          <Button variant="outline" size="sm" onClick={() => importRef.current?.click()}>
            <Upload className="h-4 w-4 mr-1" />
            Import
          </Button>
          <Button size="sm" onClick={() => { setNewName(""); setNewDesc(""); setNewJson(""); setCreateOpen(true) }}>
            <Plus className="h-4 w-4 mr-1" />
            New Template
          </Button>
        </div>
      </div>

      {templates.length === 0 ? (
        <Card>
          <CardContent className="py-12">
            <EmptyState
              icon={LayoutTemplate}
              title="No templates"
              description="Create or import a workflow template to get started"
            />
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {templates.map((tmpl) => {
            const Icon = iconMap[tmpl.icon || ""] || LayoutTemplate
            const steps = tmpl.template_json?.steps || []
            return (
              <Card key={tmpl.id} className="group hover:border-foreground/20 transition-colors">
                <CardHeader className="pb-2">
                  <div className="flex items-start justify-between">
                    <div className="flex items-center gap-2">
                      <div
                        className="w-8 h-8 rounded-lg flex items-center justify-center"
                        style={{ backgroundColor: (tmpl.color || "#6b7280") + "20" }}
                      >
                        <Icon className="h-4 w-4" style={{ color: tmpl.color || "#6b7280" }} />
                      </div>
                      <div>
                        <CardTitle className="text-sm">{tmpl.name}</CardTitle>
                        {tmpl.is_builtin && (
                          <Badge variant="outline" className="text-[10px] px-1 py-0 mt-0.5">builtin</Badge>
                        )}
                      </div>
                    </div>
                    <div className="flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                      <Button variant="ghost" size="icon" className="h-7 w-7" onClick={() => handleExport(tmpl)}>
                        <Download className="h-3.5 w-3.5" />
                      </Button>
                      <Button variant="ghost" size="icon" className="h-7 w-7" onClick={() => handleDuplicate(tmpl)}>
                        <Copy className="h-3.5 w-3.5" />
                      </Button>
                      {!tmpl.is_builtin && (
                        <Button variant="ghost" size="icon" className="h-7 w-7 text-destructive" onClick={() => handleDelete(tmpl.id)}>
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                      )}
                    </div>
                  </div>
                </CardHeader>
                <CardContent className="space-y-3">
                  {tmpl.description && (
                    <p className="text-xs text-muted-foreground line-clamp-2">{tmpl.description}</p>
                  )}
                  <div className={cn(
                    "rounded-lg border bg-muted/30 p-2 min-h-[60px]",
                    "flex items-center justify-center"
                  )}>
                    <TemplateMiniGraph steps={steps} />
                  </div>
                  <div className="flex items-center gap-2 text-xs text-muted-foreground">
                    <span>{steps.length} step{steps.length !== 1 ? "s" : ""}</span>
                    {steps.some((s) => (s.max_iterations || 0) > 1) && (
                      <Badge variant="secondary" className="text-[10px] px-1 py-0">
                        <Repeat className="h-3 w-3 mr-0.5" />
                        loop
                      </Badge>
                    )}
                    {steps.some((s) => (s.depends_on?.length || 0) === 0) && steps.filter((s) => (s.depends_on?.length || 0) === 0).length > 1 && (
                      <Badge variant="secondary" className="text-[10px] px-1 py-0">parallel</Badge>
                    )}
                  </div>
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}

      {/* Create Template Dialog */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Create Template</DialogTitle>
            <DialogDescription>
              Define a reusable workflow pattern. Use JSON to define steps and dependencies.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label>Name</Label>
              <Input
                placeholder="e.g. My Virtual Company"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label>Description</Label>
              <Input
                placeholder="What does this workflow do?"
                value={newDesc}
                onChange={(e) => setNewDesc(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label>Template JSON</Label>
              <Textarea
                placeholder={`{\n  "name": "my-workflow",\n  "description": "...",\n  "steps": [\n    { "id": "step-1", "title": "Research", "agent_role": "researcher" },\n    { "id": "step-2", "title": "Write", "agent_role": "writer", "depends_on": ["step-1"] }\n  ]\n}`}
                value={newJson}
                onChange={(e) => setNewJson(e.target.value)}
                rows={10}
                className="font-mono text-xs"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>Cancel</Button>
            <Button onClick={handleCreate} disabled={saving || !newName.trim() || !newJson.trim()}>
              {saving ? "Creating..." : "Create Template"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
