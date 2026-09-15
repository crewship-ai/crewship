"use client"

import * as React from "react"
import { useRouter } from "next/navigation"
import { Check, Clock, Copy, GitFork, Hand, Sparkles, Tag, Terminal, Users, ListChecks } from "lucide-react"
import { toast } from "sonner"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceDescriptionInput,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfacePill,
  CreateSurfacePills,
  CreateSurfaceRefusal,
  CreateSurfaceTile,
  CreateSurfaceTitleInput,
} from "@/components/layout/create-surface"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { CrewIcon } from "@/components/ui/crew-icon"
import { apiFetch } from "@/lib/api-fetch"
import { extractProblemDetail } from "@/lib/problem-details"
import { duplicatePayload } from "@/lib/routine-save-payload"
import { listRoutineDrafts, type RoutineDraftListEntry } from "@/lib/routine-drafts"
import { resolveRoutineColor, resolveRoutineIcon } from "@/lib/routine-identity"
import type { Pipeline } from "@/hooks/use-pipelines"

// routine-new-dialog — three honest ways in, every one ending as a draft
// you read on the routine page and publish when it looks right.
//
//   Describe it   the crew lead drafts it with you in chat and saves a draft
//                 (get_routine_draft / save_routine_draft); the chat's link
//                 comes back here as ?draft=<slug>, which opens the routine
//                 page with the draft row on top;
//   Copy          an existing routine under a new name, through the same
//                 save path the kebab's Copy uses;
//   CLI           scripts, agent behaviour and checks are files; the commands
//                 are the real ones from docs/cli/routine.mdx.
//
// There is no "write it yourself" editor: the web cannot carry the scripts,
// tools and agent behaviour a routine needs, and a form that pretends it can
// is a lie the first real routine exposes.

interface Crew {
  id: string
  name: string
  icon?: string | null
  color?: string | null
}
interface AgentRec {
  id: string
  name: string
  slug: string
  agent_role: string
  crew_id: string | null
}

type Mode = "pick" | "describe" | "copy" | "cli"

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  routines: Pipeline[]
  /** Open a routine page (a draft to continue, or the fresh copy). */
  onOpenRoutine: (slug: string) => void
  /** Something was created on the server (a copy) — refresh the list. */
  onCreated?: () => void
}

/** The lead of a crew: its LEAD agent, else any agent in it. */
export function leadOf(agents: AgentRec[], crewId: string | null): AgentRec | null {
  if (!crewId) return null
  const inCrew = agents.filter((a) => a.crew_id === crewId)
  return inCrew.find((a) => a.agent_role === "LEAD") ?? inCrew[0] ?? null
}

/** The prompt the lead receives; it must save a draft, never publish. */
export function describePrompt(name: string, goal: string): string {
  return `Author a routine draft for me using get_routine_draft and save_routine_draft. Return the editor_url so I can review and publish it; do not publish with save_routine.${name.trim() ? ` Call it "${name.trim()}".` : ""} Goal: ${goal.trim()}
Use scripts for deterministic work and agents where judgment is needed. Show a readable recipe and expected outputs before saving. Distinguish static validation from a real trial. Use outcomes.required for checks that must block unverified results.`
}

export const CLI_COMMANDS = (slug: string, crew: string) => [
  {
    step: "1 · Get the current version (or a starter) as a file:",
    command: `crewship routine get ${slug} -f yaml > ${slug}.yaml\n# new routine: crewship routine init -o routine.json`,
  },
  {
    step: "1b · Put the scripts a step runs on the crew share (they show up under “Files this routine runs”):",
    command: `crewship crew files save ${crew} shared/scripts/ledger-post.go --file ./ledger-post.go`,
  },
  {
    step: "2 · Edit steps, checks, scripts; check it without running anything:",
    command: `crewship routine validate ${slug}.yaml\ncrewship routine fixture-test ${slug}.yaml --step <id>   # one step on sample data`,
  },
  {
    step: "3 · Save it as a draft — it appears on the routine page as “Draft r<n>”:",
    command: `crewship routine draft get ${slug} -f json > draft.json\n# put your definition in draft.json.document.definition\ncrewship routine draft save draft.json -f json > saved.json`,
  },
  {
    step: "4 · Publish here in the web (review + confirm), or with the CLI:",
    command: `crewship routine draft publish saved.json`,
  },
]

export function RoutineNewDialog({ open, onOpenChange, workspaceId, routines, onOpenRoutine, onCreated }: Props) {
  const router = useRouter()
  const [mode, setMode] = React.useState<Mode>("pick")
  const [drafts, setDrafts] = React.useState<RoutineDraftListEntry[]>([])
  const [crews, setCrews] = React.useState<Crew[]>([])
  const [agents, setAgents] = React.useState<AgentRec[]>([])
  const [crewId, setCrewId] = React.useState<string | null>(null)
  const [crewOpen, setCrewOpen] = React.useState(false)
  const [name, setName] = React.useState("")
  const [goal, setGoal] = React.useState("")
  const [busy, setBusy] = React.useState<string | null>(null)
  const [refusal, setRefusal] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (!open) return
    setMode("pick")
    setName("")
    setGoal("")
    setRefusal(null)
    setBusy(null)
    const controller = new AbortController()
    const ws = encodeURIComponent(workspaceId)
    listRoutineDrafts(workspaceId, controller.signal).then((rows) => { if (!controller.signal.aborted) setDrafts(rows) }).catch(() => {})
    apiFetch(`/api/v1/crews?workspace_id=${ws}`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : []))
      .then((data: Crew[]) => { if (!controller.signal.aborted && Array.isArray(data)) { setCrews(data); setCrewId((c) => c ?? data[0]?.id ?? null) } })
      .catch(() => {})
    apiFetch(`/api/v1/agents?workspace_id=${ws}`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : []))
      .then((data: AgentRec[]) => { if (!controller.signal.aborted && Array.isArray(data)) setAgents(data) })
      .catch(() => {})
    return () => controller.abort()
  }, [open, workspaceId])

  const crew = crews.find((c) => c.id === crewId) ?? null
  const lead = leadOf(agents, crewId)
  const draftName = (slug: string) => routines.find((r) => r.slug === slug)?.name || slug
  const published = routines.filter((r) => (r.head_version ?? 0) > 0 || r.invocation_count > 0 || !r.draft)

  const describe = () => {
    if (!goal.trim()) {
      setRefusal("Say what should happen — the lead drafts the steps from that.")
      return
    }
    if (!lead) {
      setRefusal(crews.length ? "The chosen crew has no agent to draft with. Pick another crew." : "No crew has an agent yet. Create a crew with a lead first.")
      return
    }
    router.push(`/chat/${encodeURIComponent(lead.slug)}?prompt=${encodeURIComponent(describePrompt(name, goal))}`)
    onOpenChange(false)
  }

  const copy = async (source: Pipeline) => {
    if (busy) return
    setBusy(source.slug)
    setRefusal(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(source.slug)}`)
      if (!res.ok) throw new Error(`Could not read ${source.name || source.slug} (${res.status}).`)
      const detail = (await res.json()) as { slug: string; name: string; description?: string; definition: Record<string, unknown>; author_crew_id?: string }
      const body = duplicatePayload(
        { slug: detail.slug, name: detail.name, description: detail.description, definition: detail.definition, author_crew_id: detail.author_crew_id },
        { name: `${detail.name || detail.slug} (copy)` },
      )
      const save = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/save`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!save.ok) {
        const problem = await save.clone().json().then((b) => extractProblemDetail(b) ?? b?.error).catch(() => null)
        throw new Error(problem || "The copy could not be saved.")
      }
      toast.success("Copied · schedules and history stay with the original")
      onCreated?.()
      onOpenChange(false)
      onOpenRoutine(body.slug)
    } catch (e) {
      setRefusal(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const header = {
    pick: { title: "New routine", description: "Every way in ends as a draft you read here and publish when it looks right." },
    describe: { title: "New routine · Describe it", description: undefined },
    copy: { title: "Copy an existing routine", description: "The copy gets a new name; schedules and history stay with the original." },
    cli: { title: "Build it with the CLI", description: "Scripts, agent behaviour and checks live in files. The web shows the result and publishes it." },
  }[mode]

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="md" dirty={mode === "describe" && (!!name.trim() || !!goal.trim())} discardLabel="this description" onSubmit={() => { if (mode === "describe") describe() }} ariaLabel="New routine">
      <CreateSurfaceHeader
        concept="routines"
        context="Routines"
        title={header.title}
        description={header.description}
        onBack={mode === "pick" ? undefined : () => setMode("pick")}
        onClose={() => onOpenChange(false)}
      />
      {mode === "pick" && (
        <CreateSurfaceBody className="flex flex-col gap-3">
          {drafts.length > 0 && (
            <div data-testid="routine-continue-drafts">
              <p className="mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">Continue a draft</p>
              <div className="flex flex-wrap gap-1.5">
                {drafts.map((d) => (
                  <button key={d.slug} type="button" onClick={() => { onOpenChange(false); onOpenRoutine(d.slug) }} className="rounded-full border border-purple/30 bg-purple/10 px-2.5 py-1 text-xs text-purple hover:bg-purple/20">
                    {draftName(d.slug)} · r{d.revision}
                  </button>
                ))}
              </div>
            </div>
          )}
          <CreateSurfaceTile icon={Sparkles} accent="purple" title="Describe it" meta="recommended" onClick={() => setMode("describe")} description={<>Tell the crew lead what should happen, in your words. It drafts the routine with you in chat and saves a <b className="font-medium text-foreground">draft</b> — nothing runs until you publish.</>} />
          <CreateSurfaceTile icon={GitFork} accent="blue" title="Copy an existing routine" onClick={() => setMode("copy")} description="Start from one of your routines. The copy has a new name; schedules and history stay with the original." />
          <CreateSurfaceTile icon={Terminal} accent="slate" title="Build it with the CLI" meta="for builders" onClick={() => setMode("cli")} description={<>Scripts, agent behaviour and checks are written as files and saved with <code className="font-mono">crewship routine draft</code>. The web shows the draft and publishes it.</>} />
        </CreateSurfaceBody>
      )}
      {mode === "describe" && (
        <>
          <CreateSurfaceBody className="space-y-1">
            <CreateSurfaceTitleInput value={name} onChange={(e) => setName(e.target.value)} placeholder="Routine name" aria-label="Routine name" autoFocus />
            <CreateSurfaceDescriptionInput value={goal} onChange={(e) => setGoal(e.target.value)} aria-label="What should happen" placeholder="What should happen, for whom, and when? What does a good result look like, and which files, folders or rules matter?" rows={4} />
            {!goal && (
              <button type="button" className="px-1 py-2 text-xs text-primary hover:underline" onClick={() => setGoal("## What should happen\n\n\n## For whom, and when\n\n\n## A good result looks like\n\n\n## Files, folders and rules that matter\n\n")}>
                Use a routine brief template
              </button>
            )}
            <p className="px-1 pt-2 text-[11px] text-muted-foreground">The lead agent drafts the steps from this and asks back in chat. Nothing runs and nothing is published until you say so.</p>
          </CreateSurfaceBody>
          <CreateSurfacePills>
            <Popover open={crewOpen} onOpenChange={setCrewOpen} modal>
              <PopoverTrigger asChild>
                <CreateSurfacePill set={!!crew} leading={crew ? <CrewIcon icon={crew.icon || "folder"} color={crew.color} size="sm" className="!h-4 !w-4 !rounded" /> : undefined} icon={crew ? undefined : Users}>
                  Crew · {crew ? crew.name : "choose"}
                </CreateSurfacePill>
              </PopoverTrigger>
              <PopoverContent className="w-[220px] p-1" align="start">
                {crews.length === 0 && <p className="px-2 py-1.5 text-xs text-muted-foreground">No crews yet.</p>}
                {crews.map((c) => (
                  <button key={c.id} type="button" onClick={() => { setCrewId(c.id); setCrewOpen(false) }} className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs hover:bg-accent">
                    <CrewIcon icon={c.icon || "folder"} color={c.color} size="sm" className="!h-5 !w-5 !rounded-md" />
                    <span className="flex-1 truncate">{c.name}</span>
                    {crewId === c.id && <Check className="h-3.5 w-3.5" />}
                  </button>
                ))}
              </PopoverContent>
            </Popover>
            <CreateSurfacePill readOnly icon={Clock}>Schedule · not set</CreateSurfacePill>
            <CreateSurfacePill readOnly icon={ListChecks}>Inputs · let the lead propose</CreateSurfacePill>
            <CreateSurfacePill readOnly icon={Hand}>Needs approval · choose</CreateSurfacePill>
            <CreateSurfacePill readOnly icon={Tag}>Labels</CreateSurfacePill>
          </CreateSurfacePills>
        </>
      )}
      {mode === "copy" && (
        <CreateSurfaceBody className="flex flex-col gap-2">
          {published.length === 0 && <p className="text-xs text-muted-foreground">Nothing to copy yet.</p>}
          {published.map((r) => (
            <CreateSurfaceTile
              key={r.slug}
              leading={<CrewIcon icon={resolveRoutineIcon(r)} color={resolveRoutineColor(r)} size="md" />}
              title={r.name || r.slug}
              meta={r.head_version ? `v${r.head_version}` : undefined}
              description={r.description || r.slug}
              disabled={!!busy}
              onClick={() => void copy(r)}
              data-testid={`routine-copy-${r.slug}`}
            />
          ))}
        </CreateSurfaceBody>
      )}
      {mode === "cli" && (
        <CreateSurfaceBody className="flex flex-col gap-3 text-xs">
          {CLI_COMMANDS("my-routine", crew?.name ? crew.name.toLowerCase().replace(/[^a-z0-9]+/g, "-") : "<crew>").map((c) => (
            <div key={c.step}>
              <p className="mb-1">{c.step}</p>
              <pre className="overflow-x-auto rounded-md border border-hairline bg-background px-3 py-2 font-mono text-[11px] whitespace-pre-wrap">{c.command}</pre>
            </div>
          ))}
          <p className="text-muted-foreground">The exact flags are in the CLI reference (<code className="font-mono">crewship routine --help</code>); step 3 uses the drafts API, step 4 the same publish gate as the web.</p>
        </CreateSurfaceBody>
      )}
      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />
      {mode === "describe" ? (
        <CreateSurfaceFooter onCancel={() => onOpenChange(false)} primaryLabel="Create draft with the lead" primaryIcon={Sparkles} onPrimary={describe} primaryDisabled={!goal.trim()} />
      ) : (
        <CreateSurfaceFooter hint="Esc closes" onCancel={() => onOpenChange(false)} cancelLabel={mode === "pick" ? "Cancel" : "Close"} guardCancel={false} secondary={mode === "copy" ? <span className="text-[11px] text-muted-foreground"><Copy className="mr-1 inline h-3 w-3" />Pick a routine to copy</span> : undefined} />
      )}
    </CreateSurface>
  )
}
