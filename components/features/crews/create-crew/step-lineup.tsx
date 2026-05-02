"use client"

import { useEffect, useMemo, useState } from "react"
import { Search, Sparkles, BookOpen, FileX2, RefreshCw, Lock } from "lucide-react"
import { toast } from "sonner"
import { CrewIcon } from "@/components/ui/crew-icon"
import { cn } from "@/lib/utils"
import type { CrewTemplate } from "./api"
import type { WizardState } from "./types"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

export function StepLineup(props: Props) {
  const { state } = props
  return (
    <div className="space-y-3">
      <ModeTabs state={state} setMode={(m) => props.setState({ mode: m })} />
      {state.mode === "browse" && <BrowseTemplates {...props} />}
      {state.mode === "ai" && <AISuggest {...props} />}
      {state.mode === "empty" && <EmptyMode />}
    </div>
  )
}

function ModeTabs({ state, setMode }: { state: WizardState; setMode: (m: WizardState["mode"]) => void }) {
  const tabs: { id: WizardState["mode"]; label: string; Icon: React.ElementType; pill?: string }[] = [
    { id: "browse", label: "Browse templates", Icon: BookOpen },
    { id: "ai", label: "AI Suggest", Icon: Sparkles, pill: "beta" },
    { id: "empty", label: "Empty crew", Icon: FileX2 },
  ]
  return (
    <div className="flex border-b border-white/10 -mx-5 px-5">
      {tabs.map(({ id, label, Icon, pill }) => (
        <button
          key={id}
          type="button"
          onClick={() => setMode(id)}
          className={cn(
            "px-3.5 py-2.5 text-xs flex items-center gap-2 border-b-2 -mb-px transition-colors",
            state.mode === id
              ? "border-blue-400 text-foreground"
              : "border-transparent text-muted-foreground hover:text-foreground/80",
          )}
        >
          <Icon className="h-3.5 w-3.5" />
          {label}
          {pill && (
            <span className="text-[9px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded bg-violet-500/20 text-violet-300">
              {pill}
            </span>
          )}
        </button>
      ))}
    </div>
  )
}

// =============================================================================
// Browse templates
// =============================================================================

type SourceFilter = "builtin" | "mine" | "workspace" | "marketplace"

function BrowseTemplates({ state, setState }: Props) {
  const [templates, setTemplates] = useState<CrewTemplate[] | null>(null)
  const [query, setQuery] = useState("")
  const [source, setSource] = useState<SourceFilter>("builtin")
  const [category, setCategory] = useState<string | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    fetch("/api/v1/crew-templates")
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data: CrewTemplate[]) => {
        if (!cancelled) setTemplates(Array.isArray(data) ? data : [])
      })
      .catch((e) => {
        if (!cancelled) {
          setTemplates([])
          setLoadError(e instanceof Error ? e.message : String(e))
        }
      })
    return () => { cancelled = true }
  }, [])

  // Client-side filtering. Server-side q/source/category support is a follow-up.
  const filtered = useMemo(() => {
    if (!templates) return []
    let xs = templates
    if (source === "builtin") xs = xs.filter((t) => t.is_builtin)
    if (source === "mine" || source === "workspace") xs = xs.filter((t) => !t.is_builtin)
    if (source === "marketplace") xs = []
    if (category) xs = xs.filter((t) => t.category.toLowerCase() === category.toLowerCase())
    if (query.trim()) {
      const q = query.toLowerCase()
      xs = xs.filter((t) =>
        t.name.toLowerCase().includes(q) ||
        (t.description ?? "").toLowerCase().includes(q) ||
        t.category.toLowerCase().includes(q),
      )
    }
    return xs
  }, [templates, query, source, category])

  const facets = useMemo(() => {
    const all = templates ?? []
    const counts: Record<string, number> = {}
    let builtin = 0, custom = 0
    for (const t of all) {
      if (t.is_builtin) builtin++
      else custom++
      const cat = t.category || "OTHER"
      counts[cat] = (counts[cat] ?? 0) + 1
    }
    return { builtin, custom, byCategory: counts }
  }, [templates])

  const picked = filtered.find((t) => t.slug === state.pickedTemplateSlug) ?? filtered[0] ?? null

  // Auto-pick first visible template when nothing selected, so the preview
  // pane never shows "select a template" empty state on first render.
  useEffect(() => {
    if (!state.pickedTemplateSlug && picked) {
      setState({
        pickedTemplateSlug: picked.slug,
        pickedTemplateMeta: {
          name: picked.name,
          agentCount: picked.agents.length,
          agents: picked.agents.map((a) => ({ name: a.name, agent_role: a.agent_role })),
        },
      })
    }
  }, [picked, state.pickedTemplateSlug, setState])

  // Populate identity name/slug from template the first time the user picks one.
  // Don't overwrite if user has already typed something.
  useEffect(() => {
    if (!picked) return
    if (state.name.trim() === "" && state.slug.trim() === "") {
      setState({
        name: picked.name,
        slug: picked.slug,
        description: state.description || picked.description || "",
        icon: picked.icon || state.icon,
        color: picked.color || state.color,
      })
    }
  }, [picked, state.name, state.slug, state.description, state.icon, state.color, setState])

  return (
    <div className="grid grid-cols-[1fr_320px] gap-0 border border-white/10 rounded-lg overflow-hidden bg-zinc-950/40 min-h-[440px] max-h-[480px]">
      <div className="flex flex-col min-h-0 border-r border-white/10">
        <div className="px-3 py-2 border-b border-white/10 flex items-center gap-2">
          <div className="flex-1 relative">
            <Search className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder='Search templates… (e.g. "saas", "research")'
              className="w-full pl-8 pr-2 py-1.5 text-xs bg-black/30 border border-white/15 rounded outline-none focus:border-blue-400"
            />
          </div>
        </div>

        <div className="flex border-b border-white/10 px-3">
          <SourceTab active={source === "builtin"} onClick={() => setSource("builtin")} label="Built-in" badge={facets.builtin} />
          <SourceTab active={source === "mine"} onClick={() => setSource("mine")} label="Mine" badge={facets.custom} />
          <SourceTab active={source === "workspace"} onClick={() => setSource("workspace")} label="Workspace" badge={facets.custom} />
          <SourceTab active={false} disabled label="Marketplace" pill="soon" />
        </div>

        {Object.keys(facets.byCategory).length > 0 && (
          <div className="flex gap-1.5 px-3 py-2 border-b border-white/10 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
            <CategoryChip label="All" active={category === null} count={Object.values(facets.byCategory).reduce((a, b) => a + b, 0)} onClick={() => setCategory(null)} />
            {Object.entries(facets.byCategory).map(([cat, count]) => (
              <CategoryChip key={cat} label={cat.toLowerCase()} active={category === cat} count={count} onClick={() => setCategory(cat === category ? null : cat)} />
            ))}
          </div>
        )}

        <div className="flex-1 overflow-y-auto p-1.5 grid grid-cols-2 gap-1.5 content-start">
          {templates === null && (
            <div className="col-span-2 text-center text-xs text-muted-foreground py-8">Loading templates…</div>
          )}
          {templates !== null && filtered.length === 0 && (
            <div className="col-span-2 text-center text-xs text-muted-foreground py-8">
              {loadError ? `Failed to load: ${loadError}` : "No templates match your filters."}
            </div>
          )}
          {filtered.map((t) => (
            <button
              key={t.slug}
              type="button"
              onClick={() => setState({
                pickedTemplateSlug: t.slug,
                pickedTemplateMeta: {
                  name: t.name,
                  agentCount: t.agents.length,
                  agents: t.agents.map((a) => ({ name: a.name, agent_role: a.agent_role })),
                },
              })}
              className={cn(
                "p-2.5 rounded-md border bg-card text-left flex gap-2.5 items-start transition-colors",
                state.pickedTemplateSlug === t.slug
                  ? "border-blue-400 bg-blue-500/10"
                  : "border-white/10 hover:border-white/20",
              )}
            >
              <CrewIcon icon={t.icon || "users"} color={t.color || "blue"} size="sm" />
              <div className="flex-1 min-w-0">
                <div className="text-xs font-semibold flex items-center gap-1.5">
                  <span className="truncate">{t.name}</span>
                  {t.is_builtin && (
                    <span className="shrink-0 inline-flex items-center justify-center h-3 w-3 rounded-full bg-blue-500 text-[8px] text-blue-950 font-bold">
                      ✓
                    </span>
                  )}
                </div>
                <div className="text-[10px] text-muted-foreground capitalize">
                  {t.category.toLowerCase()} · {t.agents.length} agents
                </div>
                {t.description && (
                  <p className="text-[11px] text-foreground/60 mt-1 line-clamp-2">{t.description}</p>
                )}
                <div className="flex gap-1 mt-1.5 flex-wrap">
                  <Pill>{t.agents.length} agents</Pill>
                  <SourceMark builtin={t.is_builtin} />
                </div>
              </div>
            </button>
          ))}
        </div>
      </div>

      {picked ? (
        <PreviewPane template={picked} />
      ) : (
        <div className="flex items-center justify-center text-xs text-muted-foreground p-6 text-center">
          Pick a template to preview the lineup.
        </div>
      )}
    </div>
  )
}

function SourceTab({ active, onClick, label, badge, pill, disabled }: { active: boolean; onClick?: () => void; label: string; badge?: number; pill?: string; disabled?: boolean }) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "px-3 py-2 text-xs flex items-center gap-1.5 border-b-2 -mb-px",
        active ? "border-blue-400 text-foreground" : "border-transparent text-muted-foreground",
        !disabled && "hover:text-foreground/80",
        disabled && "opacity-40 cursor-not-allowed",
      )}
      title={disabled ? "Coming soon" : undefined}
    >
      {disabled && <Lock className="h-2.5 w-2.5" />}
      {label}
      {typeof badge === "number" && (
        <span className={cn("text-[9px] px-1.5 rounded-full font-semibold", active ? "bg-blue-500/20 text-blue-300" : "bg-white/5 text-muted-foreground")}>
          {badge}
        </span>
      )}
      {pill && (
        <span className="text-[9px] px-1.5 py-0.5 rounded bg-white/5 text-muted-foreground">{pill}</span>
      )}
    </button>
  )
}

function CategoryChip({ label, active, count, onClick }: { label: string; active: boolean; count: number; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "shrink-0 px-2.5 py-0.5 rounded-full text-[11px] border whitespace-nowrap transition-colors capitalize",
        active
          ? "bg-blue-500/20 border-blue-400 text-blue-300"
          : "bg-card border-white/10 text-foreground/70 hover:border-white/20",
      )}
    >
      {label} <span className="opacity-60 ml-1">{count}</span>
    </button>
  )
}

function Pill({ children }: { children: React.ReactNode }) {
  return <span className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-white/5 text-muted-foreground">{children}</span>
}

function SourceMark({ builtin }: { builtin: boolean }) {
  return (
    <span className={cn(
      "text-[9px] px-1.5 py-0.5 rounded",
      builtin ? "bg-white/5 text-muted-foreground" : "bg-emerald-500/15 text-emerald-300",
    )}>
      {builtin ? "built-in" : "workspace"}
    </span>
  )
}

function PreviewPane({ template }: { template: CrewTemplate }) {
  const lead = template.agents.find((a) => a.agent_role === "LEAD")
  return (
    <div className="flex flex-col min-h-0 bg-card">
      <div className="px-3.5 py-3 border-b border-white/10 flex items-start gap-2.5">
        <CrewIcon icon={template.icon || "users"} color={template.color || "blue"} size="md" />
        <div className="flex-1 min-w-0">
          <div className="text-sm font-semibold">{template.name}</div>
          <div className="text-[11px] text-muted-foreground capitalize">
            {template.category.toLowerCase()} · {template.agents.length} agents
          </div>
        </div>
      </div>
      {template.description && (
        <div className="px-3.5 py-2.5 text-[12px] text-foreground/70 border-b border-white/10">
          {template.description}
        </div>
      )}
      <div className="flex-1 overflow-y-auto">
        <div className="px-3.5 pt-2.5 pb-1 text-[10px] uppercase tracking-wider text-muted-foreground">
          Lineup <span className="opacity-60">— seeded on create</span>
        </div>
        <div className="px-2 pb-2">
          {[...(lead ? [lead] : []), ...template.agents.filter((a) => a.agent_role !== "LEAD")].map((agent, i) => (
            <div
              key={agent.slug}
              className={cn(
                "px-2.5 py-1.5 flex items-center gap-2.5 rounded",
                i > 0 && "border-t border-dashed border-white/5",
              )}
            >
              <div className={cn(
                "h-6 w-6 rounded text-[11px] font-semibold flex items-center justify-center shrink-0",
                agent.agent_role === "LEAD" ? "bg-amber-500/15 text-amber-300" : "bg-violet-500/15 text-violet-300",
              )}>
                {agent.name.slice(0, 1).toUpperCase()}
              </div>
              <div className="flex-1 min-w-0">
                <div className="text-[12px] font-medium leading-tight">{agent.name}</div>
                <div className="text-[10px] text-muted-foreground leading-tight truncate">{agent.role_title}</div>
              </div>
              <span className={cn(
                "text-[9px] font-mono px-1.5 py-0.5 rounded",
                agent.agent_role === "LEAD"
                  ? "bg-amber-500/15 text-amber-300"
                  : "bg-white/5 text-muted-foreground",
              )}>
                {agent.agent_role}
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

// =============================================================================
// AI Suggest mode
// =============================================================================

const AI_EXAMPLES = [
  "Build and maintain a small SaaS: Go API, Next.js frontend, PostgreSQL DB. Need feature dev plus bug triage.",
  "Run a content blog — research topics, write SEO-optimized posts, edit, publish weekly.",
  "Audit JavaScript dependencies for CVEs and outdated packages, propose patch PRs.",
  "Triage incoming customer support tickets: categorize, escalate, draft responses.",
  "Quarterly financial close: collect statements, reconcile, prepare report.",
]

function AISuggest({ state, setState }: Props) {
  const [generating, setGenerating] = useState(false)

  const generate = async () => {
    if (state.aiPrompt.trim().length < 10) {
      toast.error("Describe the goal in at least 10 characters.")
      return
    }
    setGenerating(true)
    try {
      const res = await fetch("/api/v1/crew-ai-suggest", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ description: state.aiPrompt.trim() }),
      })
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }
      const data = await res.json()
      // Map AI agents to AgentDraft. Backend's response is loose — fill defaults.
      const agents = (data.agents ?? []).map((a: { name?: string; slug?: string; role_title?: string; agent_role?: string; system_prompt?: string }) => ({
        name: a.name || "Unnamed",
        slug: a.slug || (a.name || "agent").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""),
        role_title: a.role_title || "",
        agent_role: ((a.agent_role || "AGENT").toUpperCase() === "LEAD" ? "LEAD" : "AGENT") as "AGENT" | "LEAD",
        system_prompt: a.system_prompt,
        cli_adapter: "CLAUDE_CODE",
        tool_profile: "general",
      }))
      setState({
        aiResult: {
          crew_name: data.crew_name || state.name,
          crew_slug: data.crew_slug || state.slug,
          description: data.description || "",
          agents,
        },
        // Also pre-fill Step 1 if user came in blank.
        ...(state.name.trim() === "" ? { name: data.crew_name } : {}),
        ...(state.slug.trim() === "" ? { slug: data.crew_slug } : {}),
        ...(state.description.trim() === "" ? { description: data.description || "" } : {}),
      })
      toast.success(`Generated ${agents.length} agents`)
    } catch (e) {
      toast.error(`AI suggest failed: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setGenerating(false)
    }
  }

  const removeAgent = (slug: string) => {
    if (!state.aiResult) return
    setState({
      aiResult: {
        ...state.aiResult,
        agents: state.aiResult.agents.filter((a) => a.slug !== slug),
      },
    })
  }

  return (
    <div className="space-y-3">
      <div className="rounded border border-violet-500/30 bg-violet-500/[0.06] px-3 py-2 text-xs text-foreground/80 flex gap-2 items-start">
        <span className="shrink-0 text-[9px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded bg-violet-500 text-violet-950">
          AI
        </span>
        <span>
          Claude generates a 3-5 agent lineup tailored to your goal — names, role titles, system prompts, recommended LLM/tools per role.
          You review, edit, then commit. <strong>Requires an Anthropic API key</strong> in workspace credentials.
        </span>
      </div>

      <div className="rounded-lg border border-white/10 bg-gradient-to-b from-violet-500/[0.03] to-transparent p-3.5">
        <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-2">
          What should this crew accomplish?
        </label>
        <textarea
          value={state.aiPrompt}
          onChange={(e) => setState({ aiPrompt: e.target.value })}
          placeholder="Describe the goal in 1-3 sentences…"
          rows={3}
          className="w-full bg-zinc-950 border border-white/15 rounded p-2.5 text-sm outline-none focus:border-violet-400 focus:ring-2 focus:ring-violet-400/20 resize-none"
        />
        <div className="flex flex-wrap gap-1.5 mt-2.5">
          {AI_EXAMPLES.map((ex) => (
            <button
              key={ex}
              type="button"
              onClick={() => setState({ aiPrompt: ex })}
              className="px-2.5 py-1 rounded-full text-[11px] bg-card border border-white/10 text-foreground/70 hover:border-violet-400 hover:text-violet-300 transition-colors text-left max-w-[260px] truncate"
              title={ex}
            >
              {ex}
            </button>
          ))}
        </div>
        <div className="flex items-center gap-2 mt-3">
          <button
            type="button"
            onClick={generate}
            disabled={generating || state.aiPrompt.trim().length < 10}
            className="px-3.5 py-1.5 rounded text-sm font-medium bg-violet-500 hover:bg-violet-400 text-violet-950 disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-1.5"
          >
            {generating ? <RefreshCw className="h-3.5 w-3.5 animate-spin" /> : <Sparkles className="h-3.5 w-3.5" />}
            {generating ? "Generating…" : (state.aiResult ? "Regenerate" : "Generate lineup")}
          </button>
          <span className="text-[11px] text-muted-foreground">~5 sec · uses 1 LLM call</span>
        </div>
      </div>

      {state.aiResult && (
        <div className="rounded-lg border border-violet-500/30 bg-violet-500/[0.04] p-3.5">
          <div className="inline-flex items-center gap-1 text-[10px] px-2 py-0.5 rounded bg-violet-500/15 text-violet-300 mb-2">
            <Sparkles className="h-2.5 w-2.5" /> AI generated · claude-sonnet
          </div>
          <h4 className="text-sm font-semibold">{state.aiResult.crew_name}</h4>
          <p className="text-xs text-muted-foreground mt-1 mb-3">{state.aiResult.description}</p>

          <div className="rounded-lg border border-white/10 bg-card overflow-hidden">
            {state.aiResult.agents.map((a, i) => (
              <div
                key={a.slug || i}
                className={cn(
                  "px-3 py-2 flex items-center gap-3",
                  i > 0 && "border-t border-dashed border-white/5",
                )}
              >
                <div className={cn(
                  "h-6 w-6 rounded text-[11px] font-semibold flex items-center justify-center shrink-0",
                  a.agent_role === "LEAD" ? "bg-amber-500/15 text-amber-300" : "bg-violet-500/15 text-violet-300",
                )}>
                  {a.name.slice(0, 1).toUpperCase()}
                </div>
                <div className="flex-1 min-w-0">
                  <div className="text-[12px] font-medium leading-tight">{a.name}</div>
                  <div className="text-[10px] text-muted-foreground leading-tight truncate">{a.role_title}</div>
                </div>
                <span className={cn(
                  "text-[9px] font-mono px-1.5 py-0.5 rounded shrink-0",
                  a.agent_role === "LEAD"
                    ? "bg-amber-500/15 text-amber-300"
                    : "bg-white/5 text-muted-foreground",
                )}>
                  {a.agent_role}
                </span>
                <button
                  type="button"
                  onClick={() => removeAgent(a.slug)}
                  className="text-[11px] text-muted-foreground hover:text-red-400 px-1"
                  title="Remove from lineup"
                >
                  ×
                </button>
              </div>
            ))}
          </div>

          <div className="text-[11px] text-muted-foreground mt-2 text-right">
            {state.aiResult.agents.length} agents will be created
          </div>
        </div>
      )}
    </div>
  )
}

// =============================================================================
// Empty mode
// =============================================================================

function EmptyMode() {
  return (
    <div className="rounded-lg border border-dashed border-white/15 px-6 py-10 text-center bg-zinc-950/30">
      <FileX2 className="h-8 w-8 text-muted-foreground mx-auto mb-3" />
      <h4 className="text-sm font-semibold">Empty crew</h4>
      <p className="text-xs text-muted-foreground mt-1.5 max-w-sm mx-auto">
        Crew will be created with no agents. Add them later via the
        {" "}<code className="text-[11px] font-mono bg-black/40 px-1 py-0.5 rounded">Create agent</code> button or
        {" "}<code className="text-[11px] font-mono bg-black/40 px-1 py-0.5 rounded">crewship agent create --crew &lt;slug&gt;</code>.
      </p>
    </div>
  )
}
