"use client"

import { Bot, ChevronRight } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Badge } from "@/components/ui/badge"
import { CrewIcon } from "@/components/ui/crew-icon"

interface TemplateAgent {
  name: string
  slug: string
  role_title: string
  agent_role: string
  system_prompt: string
}

interface CrewTemplate {
  id: string
  name: string
  slug: string
  description: string | null
  icon: string | null
  color: string | null
  category: string
  agents: TemplateAgent[]
  is_builtin: boolean
}

// Crew templates stored in the DB can carry a palette ID ("blue") or a
// legacy hex string ("#3b82f6") in their `color` field. CrewIcon expects a
// palette ID — anything else silently falls back to the first palette entry,
// which looks like a bug. Normalize up-front so we pass a clean palette ID
// (or null to let CrewIcon pick the default) into the component.
const CREW_PALETTE_IDS = new Set([
  "blue", "emerald", "violet", "amber", "rose", "cyan", "lime", "fuchsia",
])
function normalizeTemplateColor(color: string | null | undefined): string | null {
  if (!color) return null
  return CREW_PALETTE_IDS.has(color) ? color : null
}

// CrewIcon expects a lucide icon name (`clipboard`, `rocket`, `code`, …).
// Legacy template rows in the DB may carry emoji or other free-form strings;
// enforce the lucide naming contract (kebab-case ASCII) and fall back to the
// canonical default so the icon never breaks.
const LUCIDE_ICON_NAME_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/i
function normalizeTemplateIcon(icon: string | null | undefined): string {
  if (!icon) return "clipboard"
  return LUCIDE_ICON_NAME_RE.test(icon) ? icon : "clipboard"
}

// ── Quick-start template grid (shown on the mode chooser screen) ──────────

interface QuickStartTemplateGridProps {
  templates: CrewTemplate[]
  loading: boolean
  onSelect: (template: CrewTemplate) => void
}

export function QuickStartTemplateGrid({ templates, loading, onSelect }: QuickStartTemplateGridProps) {
  if (loading) {
    return (
      <div
        role="status"
        aria-live="polite"
        className="flex items-center gap-2 text-sm text-muted-foreground"
      >
        <Spinner className="h-4 w-4" /> Loading templates...
      </div>
    )
  }

  if (templates.length === 0) return null

  return (
    <div>
      <h3 className="text-sm font-medium text-muted-foreground mb-3">Quick Start Templates</h3>
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {templates.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => onSelect(t)}
            className="flex items-start gap-3 rounded-lg border border-border p-3 text-left transition-all hover:bg-accent hover:border-primary/50 group"
          >
            <CrewIcon icon={normalizeTemplateIcon(t.icon)} color={normalizeTemplateColor(t.color)} size="sm" />
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-1">
                <span className="font-medium text-sm truncate">{t.name}</span>
                <ChevronRight className="h-3 w-3 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
              </div>
              <p className="text-xs text-muted-foreground line-clamp-2 mt-0.5">{t.description}</p>
              <div className="flex items-center gap-2 mt-1.5">
                <div className="flex items-center gap-1">
                  <Bot className="h-3 w-3 text-muted-foreground" />
                  <span className="text-xs text-muted-foreground">{t.agents.length} agents</span>
                </div>
                <Badge variant="outline" className="text-xs py-0">{t.category}</Badge>
              </div>
            </div>
          </button>
        ))}
      </div>
    </div>
  )
}

// ── Full template gallery (the "Choose a Template" screen) ────────────────

interface TemplateGalleryProps {
  templates: CrewTemplate[]
  loading: boolean
  onSelect: (template: CrewTemplate) => void
}

export function TemplateGallery({ templates, loading, onSelect }: TemplateGalleryProps) {
  if (loading) {
    return (
      <div
        role="status"
        aria-live="polite"
        className="flex items-center gap-2 text-sm text-muted-foreground py-8"
      >
        <Spinner className="h-4 w-4" /> Loading templates...
      </div>
    )
  }

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
      {templates.map((t) => (
        <button
          key={t.id}
          type="button"
          onClick={() => onSelect(t)}
          className="flex flex-col items-start gap-2 rounded-lg border border-border p-4 text-left transition-all hover:bg-accent hover:border-primary/50"
        >
          <div className="flex items-center gap-2">
            <CrewIcon icon={normalizeTemplateIcon(t.icon)} color={normalizeTemplateColor(t.color)} size="sm" />
            <span className="font-semibold">{t.name}</span>
          </div>
          <p className="text-sm text-muted-foreground">{t.description}</p>
          <div className="flex items-center gap-2 mt-1">
            <Bot className="h-3.5 w-3.5 text-muted-foreground" />
            <span className="text-xs text-muted-foreground">{t.agents.length} agents</span>
            <Badge variant="outline" className="text-xs">{t.category}</Badge>
          </div>
          <div className="flex flex-wrap gap-1 mt-1">
            {t.agents.map((a) => (
              <Badge key={a.slug} variant={a.agent_role === "LEAD" ? "default" : "secondary"} className="text-xs">
                {a.name}
              </Badge>
            ))}
          </div>
        </button>
      ))}
    </div>
  )
}
