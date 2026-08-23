"use client"

/**
 * Routines — New routine, Import.
 *
 * New routine is the surface that currently changes width three times: 576px
 * at the door, 672px in the fork list, 768px and 90vh tall in the editor
 * (routine-create-dialog.tsx). Here it is `lg` throughout, and the three modes
 * become a first screen you go BACK from rather than three dialogs wearing one
 * name. Every field of the original is present: goal, lead agent, name, slug,
 * description, DSL format, the editor, the dry-run gate and Test-and-save.
 */

import * as React from "react"
import {
  AlertTriangle,
  Braces,
  FileJson,
  FlaskConical,
  GitFork,
  Save,
  Search,
  Sparkles,
  Upload,
  Wand2,
} from "lucide-react"

import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import {
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceDescriptionInput,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceGrid,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceSecondaryAction,
  CreateSurfaceSection,
  CreateSurfaceTile,
  CreateSurfaceTitleInput,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"

/* ══ Routines → New routine ═════════════════════════════════════════════ */

type Mode = "entry" | "describe" | "fork" | "advanced"

const FORKABLE = [
  { slug: "nightly-sweep", name: "Nightly sweep", runs: "412 runs" },
  { slug: "pr-triage", name: "PR triage", runs: "1 204 runs" },
  { slug: "digest-0900", name: "Morning digest", runs: "87 runs" },
]

const STARTERS = [
  { id: "blank", label: "Blank", hint: "one step, no trigger" },
  { id: "schedule", label: "On a schedule", hint: "cron + one agent step" },
  { id: "webhook", label: "On a webhook", hint: "payload → agent" },
]

export function NewRoutineContent({ onClose }: { onClose: () => void }) {
  const [mode, setMode] = React.useState<Mode>("entry")

  // describe
  const [goal, setGoal] = React.useState("")
  const [lead, setLead] = React.useState("keeper")

  // fork
  const [forkSearch, setForkSearch] = React.useState("")
  const [fork, setFork] = React.useState<string | null>(null)

  // advanced / shared
  const [name, setName] = React.useState("")
  const [description, setDescription] = React.useState("")
  const [dslFormat, setDslFormat] = React.useState<"yaml" | "json">("yaml")
  const [starter, setStarter] = React.useState("schedule")
  const [skipTestGate, setSkipTestGate] = React.useState(false)

  const title =
    mode === "entry"
      ? "New routine"
      : mode === "describe"
        ? "Describe it"
        : mode === "fork"
          ? "Fork a routine"
          : "Editor"

  const primary =
    mode === "entry" ? "Continue" : mode === "advanced" ? "Test & save" : mode === "fork" ? "Fork" : "Draft it"

  const valid =
    mode === "entry" ? true : mode === "describe" ? goal.trim() !== "" : mode === "fork" ? fork !== null : name.trim() !== ""

  return (
    <>
      <CreateSurfaceHeader
        concept="routines"
        context="Platform"
        title={title}
        description={
          mode === "entry" ? "Three ways in. All three land on the same routine — pick the one you have inputs for." : undefined
        }
        onBack={mode === "entry" ? undefined : () => setMode("entry")}
        onClose={onClose}
      />

      <CreateSurfaceBody className="space-y-4">
        {mode === "entry" && (
          <>
            <CreateSurfaceTile
              icon={Sparkles}
              accent="gold"
              title="Describe what it should do"
              description="A lead agent drafts the definition from a sentence. You review before anything is saved."
              meta="fastest"
              onClick={() => setMode("describe")}
            />
            <CreateSurfaceTile
              icon={GitFork}
              accent="purple"
              title="Fork one of your own"
              description="Copy an existing routine and change what differs. Keeps the schedule and the steps."
              onClick={() => setMode("fork")}
            />
            <CreateSurfaceTile
              icon={Braces}
              accent="teal"
              title="Write the definition"
              description="The YAML/JSON editor, with starter templates and a dry run before save."
              meta="full control"
              onClick={() => setMode("advanced")}
            />
          </>
        )}

        {mode === "describe" && (
          <>
            <CreateSurfaceSection title="Goal" icon={Sparkles} accent="gold">
              <CreateSurfaceDescriptionInput
                autoFocus
                value={goal}
                onChange={(e) => setGoal(e.target.value)}
                rows={4}
                placeholder="Every weekday at 09:00, summarise yesterday's merged PRs and post it to #eng."
                className="rounded-lg border border-hairline bg-foreground/[0.02] p-2.5 text-xs leading-relaxed"
              />
            </CreateSurfaceSection>
            <CreateSurfaceField
              label="Drafted by"
              htmlFor="routine-lead"
              hint="A lead agent writes the definition. You get the diff, not a saved routine."
            >
              <select
                id="routine-lead"
                value={lead}
                onChange={(e) => setLead(e.target.value)}
                className="h-8 w-full rounded-md border border-hairline bg-background px-2 text-xs text-foreground outline-none focus:border-primary max-sm:h-12 max-sm:text-sm"
              >
                <option value="keeper">keeper — platform</option>
                <option value="filip">filip — backend lead</option>
              </select>
            </CreateSurfaceField>
          </>
        )}

        {mode === "fork" && (
          <>
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground-soft" />
              <Input
                autoFocus
                value={forkSearch}
                onChange={(e) => setForkSearch(e.target.value)}
                placeholder="Search your routines…"
                className="h-8 pl-8 text-xs max-sm:h-12 max-sm:text-sm"
              />
            </div>
            {FORKABLE.filter((r) => r.name.toLowerCase().includes(forkSearch.toLowerCase())).map((r) => (
              <CreateSurfaceTile
                key={r.slug}
                concept="routines"
                title={r.name}
                description={r.slug}
                meta={r.runs}
                selected={fork === r.slug}
                onClick={() => setFork(r.slug)}
              />
            ))}
          </>
        )}

        {mode === "advanced" && (
          <>
            <CreateSurfaceSection title="Identity" concept="routines">
              <CreateSurfaceTitleInput
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Routine name"
              />
              <CreateSurfaceGrid>
                <CreateSurfaceField label="Slug" htmlFor="routine-slug" required>
                  <Input
                    id="routine-slug"
                    placeholder={name.trim().toLowerCase().replace(/\s+/g, "-") || "nightly-sweep"}
                    className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
                  />
                </CreateSurfaceField>
                <CreateSurfaceField label="Owned by" htmlFor="routine-crew">
                  <select
                    id="routine-crew"
                    className="h-8 w-full rounded-md border border-hairline bg-background px-2 text-xs text-foreground outline-none focus:border-primary max-sm:h-12 max-sm:text-sm"
                  >
                    <option>platform</option>
                    <option>infra</option>
                  </select>
                </CreateSurfaceField>
              </CreateSurfaceGrid>
              <CreateSurfaceField label="Description">
                <Input
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                  placeholder="What it does, in one line."
                  className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
                />
              </CreateSurfaceField>
            </CreateSurfaceSection>

            <CreateSurfaceSection title="Definition" icon={Braces} accent="teal">
              <div className="flex flex-wrap items-center gap-2">
                <CreateSurfaceChoice
                  ariaLabel="DSL format"
                  value={dslFormat}
                  onChange={setDslFormat}
                  options={[
                    { value: "yaml", label: "YAML" },
                    { value: "json", label: "JSON" },
                  ]}
                />
                <select
                  aria-label="Starter template"
                  value={starter}
                  onChange={(e) => setStarter(e.target.value)}
                  className="h-8 rounded-md border border-hairline bg-background px-2 text-xs text-foreground outline-none focus:border-primary max-sm:h-12 max-sm:w-full max-sm:text-sm"
                >
                  {STARTERS.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.label} — {s.hint}
                    </option>
                  ))}
                </select>
              </div>
              <pre className="max-h-56 overflow-auto rounded-lg border border-hairline bg-background p-2.5 font-mono text-[11px] leading-relaxed text-foreground/80">
{`name: ${name || "nightly-sweep"}
on:
  schedule: "0 9 * * 1-5"
steps:
  - agent: keeper
    prompt: |
      Summarise yesterday's merged PRs.
  - notify: "#eng"`}
              </pre>
            </CreateSurfaceSection>

            <CreateSurfaceToggleRow
              icon={FlaskConical}
              accent="amber"
              label="Skip the dry run"
              hint="Off means the routine is executed once against a sandbox before it is saved. Leave it off."
              control={<Switch checked={skipTestGate} onCheckedChange={setSkipTestGate} />}
            />
            {skipTestGate && (
              <CreateSurfaceNotice tone="warn" icon={AlertTriangle}>
                Saving without a dry run means the first real trigger is the first execution.
              </CreateSurfaceNotice>
            )}
          </>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        hint={mode === "advanced" ? "A dry run happens before the save unless you skipped it." : undefined}
        onCancel={onClose}
        secondary={
          mode === "advanced" ? (
            <CreateSurfaceSecondaryAction icon={FlaskConical}>Test only</CreateSurfaceSecondaryAction>
          ) : undefined
        }
        primaryLabel={primary}
        primaryIcon={mode === "advanced" ? Save : mode === "describe" ? Wand2 : undefined}
        primaryDisabled={!valid}
        onPrimary={() => (mode === "entry" ? setMode("describe") : onClose())}
      />
    </>
  )
}

/* ══ Routines → Import ══════════════════════════════════════════════════ */

export function ImportRoutineContent({ onClose }: { onClose: () => void }) {
  const [json, setJson] = React.useState("")
  const [replace, setReplace] = React.useState(false)

  return (
    <>
      <CreateSurfaceHeader
        icon={Upload}
        accent="purple"
        context="Routines"
        title="Import a bundle"
        description="Exported from another workspace, or shared by an agent. Authorship metadata is preserved."
        onClose={onClose}
      />

      <CreateSurfaceBody className="space-y-3">
        <CreateSurfaceField label="Bundle JSON" htmlFor="routine-bundle">
          <textarea
            id="routine-bundle"
            autoFocus
            value={json}
            onChange={(e) => setJson(e.target.value)}
            rows={10}
            placeholder='{"slug":"…","definition":{…},"versions":[…]}'
            className="w-full resize-none rounded-lg border border-hairline bg-background p-2.5 font-mono text-[11px] text-foreground outline-none focus:border-primary"
          />
        </CreateSurfaceField>

        <CreateSurfaceToggleRow
          icon={FileJson}
          accent="amber"
          label="Replace on slug collision"
          hint="Off refuses the import when a routine of that slug already exists here, which is the safe default."
          control={<Switch checked={replace} onCheckedChange={setReplace} />}
        />

        {replace && (
          <CreateSurfaceNotice tone="warn" icon={AlertTriangle}>
            The existing routine&apos;s history is kept, but its current definition is overwritten.
          </CreateSurfaceNotice>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        onCancel={onClose}
        primaryLabel="Import"
        primaryIcon={Upload}
        primaryDisabled={!json.trim()}
        onPrimary={onClose}
      />
    </>
  )
}
