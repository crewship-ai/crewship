"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { toast } from "sonner"
import { FileText, KeyRound, Palette, Sparkles } from "lucide-react"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfacePicker,
  CreateSurfaceRefusal,
  CreateSurfaceSection,
  CreateSurfaceTitleInput,
} from "@/components/layout/create-surface"
import { StatusPill } from "@/components/ui/status-pill"
import { Textarea } from "@/components/ui/textarea"
import { Input } from "@/components/ui/input"
import { useGenerateSkill, useImportSkill } from "@/hooks/use-skills"
import { CREW_ICON_CATEGORIES, getCrewIconDef, searchCrewIcons } from "@/lib/entities"
import { cn } from "@/lib/utils"
import { SkillTile } from "./skill-card"
import { SKILL_BODY_TEMPLATE, patchFrontmatter, skillMarkdown, slugifySkill, type SkillMeta } from "./skill-doc"
import { DOMAIN_ORDER, domainMeta, skillIconName, skillName, skillTags, type SkillDetail } from "./skills-model"

// New skill, Edit and Duplicate (#3033) on the shared CreateSurface shell.
//
// Two ways to make a skill. "Describe it" asks the generator to write the
// SKILL.md; "Write it" builds the SKILL.md here. Both end at the import
// endpoint, because that is the call that stores the frontmatter `icon` and
// `category` — the generator stores neither, so its output is re-imported
// with the icon and domain picked here. The importer upserts by slug, which is
// also how Edit saves, and refuses to overwrite a built-in skill.

export type SkillEditorTarget =
  | { kind: "new" }
  | { kind: "edit"; skill: SkillDetail; panel?: "icon" }
  | { kind: "duplicate"; skill: SkillDetail }

type Method = "describe" | "write"

const ENV_VAR = /^[A-Z_][A-Z0-9_]*$/

function parseCredentials(text: string): { list: string[]; bad: string[] } {
  const list = text
    .split(/[\s,;]+/)
    .map((x) => x.trim())
    .filter(Boolean)
  return { list, bad: list.filter((x) => !ENV_VAR.test(x)) }
}

interface Draft {
  method: Method
  name: string
  icon: string | null
  category: string
  prompt: string
  description: string
  body: string
  credentials: string
}

function draftFor(target: SkillEditorTarget): Draft {
  if (target.kind === "new") {
    return { method: "describe", name: "", icon: null, category: "CUSTOM", prompt: "", description: "", body: SKILL_BODY_TEMPLATE, credentials: "" }
  }
  const s = target.skill
  return {
    method: "write",
    name: target.kind === "duplicate" ? `${skillName(s)} copy` : skillName(s),
    icon: skillIconName(s.icon),
    category: s.category || "CUSTOM",
    prompt: "",
    description: s.description ?? "",
    body: s.content ?? "",
    credentials: (s.needs_credentials ?? []).join(", "),
  }
}

export function SkillEditor({
  target,
  workspaceId,
  takenSlugs,
  onClose,
  onSaved,
}: {
  target: SkillEditorTarget | null
  workspaceId: string
  /** Slugs already in the catalog: a new skill must not silently update one. */
  takenSlugs: Set<string>
  onClose: () => void
  onSaved: (skillId: string) => void
}) {
  const generate = useGenerateSkill(workspaceId)
  const save = useImportSkill(workspaceId)
  const [draft, setDraft] = useState<Draft>(() => draftFor(target ?? { kind: "new" }))
  const [panel, setPanel] = useState<null | "icon">(null)
  const [iconQuery, setIconQuery] = useState("")
  const [iconCategory, setIconCategory] = useState<string | null>(null)
  const [refusal, setRefusal] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const nameRef = useRef<HTMLInputElement>(null)

  const open = target !== null
  const editing = target?.kind === "edit"
  // Reset per opening: the target object changes identity on every open.
  useEffect(() => {
    if (!target) return
    setDraft(draftFor(target))
    setPanel(target.kind === "edit" && target.panel === "icon" ? "icon" : null)
    setIconQuery("")
    setIconCategory(null)
    setRefusal(null)
    if (target.kind !== "edit") setTimeout(() => nameRef.current?.focus(), 100)
  }, [target])

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => ({ ...d, [k]: v }))
  const slug = editing ? target.skill.slug : slugifySkill(draft.name)
  const taken = !editing && slug !== "" && takenSlugs.has(slug)
  const creds = parseCredentials(draft.credentials)
  const initial = useMemo(() => (target ? draftFor(target) : null), [target])
  const dirty = !!initial && JSON.stringify(initial) !== JSON.stringify(draft)

  const meta: SkillMeta = {
    name: slug || "new-skill",
    display_name: draft.name.trim(),
    description: draft.description.trim(),
    category: draft.category,
    icon: draft.icon,
    credential_requirements: creds.list,
    ...(target && target.kind !== "new"
      ? {
          version: target.kind === "edit" ? target.skill.version : null,
          author: target.skill.author,
          license: target.skill.spdx_license || target.skill.license,
          tags: skillTags(target.skill),
        }
      : {}),
  }
  const preview = draft.method === "write" ? skillMarkdown(meta, draft.body) : ""

  const problem = (() => {
    if (!draft.name.trim() || !slug) return "Give the skill a name."
    if (taken) return `A skill called ${slug} already exists. Pick another name.`
    if (draft.method === "describe") return draft.prompt.trim().length < 20 ? "Describe what it should do in a sentence or two." : null
    if (!draft.description.trim()) return "Say when agents should use it."
    if (!draft.body.trim()) return "Write its instructions."
    if (creds.bad.length) return `${creds.bad.join(", ")} is not an environment variable name. Use capitals, digits and underscores.`
    return null
  })()

  const submit = async () => {
    if (panel) {
      setPanel(null)
      return
    }
    if (problem || busy) return
    setBusy(true)
    setRefusal(null)
    try {
      if (draft.method === "write") {
        const res = await save.mutateAsync({ content: preview })
        toast.success(editing ? `${draft.name.trim()} saved. Agents load the new version on their next run.` : `${draft.name.trim()} created`)
        onSaved(res.skill_id)
        return
      }
      const gen = await generate.mutateAsync({ slug, prompt: draft.prompt.trim() })
      const patch: Record<string, string> = { name: gen.slug, display_name: draft.name.trim(), category: draft.category }
      if (draft.icon) patch.icon = draft.icon
      try {
        await save.mutateAsync({ content: patchFrontmatter(gen.content, patch) })
        toast.success(`${draft.name.trim()} written. Give it to an agent to use it.`)
      } catch (e) {
        toast.warning(`${draft.name.trim()} was written, but its icon and domain were not saved: ${e instanceof Error ? e.message : "unknown error"}`)
      }
      onSaved(gen.skill_id)
    } catch (e) {
      setRefusal(e instanceof Error ? e.message : "Could not save the skill")
    } finally {
      setBusy(false)
    }
  }

  const iconResults = useMemo(() => searchCrewIcons(iconCategory ?? iconQuery), [iconQuery, iconCategory])
  const title =
    panel === "icon"
      ? "Icon"
      : target?.kind === "edit"
        ? `Edit ${skillName(target.skill)}`
        : target?.kind === "duplicate"
          ? "Duplicate skill"
          : "New skill"
  const primary = panel ? "Use this icon" : draft.method === "describe" ? "Write skill" : editing ? "Save changes" : "Create skill"
  const dm = domainMeta(draft.category)

  return (
    <CreateSurface
      open={open}
      onOpenChange={(o) => !o && !busy && onClose()}
      size="xl"
      dirty={dirty}
      discardLabel="this skill"
      onSubmit={() => void submit()}
    >
      <CreateSurfaceHeader
        concept="skills"
        context="Skills"
        title={title}
        description={
          panel === "icon"
            ? "Every skill wears the same tint; the icon tells them apart. Without one it shows its domain's."
            : "A skill is a SKILL.md agents load on their next run."
        }
        onBack={panel ? () => setPanel(null) : undefined}
        onClose={onClose}
      />

      <CreateSurfaceBody className="flex flex-col gap-4">
        {panel === "icon" ? (
          <CreateSurfacePicker
            preview={<SkillTile category={draft.category} icon={draft.icon} size="lg" />}
            previewHint={draft.icon ? getCrewIconDef(draft.icon).label : `${dm.label} domain icon`}
            inherit={{
              label: "Use the domain's icon",
              hint: dm.label,
              active: draft.icon === null,
              onSelect: () => set("icon", null),
            }}
            categories={{
              value: iconCategory,
              options: CREW_ICON_CATEGORIES,
              onChange: (c) => {
                setIconCategory(c)
                setIconQuery("")
              },
            }}
            search={{
              value: iconQuery,
              onChange: (v) => {
                setIconQuery(v)
                setIconCategory(null)
              },
              placeholder: "Search icons…",
            }}
            options={iconResults.map((n) => {
              const def = getCrewIconDef(n)
              return { id: n, label: def.label, render: <def.icon className="h-4 w-4 text-foreground/70" /> }
            })}
            value={draft.icon}
            onChange={(n) => set("icon", n)}
            columns={8}
          />
        ) : (
          <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_280px]">
            <div className="flex min-w-0 flex-col gap-4">
              {target?.kind === "new" && (
                <CreateSurfaceChoice<Method>
                  ariaLabel="How to make it"
                  value={draft.method}
                  onChange={(m) => set("method", m)}
                  options={[
                    { value: "describe", label: <span className="inline-flex items-center gap-1.5"><Sparkles className="h-3.5 w-3.5" />Describe it</span>, hint: "Claude writes the SKILL.md" },
                    { value: "write", label: <span className="inline-flex items-center gap-1.5"><FileText className="h-3.5 w-3.5" />Write it</span>, hint: "You write the SKILL.md" },
                  ]}
                />
              )}

              <CreateSurfaceSection title="Identity" icon={Palette} accent="lime">
                <div className="flex items-start gap-3">
                  <button
                    type="button"
                    aria-label="Change skill icon"
                    onClick={() => setPanel("icon")}
                    className="shrink-0 rounded-xl transition-transform hover:scale-105"
                  >
                    <SkillTile category={draft.category} icon={draft.icon} size="lg" />
                  </button>
                  <div className="min-w-0 flex-1">
                    <label htmlFor="skill-name" className="sr-only">
                      Skill name
                    </label>
                    <CreateSurfaceTitleInput
                      id="skill-name"
                      ref={nameRef}
                      value={draft.name}
                      onChange={(e) => set("name", e.target.value)}
                      placeholder="Invoice matcher"
                    />
                    <p className={cn("mt-1 font-mono text-micro", taken ? "text-destructive" : "text-muted-foreground-soft")}>
                      {slug || "slug appears here"}
                      {editing && " · the slug stays"}
                      {taken && " · taken"}
                    </p>
                  </div>
                </div>
                <CreateSurfaceField label="Domain">
                  <CreateSurfaceChoice
                    ariaLabel="Domain"
                    value={draft.category}
                    onChange={(c) => set("category", c)}
                    options={DOMAIN_ORDER.map((d) => {
                      const m = domainMeta(d)
                      return { value: d, label: <span className="inline-flex items-center gap-1.5"><m.icon className="h-3.5 w-3.5" />{m.label}</span> }
                    })}
                  />
                </CreateSurfaceField>
              </CreateSurfaceSection>

              {draft.method === "describe" ? (
                <CreateSurfaceField label="What should it do, and when?" htmlFor="skill-prompt" hint="Claude writes the SKILL.md from this with the workspace's Anthropic key. The import scan checks it before it lands in the catalog.">
                  <Textarea
                    id="skill-prompt"
                    rows={6}
                    value={draft.prompt}
                    onChange={(e) => set("prompt", e.target.value)}
                    placeholder="Match incoming payments to open invoices by amount, date window and reference. Use when a payment arrives without an invoice number. Never mark an invoice paid; propose the match."
                  />
                </CreateSurfaceField>
              ) : (
                <>
                  <CreateSurfaceField label="When should agents use it?" htmlFor="skill-description" hint="One sentence. Agents read it to decide whether to load the skill.">
                    <Input
                      id="skill-description"
                      value={draft.description}
                      onChange={(e) => set("description", e.target.value)}
                      placeholder="Match payments to open invoices when a payment has no invoice number."
                    />
                  </CreateSurfaceField>
                  <CreateSurfaceField
                    label="Instructions"
                    htmlFor="skill-body"
                    hint="Markdown. Sections called When to Activate, Instructions, Output Format and Guardrails show up structured on the skill's Overview."
                  >
                    <Textarea id="skill-body" rows={12} value={draft.body} onChange={(e) => set("body", e.target.value)} className="font-mono text-label" />
                  </CreateSurfaceField>
                  <CreateSurfaceField label="Credentials it needs" htmlFor="skill-creds" hint="Environment variable names, separated by commas. Agents without them get a warning on the Skills page.">
                    <div className="relative">
                      <KeyRound className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground-soft" />
                      <Input
                        id="skill-creds"
                        value={draft.credentials}
                        onChange={(e) => set("credentials", e.target.value)}
                        placeholder="GH_TOKEN, SLACK_BOT_TOKEN"
                        className="pl-8 font-mono"
                      />
                    </div>
                  </CreateSurfaceField>
                </>
              )}
            </div>

            <aside className="flex min-w-0 flex-col gap-3 max-lg:hidden" aria-label="Preview">
              <h4 className="eyebrow">In the catalog</h4>
              <div className="flex flex-col gap-2 rounded-card border border-border bg-card p-3" data-testid="skill-editor-preview">
                <div className="flex min-w-0 items-center gap-2.5">
                  <SkillTile category={draft.category} icon={draft.icon} size="sm" />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-control font-semibold">{draft.name.trim() || "New skill"}</div>
                    <div className="truncate font-mono text-micro text-muted-foreground-soft">{slug || "slug"}</div>
                  </div>
                  <StatusPill tone="warn" label="Not scanned" />
                </div>
                <p className="line-clamp-2 min-h-9 text-label text-muted-foreground">
                  {(draft.method === "write" ? draft.description : draft.prompt).trim() || "What it does shows here."}
                </p>
                <div className="flex gap-1.5">
                  <span className="inline-flex items-center gap-1 rounded-full border border-border px-2 py-px text-micro text-muted-foreground">
                    <dm.icon className="h-3 w-3" aria-hidden />
                    {dm.label}
                  </span>
                  <span className="inline-flex items-center rounded-full border border-border px-2 py-px text-micro text-muted-foreground">
                    {editing ? (target.skill.source === "GENERATED" ? "Generated" : "Imported") : draft.method === "describe" ? "Generated" : "Imported"}
                  </span>
                </div>
              </div>
              {draft.method === "write" ? (
                <>
                  <h4 className="eyebrow mt-1">SKILL.md</h4>
                  <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-lg border border-border bg-card p-3 font-mono text-micro leading-relaxed text-foreground/85">
                    {preview}
                  </pre>
                </>
              ) : (
                <ol className="flex flex-col gap-2 text-label text-muted-foreground">
                  {["Claude writes the SKILL.md from your text.", "The import scan checks it for unsafe instructions.", "It lands in the catalog with this icon and domain.", "You pick the agents that get it."].map((t, i) => (
                    <li key={t} className="flex gap-2">
                      <span className="grid h-[18px] w-[18px] shrink-0 place-items-center rounded-md bg-foreground/[0.06] font-mono text-micro text-foreground">{i + 1}</span>
                      {t}
                    </li>
                  ))}
                </ol>
              )}
            </aside>
          </div>
        )}
      </CreateSurfaceBody>

      {!panel && <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />}

      <CreateSurfaceFooter
        lead={!panel && problem && (draft.name || draft.prompt || draft.description) ? <span className="text-label text-muted-foreground">{problem}</span> : undefined}
        onCancel={panel ? () => setPanel(null) : onClose}
        guardCancel={!panel}
        cancelLabel={panel ? "Back" : "Cancel"}
        primaryLabel={busy ? (draft.method === "describe" ? "Writing…" : "Saving…") : primary}
        onPrimary={() => void submit()}
        primaryDisabled={panel ? false : !!problem || busy}
        busy={!panel && busy}
      />
    </CreateSurface>
  )
}
