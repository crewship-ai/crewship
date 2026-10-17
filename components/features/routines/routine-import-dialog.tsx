"use client"

// Import a routine (Harbor 2 D-RTIMPORT): a bundle exported from another
// workspace, or a routine definition, chosen as a file or pasted. It becomes a
// DRAFT in the crew you pick — read it, test it and publish it like any other
// change. The import endpoint publishes straight away, so the client takes the
// definition out of the bundle and saves it through POST /pipelines/drafts.

import * as React from "react"
import { AlertTriangle, CheckCircle2, FileUp, Upload } from "lucide-react"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { toast } from "@/lib/toast"
import { cn } from "@/lib/utils"
import { saveRoutineDraft, RoutineDraftError } from "@/lib/routine-drafts"
import { parseImportedRoutine } from "@/lib/routine-page"
import { SLUG_RE } from "@/lib/routine-wizard"
import { isRecord, asString } from "@/lib/routine-step-describe"
import { foreachBody } from "@/lib/routine-steps-layout"
import { useWorkspaceAgentDirectory } from "@/hooks/use-workspace-agent-directory"

const MAX_BYTES = 1024 * 1024

export interface ImportCrew {
  id: string
  name: string
}

/** The agents a definition names, in its steps and loop bodies. */
export function definitionAgents(definition: Record<string, unknown>): string[] {
  const steps = Array.isArray(definition.steps) ? definition.steps.filter(isRecord) : []
  const all = [...steps, ...steps.flatMap(foreachBody)]
  return Array.from(new Set(all.map((s) => asString(s.agent_slug)).filter(Boolean)))
}

export function ImportRoutineDialog({
  workspaceId,
  crews = [],
  defaultCrewId = null,
  onClose,
  onImported,
}: {
  workspaceId: string
  crews?: ImportCrew[]
  defaultCrewId?: string | null
  onClose: () => void
  /** The draft was saved; opens its page. */
  onImported: (slug: string) => void
}) {
  const [text, setText] = React.useState("")
  const [crewId, setCrewId] = React.useState<string | null>(defaultCrewId)
  const [slug, setSlug] = React.useState("")
  const [clash, setClash] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const fileRef = React.useRef<HTMLInputElement>(null)
  const ids = { text: React.useId(), slug: React.useId() }
  const { agents } = useWorkspaceAgentDirectory(workspaceId)
  const parsed = React.useMemo(() => (text.trim() ? parseImportedRoutine(text) : null), [text])
  const routine = parsed?.ok ? parsed.routine : null

  // The ID follows the bundle until it clashes and the person renames it.
  React.useEffect(() => {
    if (routine && !clash) setSlug(routine.slug)
  }, [routine?.slug, clash]) // eslint-disable-line react-hooks/exhaustive-deps

  const crewAgents = React.useMemo(() => {
    const rows = (agents ?? []) as unknown as { slug: string; crew_id?: string | null }[]
    return new Set(rows.filter((a) => a.crew_id === crewId).map((a) => a.slug))
  }, [agents, crewId])
  const named = routine ? definitionAgents(routine.definition) : []
  const missing = crewId && agents ? named.filter((a) => !crewAgents.has(a)) : []
  const crewName = crews.find((c) => c.id === crewId)?.name
  const slugOk = SLUG_RE.test(slug)
  const ready = !!routine && !!crewId && slugOk && !busy

  const readFile = async (file: File) => {
    if (file.size > MAX_BYTES) {
      setRefusal("This file is larger than 1 MB. A routine bundle is much smaller; check it is the right file.")
      return
    }
    setRefusal(null)
    setClash(false)
    setText(await file.text())
  }

  const submit = async () => {
    if (!ready || !routine || !crewId) return
    setBusy(true)
    setRefusal(null)
    try {
      await saveRoutineDraft(
        workspaceId,
        { id: "", slug, revision: 0, base_pipeline_id: "", base_revision: 0, document: {} },
        {
          slug,
          name: routine.name || slug,
          description: routine.description,
          definition: { ...routine.definition, name: slug },
          author_crew_id: crewId,
        },
      )
      toast.success(`${routine.name || slug} is imported as a draft · not live`, { description: "Test it, then publish it to make it live." })
      onImported(slug)
    } catch (e) {
      if (e instanceof RoutineDraftError && e.status === 409) {
        setClash(true)
        setRefusal(`A routine called “${slug}” already exists. Pick another ID.`)
      } else setRefusal(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface
      open
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
      size="md"
      dirty={text.trim() !== ""}
      discardLabel="this bundle"
      onSubmit={() => void submit()}
    >
      <CreateSurfaceHeader
        icon={Upload}
        accent="purple"
        context="Routines"
        title="Import a routine"
        description="A bundle exported from another workspace, or a routine definition. It arrives as a draft: nothing runs until you publish it."
        onClose={onClose}
      />

      <CreateSurfaceBody className="flex flex-col gap-4">
        <div
          className="flex flex-col items-center gap-2 rounded-lg border border-dashed border-border px-4 py-5 text-center"
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault()
            const file = e.dataTransfer.files?.[0]
            if (file) void readFile(file)
          }}
        >
          <FileUp className="h-5 w-5 text-muted-foreground" aria-hidden />
          <p className="text-xs text-muted-foreground">Drop a .json bundle here, or</p>
          <Button type="button" variant="outline" size="sm" onClick={() => fileRef.current?.click()} className="coarse:h-11">
            Choose a file
          </Button>
          <input
            ref={fileRef}
            type="file"
            accept="application/json,.json"
            className="sr-only"
            aria-label="Routine file"
            onChange={(e) => {
              const file = e.target.files?.[0]
              if (file) void readFile(file)
            }}
          />
          <p className="text-xs text-muted-foreground-soft">JSON · up to 1 MB</p>
        </div>

        <CreateSurfaceField
          label="Or paste it"
          htmlFor={ids.text}
          hint="An ID already in use here is refused — nothing already saved is overwritten."
        >
          <textarea
            id={ids.text}
            value={text}
            onChange={(e) => {
              setText(e.target.value)
              setClash(false)
            }}
            placeholder='{"format":"crewship-pipeline-bundle/v1","pipeline":{"slug":"…","definition":{…}}}'
            className="h-40 w-full resize-none rounded-md border border-hairline bg-background p-2 font-mono text-xs text-foreground outline-none transition-shadow focus:border-primary focus:ring-2 focus:ring-primary/20"
          />
        </CreateSurfaceField>

        <CreateSurfaceField label="Crew" required hint="Every step runs as an agent of this crew, with its credentials.">
          {crews.length === 0 ? (
            <p className="text-xs text-muted-foreground">No crew yet. Create a crew first.</p>
          ) : (
            <CreateSurfaceChoice ariaLabel="Crew" value={crewId ?? ""} onChange={(v) => setCrewId(v || null)} options={crews.map((c) => ({ value: c.id, label: c.name }))} />
          )}
        </CreateSurfaceField>

        {routine && (
          <CreateSurfaceField label="ID" htmlFor={ids.slug} hint={slugOk ? "Lowercase letters, digits, - and _. Fixed after the first save." : "Start with a letter or digit; use lowercase letters, digits, - and _."}>
            <Input id={ids.slug} value={slug} aria-invalid={!slugOk || undefined} onChange={(e) => setSlug(e.target.value.trim())} className={cn(CREATE_SURFACE_INPUT, "font-mono")} />
          </CreateSurfaceField>
        )}

        {parsed && (
          <ul className="flex flex-col gap-1.5 text-xs" aria-label="Checks" data-testid="import-checks">
            {parsed.ok ? (
              <Check ok text={`Valid routine · ${routine!.name || routine!.slug} · ${(routine!.definition.steps as unknown[]).length} steps`} />
            ) : (
              <Check ok={false} text={parsed.error} />
            )}
            {routine && crewId && named.length > 0 && (
              missing.length === 0 ? (
                <Check ok text={`Agents ${named.join(", ")} are in ${crewName ?? "this crew"}`} />
              ) : (
                <Check ok={false} text={`${missing.join(", ")} ${missing.length === 1 ? "is" : "are"} not in ${crewName ?? "this crew"}: pick ${missing.length === 1 ? "an agent" : "agents"} in the step editor before publishing`} />
              )
            )}
            {routine && <Check ok text="Saved as a draft; it is tested before it can be published" />}
          </ul>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />

      <CreateSurfaceFooter
        onCancel={onClose}
        primaryLabel={busy ? "Importing…" : "Import as draft"}
        primaryIcon={Upload}
        onPrimary={() => void submit()}
        primaryDisabled={!ready}
        busy={busy}
      />
    </CreateSurface>
  )
}

function Check({ ok, text }: { ok: boolean; text: string }) {
  return (
    <li className="flex items-start gap-2">
      {ok ? <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0 text-success" aria-hidden /> : <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warn" aria-hidden />}
      <span>{text}</span>
    </li>
  )
}
