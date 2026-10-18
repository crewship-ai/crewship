"use client"

import { useMemo, useState } from "react"
import { ClipboardPaste, FolderGit2, PencilLine, Plus, Sparkles, Terminal } from "lucide-react"

import { toast } from "@/lib/toast"
import { Checkbox } from "@/components/ui/checkbox"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceGrid,
  CreateSurfaceHeader,
  CreateSurfaceLoading,
  CreateSurfaceNotice,
  CreateSurfaceRefusal,
  CreateSurfaceSecondaryAction,
  CreateSurfaceSteps,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"
import { useBulkImportSkills, useImportSkill, useSetSkillAssignment, type BulkImportResult } from "@/hooks/use-skills"
import { cn } from "@/lib/utils"
import type { SkillsAgent, SkillsCrew } from "./skills-model"

// Add skill (Harbor 2 #3396): Source → Skills → Who gets it, on the shared
// CreateSurface. A repository goes through the bulk importer twice — a dry run
// lists what the licence gate lets through, then the import takes only the
// picked paths — and a pasted SKILL.md through the single importer. Describe
// it and Write it hand over to the skill editor.

const STEPS = [
  { id: "source", label: "Source" },
  { id: "skills", label: "Skills" },
  { id: "who", label: "Who gets it" },
]

/** `owner/repo` and `github.com/owner/repo` become a clonable https URL. */
export function normalizeRepoInput(raw: string): string {
  const v = raw.trim().replace(/\/+$/, "")
  if (/^[\w.-]+\/[\w.-]+$/.test(v)) return `https://github.com/${v}`
  if (/^(?:www\.)?(?:github|gitlab)\.com\//i.test(v)) return `https://${v.replace(/^www\./i, "")}`
  return v
}

/** The frontmatter `name` of a pasted SKILL.md, if it has one. */
export function pastedSkillName(content: string): string | null {
  const fm = /^---\s*\n([\s\S]*?)\n---/.exec(content.trimStart())
  const m = fm && /^name:\s*["']?([^"'\n]+)["']?\s*$/m.exec(fm[1])
  return m ? m[1].trim() : null
}

export interface FoundSkill {
  path: string
  slug: string
  /** Why it cannot be added; undefined when it can. */
  refused?: string
  licence?: boolean
}

/** A dry run's answer as rows: importable ones carry the reason "dry-run". */
export function foundFromDryRun(r: BulkImportResult): FoundSkill[] {
  return r.skipped.map((s) => {
    const slug = s.slug || s.path.replace(/\/?SKILL\.md$/, "").split("/").pop() || s.path
    if (s.reason === "dry-run") return { path: s.path, slug }
    const licence = /licen[cs]e/i.test(s.reason)
    return { path: s.path, slug, licence, refused: licence ? "License not on the allow list · admins can override" : s.reason }
  })
}

type Pair = { agentId: string; skillId: string; on: true }

export interface AddSkillDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  crews: SkillsCrew[]
  agents: SkillsAgent[]
  /** Slugs already in the library: importing one replaces it. */
  takenSlugs: Set<string>
  /** Repositories the library already draws from, as quick picks. */
  recentSources: { label: string; url: string }[]
  /** Describe it / Write it: the skill editor takes over. */
  onEditor: (method: "describe" | "write") => void
  /** Skills were added; ids of the new or replaced skills. */
  onAdded: (skillIds: string[]) => void
}

export function AddSkillDialog({ open, onOpenChange, workspaceId, crews, agents, takenSlugs, recentSources, onEditor, onAdded }: AddSkillDialogProps) {
  const bulk = useBulkImportSkills(workspaceId)
  const single = useImportSkill(workspaceId)
  const assign = useSetSkillAssignment(workspaceId)

  const [step, setStep] = useState(0)
  const [mode, setMode] = useState<"repo" | "paste">("repo")
  const [from, setFrom] = useState("")
  const [ref, setRef] = useState("")
  const [content, setContent] = useState("")
  const [found, setFound] = useState<FoundSkill[] | null>(null)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [unsafe, setUnsafe] = useState(false)
  const [crewIds, setCrewIds] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [failed, setFailed] = useState<{ pairs: Pair[]; ids: string[] } | null>(null)

  const reset = () => {
    setStep(0)
    setMode("repo")
    setFrom("")
    setRef("")
    setContent("")
    setFound(null)
    setPicked(new Set())
    setUnsafe(false)
    setCrewIds(new Set())
    setBusy(false)
    setError(null)
    setFailed(null)
  }
  const close = () => {
    onOpenChange(false)
    reset()
  }

  const gitUrl = normalizeRepoInput(from)
  const pasteName = pastedSkillName(content)
  const dirty = from.trim() !== "" || content.trim() !== "" || step > 0

  const pickedSkills = useMemo(() => (found ?? []).filter((f) => picked.has(f.path)), [found, picked])
  const count = mode === "paste" ? 1 : pickedSkills.length
  const crewAgents = (id: string) => agents.filter((a) => a.crew_id === id)
  const chosenCrews = crews.filter((c) => crewIds.has(c.id))

  const lookForSkills = async (allowUnsafe = unsafe) => {
    setError(null)
    setBusy(true)
    try {
      const r = await bulk.mutateAsync({ gitUrl, gitRef: ref.trim() || undefined, dryRun: true, allowUnsafeLicense: allowUnsafe })
      const rows = foundFromDryRun(r)
      setFound(rows)
      setPicked(new Set(rows.filter((x) => !x.refused).map((x) => x.path)))
      setStep(1)
      if (r.truncated) setError("The repository has more skills than one import reads; the list stops at the cap.")
    } catch (e) {
      setError(e instanceof Error ? e.message : "The repository could not be read")
    } finally {
      setBusy(false)
    }
  }

  const give = async (pairs: Pair[], ids: string[]) => {
    const results = await Promise.allSettled(pairs.map((p) => assign.mutateAsync(p)))
    const bad = pairs.filter((_, i) => results[i].status === "rejected")
    if (bad.length) {
      const names = [...new Set(bad.map((p) => agents.find((a) => a.id === p.agentId)?.name ?? p.agentId))]
      setFailed({ pairs: bad, ids })
      setError(`The skills were added, but giving them failed for ${names.join(", ")}.`)
      return false
    }
    return true
  }

  const finish = (ids: string[], refused: number) => {
    const n = ids.length
    const to = chosenCrews.length ? ` · ${chosenCrews.map((c) => c.name).join(", ")} ${chosenCrews.length === 1 ? "gets" : "get"} ${n === 1 ? "it" : "them"} on the next run` : ""
    toast.success(`${n} ${n === 1 ? "skill" : "skills"} added${refused ? ` · ${refused} refused` : ""}${to}`)
    onAdded(ids)
    close()
  }

  const add = async () => {
    setError(null)
    setBusy(true)
    try {
      if (failed) {
        if (await give(failed.pairs, failed.ids)) finish(failed.ids, 0)
        return
      }
      let ids: string[] = []
      let refused = 0
      if (mode === "paste") {
        const r = await single.mutateAsync({ content, allowUnsafeLicense: unsafe })
        ids = [r.skill_id]
      } else {
        const r = await bulk.mutateAsync({ gitUrl, gitRef: ref.trim() || undefined, paths: pickedSkills.map((p) => p.path), allowUnsafeLicense: unsafe })
        ids = r.imported.map((x) => x.skill_id)
        refused = r.skipped.length
        if (ids.length === 0) {
          setError(`Nothing was added. ${r.skipped.map((s) => `${s.slug || s.path}: ${s.reason}`).join("; ") || "The repository had no skill to import."}`)
          return
        }
      }
      const pairs: Pair[] = chosenCrews.flatMap((c) => crewAgents(c.id).flatMap((a) => ids.map((skillId) => ({ agentId: a.id, skillId, on: true as const }))))
      if (pairs.length && !(await give(pairs, ids))) return
      finish(ids, refused)
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not add the skill")
    } finally {
      setBusy(false)
    }
  }

  const canNext = step === 0 ? (mode === "repo" ? from.trim() !== "" : content.trim() !== "") : step === 1 ? count > 0 : true
  const primary = () => {
    if (busy || !canNext) return
    if (step === 0) {
      if (mode === "repo") void lookForSkills()
      else setStep(1)
    } else if (step === 1) setStep(2)
    else void add()
  }

  const repoForCli = gitUrl || "https://github.com/<owner>/<repo>"
  const cli = [
    mode === "paste" ? "crewship skill import --file SKILL.md" : `crewship skill import --repo ${repoForCli}${ref.trim() ? ` --ref ${ref.trim()}` : ""}${pickedSkills.length ? ` --paths ${pickedSkills.map((p) => p.path).join(",")}` : ""}${unsafe ? " --unsafe-license" : ""}`,
    ...chosenCrews.map((c) => `crewship skill assign ${mode === "paste" ? (pasteName ?? "<skill>") : (pickedSkills[0]?.slug ?? "<skill>")} --to-crew ${c.slug}`),
  ]

  return (
    <CreateSurface
      open={open}
      onOpenChange={(o) => (o ? onOpenChange(true) : !busy && close())}
      size="md"
      dirty={dirty && !busy}
      discardLabel="this skill"
      onSubmit={primary}
      restoreOpenerFocus
    >
      <CreateSurfaceHeader
        concept="skills"
        icon={Plus}
        context="Skills"
        title="Add skill"
        description="Agent Skills from a repository: each one is a folder with SKILL.md."
        onBack={step > 0 && !busy && !failed ? () => setStep(step - 1) : undefined}
        onClose={close}
      />
      <CreateSurfaceSteps steps={STEPS} current={step} onJump={(i) => !busy && !failed && setStep(i)} ariaLabel="Add skill steps" />

      <CreateSurfaceBody className="flex flex-col gap-3.5" data-slot="add-skill-body">
        {step === 0 && mode === "repo" && (
          <>
            <CreateSurfaceField
              label="From"
              htmlFor="add-skill-from"
              hint="owner/repo, a GitHub or GitLab URL, or any https git URL"
            >
              <Input
                id="add-skill-from"
                autoFocus
                value={from}
                onChange={(e) => setFrom(e.target.value)}
                placeholder="anthropics/skills"
                disabled={busy}
                className={cn(CREATE_SURFACE_INPUT, "font-mono")}
              />
            </CreateSurfaceField>
            {recentSources.length > 0 && (
              <div className="flex flex-wrap gap-1.5" aria-label="Sources in the library">
                {recentSources.map((s) => (
                  <button
                    key={s.url}
                    type="button"
                    onClick={() => setFrom(s.label)}
                    aria-pressed={from === s.label}
                    className={cn(
                      "kit-tap inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 font-mono text-label",
                      from === s.label ? "border-primary/40 bg-primary/15 text-primary-hover" : "border-border text-muted-foreground hover:text-foreground",
                    )}
                  >
                    <FolderGit2 className="size-3" aria-hidden />
                    {s.label}
                  </button>
                ))}
              </div>
            )}
            <CreateSurfaceGrid>
              <CreateSurfaceField label="Ref" htmlFor="add-skill-ref" hint="Branch or tag; the default branch when empty">
                <Input id="add-skill-ref" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="main" disabled={busy} className={cn(CREATE_SURFACE_INPUT, "font-mono")} />
              </CreateSurfaceField>
            </CreateSurfaceGrid>
            {busy && <CreateSurfaceLoading rows={2} />}
            <div className="flex items-center gap-2 text-label text-muted-foreground-soft" aria-hidden>
              <span className="h-px flex-1 bg-border" />
              or
              <span className="h-px flex-1 bg-border" />
            </div>
            <div className="grid gap-2 sm:grid-cols-3" role="group" aria-label="Other ways to add a skill">
              {(
                [
                  [ClipboardPaste, "Paste SKILL.md", "Its frontmatter names it", () => setMode("paste")],
                  [Sparkles, "Describe it", "Crewship writes it with AI", () => (close(), onEditor("describe"))],
                  [PencilLine, "Write it", "Start from a template", () => (close(), onEditor("write"))],
                ] as const
              ).map(([Icon, label, hint, go]) => (
                <button
                  key={label}
                  type="button"
                  onClick={go}
                  className="kit-tap flex items-start gap-2.5 rounded-lg border border-border bg-foreground/[0.02] p-2.5 text-left hover:bg-foreground/[0.05]"
                >
                  <Icon className="mt-0.5 size-4 shrink-0 text-primary-hover" aria-hidden />
                  <span className="min-w-0">
                    <b className="block text-control font-medium">{label}</b>
                    <small className="block text-label text-muted-foreground">{hint}</small>
                  </span>
                </button>
              ))}
            </div>
          </>
        )}

        {step === 0 && mode === "paste" && (
          <>
            <CreateSurfaceField label="SKILL.md" htmlFor="add-skill-content" hint="Frontmatter with name and description, then the instructions">
              <Textarea
                id="add-skill-content"
                autoFocus
                rows={12}
                value={content}
                onChange={(e) => setContent(e.target.value)}
                placeholder={"---\nname: my-skill\ndescription: Use when…\n---\n\n## Instructions\n…"}
                className="font-mono text-label"
              />
            </CreateSurfaceField>
            <button type="button" className="self-start text-label text-primary-hover hover:underline" onClick={() => setMode("repo")}>
              ‹ From a repository instead
            </button>
          </>
        )}

        {step === 1 && mode === "repo" && found && (
          <>
            <p className="text-control text-muted-foreground">
              Found {found.length} {found.length === 1 ? "skill" : "skills"} in <span className="font-mono text-foreground">{gitUrl.replace(/^https:\/\//, "")}{ref.trim() ? `@${ref.trim()}` : ""}</span>. The scan runs on import; a flagged skill is added as Flagged.
            </p>
            {found.length === 0 && <CreateSurfaceNotice tone="warn">This repository has no SKILL.md files.</CreateSurfaceNotice>}
            <ul className="flex flex-col divide-y divide-border rounded-lg border border-border" data-slot="add-skill-found">
              {found.map((f) => (
                <li key={f.path}>
                  <label className={cn("flex min-h-[44px] items-start gap-2.5 px-3 py-2", f.refused ? "cursor-not-allowed opacity-70" : "cursor-pointer")}>
                    <Checkbox
                      className="mt-0.5"
                      checked={picked.has(f.path)}
                      disabled={!!f.refused}
                      aria-label={f.slug}
                      onCheckedChange={(v) =>
                        setPicked((s) => {
                          const next = new Set(s)
                          if (v === true) next.add(f.path)
                          else next.delete(f.path)
                          return next
                        })
                      }
                    />
                    <span className="min-w-0 flex-1">
                      <b className="block truncate font-mono text-control font-medium">{f.slug}</b>
                      <small className="block text-label text-muted-foreground">
                        {f.refused ?? (takenSlugs.has(f.slug) ? "A skill with this slug exists · importing replaces it" : "Licence on the allow list")}
                      </small>
                    </span>
                    <span className="shrink-0 font-mono text-micro text-muted-foreground-soft">{f.path}</span>
                  </label>
                </li>
              ))}
            </ul>
            {found.some((f) => f.licence) && (
              <CreateSurfaceToggleRow
                label={<label htmlFor="add-skill-unsafe" className="cursor-pointer">Allow licences outside the allow list</label>}
                hint="Recorded in the journal. Leave it off unless you know the source."
                control={
                  <Checkbox
                    id="add-skill-unsafe"
                    checked={unsafe}
                    disabled={busy}
                    onCheckedChange={(v) => {
                      setUnsafe(v === true)
                      void lookForSkills(v === true)
                    }}
                  />
                }
              />
            )}
          </>
        )}

        {step === 1 && mode === "paste" && (
          <CreateSurfaceNotice tone={pasteName ? "info" : "warn"}>
            {pasteName ? (
              <>
                1 skill: <b className="font-mono">{pasteName}</b>
                {takenSlugs.has(pasteName) ? " · a skill with this slug exists and is replaced" : ""}. The licence gate and the scan run when it is added.
              </>
            ) : (
              "The frontmatter has no name, so the import will refuse it. Go back and add name: and description:."
            )}
          </CreateSurfaceNotice>
        )}

        {step === 2 && (
          <>
            <CreateSurfaceField label="Give to" hint="Or add to the library only and give it later.">
              <div className="flex flex-col divide-y divide-border rounded-lg border border-border" data-slot="add-skill-crews">
                {crews.length === 0 && <p className="px-3 py-2 text-label text-muted-foreground">No crews yet.</p>}
                {crews.map((c) => {
                  const n = crewAgents(c.id).length
                  return (
                    <label key={c.id} className="flex min-h-[44px] cursor-pointer items-center gap-2.5 px-3 py-2 text-control">
                      <Checkbox
                        checked={crewIds.has(c.id)}
                        disabled={n === 0}
                        aria-label={c.name}
                        onCheckedChange={(v) =>
                          setCrewIds((s) => {
                            const next = new Set(s)
                            if (v === true) next.add(c.id)
                            else next.delete(c.id)
                            return next
                          })
                        }
                      />
                      <CrewIcon icon={c.icon ?? "users"} color={c.color} size="sm" className="h-5 w-5 rounded-md [&>svg]:h-3 [&>svg]:w-3" />
                      <span>
                        {c.name} · {n === 0 ? "no agents" : `all ${n} ${n === 1 ? "agent" : "agents"}`}
                      </span>
                    </label>
                  )
                })}
              </div>
            </CreateSurfaceField>
            <div className="flex flex-col gap-1 rounded-md border border-border bg-muted/40 px-3 py-2" data-slot="add-skill-cli">
              <small className="flex items-center gap-1.5 text-label text-muted-foreground">
                <Terminal className="size-3.5" aria-hidden />
                Same as
              </small>
              {cli.map((c) => (
                <code key={c} className="break-all font-mono text-label text-foreground">
                  {c}
                </code>
              ))}
            </div>
          </>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceRefusal message={error} tone={failed ? "warn" : "error"} onDismiss={failed ? undefined : () => setError(null)} />

      <CreateSurfaceFooter
        lead={<span className="text-label tabular-nums text-muted-foreground max-sm:hidden">Step {step + 1} of 3</span>}
        hint={null}
        onCancel={close}
        cancelLabel={failed ? "Close" : "Cancel"}
        guardCancel={!failed}
        secondary={
          step === 2 && !failed ? (
            <CreateSurfaceSecondaryAction onClick={() => setStep(1)} disabled={busy}>
              Back
            </CreateSurfaceSecondaryAction>
          ) : undefined
        }
        primaryLabel={
          failed ? "Retry" : step === 0 ? (mode === "repo" ? (busy ? "Looking…" : "Look for skills") : "Continue") : step === 1 ? "Continue" : `Add ${count} ${count === 1 ? "skill" : "skills"}`
        }
        onPrimary={primary}
        primaryDisabled={!canNext}
        busy={busy}
      />
    </CreateSurface>
  )
}
