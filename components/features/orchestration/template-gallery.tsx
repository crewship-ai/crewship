"use client"

import { useCallback, useEffect, useState } from "react"
import { Badge } from "@/components/ui/badge"
import {
  LayoutTemplate,
  ArrowRight,
  GitBranch,
  Repeat,
  GitMerge,
  Wrench,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { CREW_COLORS, CREW_COLOR_DEFAULT } from "@/lib/colors"
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

const stepColors = {
  default: { fill: CREW_COLORS.blue, border: "#2563eb", text: "#93c5fd" },
  loop: { fill: CREW_COLORS.emerald, border: "#059669", text: "#6ee7b7" },
  lead: { fill: CREW_COLORS.violet, border: "#7c3aed", text: "#c4b5fd" },
}

function TemplateMiniGraph({ steps }: { steps: TemplateDefinition["steps"] }) {
  if (steps.length === 0) return null

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
  const nodeW = 64
  const nodeH = 24
  const colGap = 32
  const rowGap = 8

  for (const [level, groupSteps] of levelGroups) {
    groupSteps.forEach((step, idx) => {
      nodePositions.set(step.id, {
        x: 8 + level * (nodeW + colGap),
        y: 8 + idx * (nodeH + rowGap),
      })
    })
  }

  const maxRows = Math.max(...[...levelGroups.values()].map((g) => g.length))
  const svgW = 16 + (maxLevel + 1) * (nodeW + colGap)
  const svgH = Math.max(40, 16 + maxRows * (nodeH + rowGap))

  return (
    <svg width="100%" height={svgH} viewBox={`0 0 ${svgW} ${svgH}`} className="overflow-visible">
      <defs>
        <marker id="tmpl-arrow" markerWidth="6" markerHeight="4" refX="6" refY="2" orient="auto">
          <path d="M0,0 L6,2 L0,4" fill={CREW_COLORS.blue} opacity="0.6" />
        </marker>
      </defs>

      {/* Edges */}
      {steps.flatMap((step) =>
        (step.depends_on || []).map((depId) => {
          const from = nodePositions.get(depId)
          const to = nodePositions.get(step.id)
          if (!from || !to) return null
          const x1 = from.x + nodeW
          const y1 = from.y + nodeH / 2
          const x2 = to.x
          const y2 = to.y + nodeH / 2
          const mx = (x1 + x2) / 2
          return (
            <path
              key={`e-${depId}-${step.id}`}
              d={`M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`}
              fill="none"
              stroke={CREW_COLORS.blue}
              strokeWidth={1.5}
              strokeDasharray="4 3"
              strokeOpacity={0.4}
              markerEnd="url(#tmpl-arrow)"
            />
          )
        })
      )}

      {/* Nodes */}
      {steps.map((step) => {
        const pos = nodePositions.get(step.id)
        if (!pos) return null
        const hasLoop = step.max_iterations && step.max_iterations > 1
        const isLead = step.agent_role?.toLowerCase().includes("lead")
        const colors = hasLoop ? stepColors.loop : isLead ? stepColors.lead : stepColors.default
        const label = step.agent_role || step.title || step.id
        return (
          <g key={step.id}>
            <rect
              x={pos.x} y={pos.y}
              width={nodeW} height={nodeH}
              rx={4}
              fill={colors.fill}
              fillOpacity={0.15}
              stroke={colors.border}
              strokeWidth={1}
              strokeOpacity={0.5}
            />
            <text
              x={pos.x + nodeW / 2}
              y={pos.y + nodeH / 2 + 3.5}
              textAnchor="middle"
              fill={colors.text}
              fontSize={9}
              fontFamily="system-ui"
              fontWeight={500}
            >
              {label.length > 9 ? label.slice(0, 8) + "…" : label}
            </text>
            {hasLoop && (
              <text
                x={pos.x + nodeW - 2} y={pos.y + 8}
                textAnchor="end"
                fill={colors.text}
                fontSize={7}
                fontFamily="system-ui"
              >
                ↻
              </text>
            )}
          </g>
        )
      })}
    </svg>
  )
}

export function TemplateGallery({ workspaceId }: TemplateGalleryProps) {
  const [templates, setTemplates] = useState<WorkflowTemplate[]>([])
  const [loading, setLoading] = useState(true)

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

  if (loading) {
    return (
      <div className="space-y-4">
        <div className="h-12 rounded-lg bg-muted/30 animate-pulse" />
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="h-48 rounded-xl bg-card border border-border animate-pulse" />
          ))}
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-5">
      {/* Phase 2 banner */}
      <div className="flex items-center gap-3 rounded-lg border border-amber-500/20 bg-amber-500/5 px-4 py-3">
        <Wrench className="h-4 w-4 shrink-0 text-amber-500" />
        <p className="text-sm text-amber-200/80">
          Workflow templates are <span className="font-medium text-amber-200">read-only previews</span>. Custom template editor coming in Phase 2.
        </p>
      </div>

      {/* Header */}
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          {templates.length} template{templates.length !== 1 ? "s" : ""} available
        </p>
        <Badge variant="outline" className="text-xs text-muted-foreground">
          Coming soon
        </Badge>
      </div>

      {/* Grid */}
      {templates.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-xl border border-dashed border-border py-16 text-center">
          <LayoutTemplate className="h-10 w-10 text-muted-foreground/40 mb-3" />
          <p className="text-sm font-medium text-foreground">No templates yet</p>
          <p className="text-xs text-muted-foreground mt-1">
            Built-in workflow templates will appear here
          </p>
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {templates.map((tmpl) => {
            const Icon = iconMap[tmpl.icon || ""] || LayoutTemplate
            const steps = tmpl.template_json?.steps || []
            const hasLoop = steps.some((s) => (s.max_iterations || 0) > 1)
            const hasParallel =
              steps.filter((s) => (s.depends_on?.length || 0) === 0).length > 1

            return (
              <div
                key={tmpl.id}
                className={cn(
                  "group relative rounded-xl border border-border bg-card p-4",
                  "transition-all duration-200",
                  "hover:border-foreground/15 hover:shadow-[0_0_24px_-6px_hsl(var(--foreground)/0.06)]",
                  "hover:scale-[1.01]"
                )}
              >
                {/* Header */}
                <div className="flex items-center gap-2.5 mb-3">
                  <div
                    className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg"
                    style={{ backgroundColor: (tmpl.color || CREW_COLOR_DEFAULT) + "18" }}
                  >
                    <Icon className="h-4 w-4" style={{ color: tmpl.color || CREW_COLOR_DEFAULT }} />
                  </div>
                  <div className="min-w-0">
                    <p className="text-sm font-medium text-foreground truncate">
                      {tmpl.name}
                    </p>
                    {tmpl.description && (
                      <p className="text-[11px] text-muted-foreground truncate mt-0.5">
                        {tmpl.description}
                      </p>
                    )}
                  </div>
                </div>

                {/* Mini graph */}
                <div className="rounded-lg border border-border/50 bg-muted/20 p-2 min-h-[60px] flex items-center justify-center mb-3">
                  <TemplateMiniGraph steps={steps} />
                </div>

                {/* Footer */}
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-[11px] text-muted-foreground">
                    {steps.length} step{steps.length !== 1 ? "s" : ""}
                  </span>
                  {tmpl.is_builtin && (
                    <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                      Builtin
                    </Badge>
                  )}
                  {hasLoop && (
                    <Badge variant="secondary" className="text-[10px] px-1.5 py-0">
                      <Repeat className="h-3 w-3 mr-0.5" />
                      Loop
                    </Badge>
                  )}
                  {hasParallel && (
                    <Badge variant="secondary" className="text-[10px] px-1.5 py-0">
                      Parallel
                    </Badge>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
