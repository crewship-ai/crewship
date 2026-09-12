"use client"

import type { RoutineDetail } from "./routines-detail-panel"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { RoutineRecipeSteps } from "./routine-recipe-steps"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { RoutinePublicationReview } from "./routine-publication-review"
import { RoutineInputFormBuilder } from "./routine-input-form-builder"
import { RoutineTriggerFields, type RoutineTriggerDraft } from "./routine-trigger-fields"
import { useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { Save, Sparkles, GitFork, Braces, Search } from "lucide-react"
import { FormField } from "@/components/features/chat/asks/form-field"
import {
  slashFieldsFromRoutineInputs,
  routineInputsFromValues,
  isMissingRequired,
  type RoutineInputSpec,
} from "@/lib/routine-inputs"
import { Input } from "@/components/ui/input"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceDescriptionInput,
  CreateSurfaceField,
  CreateSurfaceTitleInput,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceLoading,
  CreateSurfaceRefusal,
  CreateSurfaceSection,
  CreateSurfaceTile,
} from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"
import {
  loadRoutineDraft,
  saveRoutineDraft,
  type RoutineDraft,
} from "@/lib/routine-drafts"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
// The shared picker, not a second copy of it. The local one was a verbatim
// fork that had already drifted: it never got the `modal` prop that fixes
// scrolling inside a dialog, so its list clipped and would not move — the
// exact cost of the duplication.
import { CrewPicker } from "@/components/features/crews/crew-picker"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { FileEditor } from "@/components/features/files/file-editor"
import { parseRoutineBuffer } from "@/lib/routine-buffer"
import { routineDslExtensions } from "@/lib/routine-dsl-editor-extensions"
import { convertDsl, toYaml, type DslFormat } from "@/lib/routine-dsl-format"

/** Which reading of the definition the editor pane is showing. */

// All three creation paths share the same recipe definition and save contract.
// The server's test_run endpoint performs static validation and mints the save
// token; it does not execute work. Code stays mounted while sections change.

interface Props {
  savedDraftLink?: { slug: string; id?: string; workspaceId?: string }
  routine?: RoutineDetail
  initialDraft?: Record<string, unknown>
  workspaceId: string
  open: boolean
  onClose: () => void
  onCreated: (slug: string) => void
}

type Mode = "entry" | "describe" | "fork" | "advanced"

export const STARTER_TEMPLATES = [
  {
    id: "empty",
    label: "Empty",
    description: "Start from scratch — slug + one agent_run step.",
    json: {
      dsl_version: "1.0",
      name: "my-routine",
      description: "Describe what this routine does.",
      inputs: [],
      outputs: [],
      steps: [
        {
          id: "step1",
          type: "agent_run",
          agent_slug: "your-agent-slug",
          complexity: "fast",
          prompt: "Replace with the prompt your agent should run.",
        },
      ],
    },
  },
  {
    id: "summarize",
    label: "Summarize text",
    description: "One-step agent_run that takes 'text' input and returns a summary.",
    json: {
      dsl_version: "1.0",
      name: "summarize-text",
      description: "Summarize input text in 3 bullet points.",
      inputs: [
        {
          name: "text",
          type: "string",
          required: true,
          description: "Text to summarize",
        },
      ],
      outputs: [{ name: "summary", type: "string" }],
      steps: [
        {
          id: "summarize",
          type: "agent_run",
          agent_slug: "your-agent-slug",
          complexity: "fast",
          prompt:
            "Summarize the following text in 3 concise bullet points:\n\n{{ inputs.text }}",
          validation: {
            min_length: 10,
            must_not_contain: ["API_KEY=", "Bearer "],
          },
        },
      ],
    },
  },
  {
    id: "two-step",
    label: "Two-step pipeline",
    description: "Fetch → summarize chain. Demonstrates step output templating.",
    json: {
      dsl_version: "1.0",
      name: "fetch-and-summarize",
      description: "Fetch content from a URL, then summarize it.",
      inputs: [{ name: "url", type: "string", required: true }],
      outputs: [{ name: "summary", type: "string" }],
      steps: [
        {
          id: "fetch",
          type: "http",
          http: {
            method: "GET",
            url: "{{ inputs.url }}",
            max_response_bytes: 200000,
          },
        },
        {
          id: "summarize",
          type: "agent_run",
          agent_slug: "your-agent-slug",
          complexity: "fast",
          prompt:
            "Summarize the following content in 3 bullets:\n\n{{ steps.fetch.output }}",
          needs: ["fetch"],
        },
      ],
    },
  },
]

interface Crew {
  id: string
  name: string
  // The identity the crews API already returns and the roster already draws.
  // A crew's colour is a hex on most rows and a palette id on the ones the
  // wizard wrote — CrewIcon knows about both, which is why neither is
  // normalised here.
  icon?: string | null
  color?: string | null
  // The default avatar style for agents in the crew that have none of their
  // own. Same fallback chain the roster uses, so a Lead does not get one face
  // here and another two clicks away.
  avatar_style?: string | null
}

interface AgentRec {
  id: string
  name: string
  slug: string
  agent_role: string
  crew_id: string | null
  role_title?: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
}

interface RoutineListItem {
  id: string
  slug: string
  name: string
  description?: string
  invocation_count: number
  ephemeral?: boolean
  // Same identity the sidebar and the detail header derive, so a
  // routine looks like itself wherever it is listed. You are choosing
  // something to copy; a column of slugs is not a thing you can
  // recognise.
  icon?: string
  color?: string
  last_invocation_status?: string
}

export function RoutineCreateDialog({
  workspaceId,
  open,
  onClose,
  onCreated,
  routine,
  initialDraft,
  savedDraftLink,
}: Props) {
  const router = useRouter()
  const [mode, setMode] = useState<Mode>("entry")

  // ── Shared meta ────────────────────────────────────────────────────
  const [icon, setIcon] = useState(routine ? resolveRoutineIcon(routine) : "workflow")
  const [color, setColor] = useState(routine ? resolveRoutineColor(routine) : "violet")
  const savedRecipe = useRef<string | null>(null)
  const publicationBusy = useRef(false)
  const draftRef = useRef<RoutineDraft | null>(null)
  const draftSelectionController = useRef<AbortController | null>(null)
  useEffect(() => () => draftSelectionController.current?.abort(), [open, workspaceId])
  const [draftRevision, setDraftRevision] = useState(0)
  const [draftLoading, setDraftLoading] = useState(false)
  const [draftLoadFailed, setDraftLoadFailed] = useState(false)
  const [approveRisk, setApproveRisk] = useState(false)
  const [savedDrafts, setSavedDrafts] = useState<{ slug: string; revision: number }[]>([])

  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [authorCrewId, setAuthorCrewId] = useState("")
  const [crews, setCrews] = useState<Crew[]>([])
  const [agents, setAgents] = useState<AgentRec[]>([])

  // ── Describe mode ──────────────────────────────────────────────────
  const [goal, setGoal] = useState("")

  // ── Fork mode ──────────────────────────────────────────────────────
  const [routines, setRoutines] = useState<RoutineListItem[]>([])
  const [routinesLoading, setRoutinesLoading] = useState(false)
  const [forkSearch, setForkSearch] = useState("")
  const [forking, setForking] = useState(false)

  // ── Advanced (JSON DSL) mode ───────────────────────────────────────
  // The buffer, in whichever format the author is writing. YAML by
  // default because that is what the routine editor, the manifest kind
  // and `crewship apply -f` all speak — creating in JSON and then
  // editing in YAML made one job two surfaces.
  //
  // `dslText` is what the editor is CONSTRUCTED from and changes only
  // when the buffer is replaced wholesale (template, format switch,
  // fork). `liveText` mirrors the live document for everything derived
  // from it. Feeding typing back into construction rebuilds CodeMirror
  // and puts the caret at position 0 — it shredded a routine in about
  // four seconds when the detail editor did it.
  const [dslFormat, setDslFormat] = useState<DslFormat>("yaml")
  // Code, not graph: this surface exists to type a DSL, and the graph is a
  // reading of what was typed.
  const [dslText, setDslText] = useState(() => toYaml(STARTER_TEMPLATES[0].json))
  const [liveText, setLiveText] = useState(() => toYaml(STARTER_TEMPLATES[0].json))
  const bufferRef = useRef(toYaml(STARTER_TEMPLATES[0].json))
  const [editorKey, setEditorKey] = useState(0)
  const [parseError, setParseError] = useState<string | null>(null)
  const [busy, setBusy] = useState<"none" | "testing" | "saving">("none")
  const [trigger, setTrigger] = useState<RoutineTriggerDraft>({
    kind: "manual",
    cron: "0 9 * * 1-5",
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    at: "",
  })
  const [sourcesAndOutput, setSourcesAndOutput] = useState("")
  const [scheduledValues, setScheduledValues] = useState<Record<string, string>>({})
  const [testResult, setTestResult] = useState<{
    passed: boolean
    details: string
  } | null>(null)
  const [section, setSection] = useState("Recipe")
  const [reviewOpen, setReviewOpen] = useState(false)
  const [reviewBaseline, setReviewBaseline] = useState<RoutineDetail | null>(null)
  const [reviewError, setReviewError] = useState<string | null>(null)
  const reviewingExisting = !!draftRef.current?.base_pipeline_id || !!routine
  const publishedRecipe = routine ?? reviewBaseline
  const reviewSlug = routine?.slug || draftRef.current?.slug || ""
  useEffect(() => {
    if (!reviewOpen || !reviewingExisting || routine) return
    const controller = new AbortController()
    setReviewBaseline(null)
    setReviewError(null)
    void (async () => {
      try {
        const res = await apiFetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(reviewSlug)}`,
          { signal: controller.signal },
        )
        if (!res.ok)
          throw new Error(
            "The published recipe could not be loaded for comparison. Return to editing and try again.",
          )
        const baseline = await res.json()
        if (!baseline.definition || typeof baseline.definition !== "object")
          throw new Error("The published recipe is unavailable for comparison.")
        if (!controller.signal.aborted) setReviewBaseline(baseline)
      } catch (error) {
        if (!controller.signal.aborted)
          setReviewError(error instanceof Error ? error.message : String(error))
      }
    })()
    return () => controller.abort()
  }, [reviewOpen, reviewingExisting, routine, workspaceId, reviewSlug])

  const [forkSource, setForkSource] = useState<string | null>(null)
  // saveToken captured from the most recent successful /test_run.
  // Used by the subsequent /save call so the server can verify via
  // HMAC instead of trusting body's last_test_run_at — closes the
  // test-gate body-trust loophole. Cleared on edit + on save success.
  const [saveToken, setSaveToken] = useState<string | null>(null)
  // What the server said when it refused the save. It was a toast only, which
  // is the one moment of this surface's life you cannot afford to miss and the
  // only one that used to fade on its own. The toast stays; this is the band
  // between the body and the footer that does not scroll away.
  const [saveError, setSaveError] = useState<string | null>(null)
  const [scheduleConflict, setScheduleConflict] = useState<{
    schedule_id: string
    name: string
    reason: string
  } | null>(null)

  // The buffer as it was handed to the editor. Anything else means there is
  // input to lose, which is what the shell's discard guard asks about.
  //
  // It has to MOVE with the buffer, not just be captured at mount: picking a
  // starter template and switching YAML↔JSON both replace the document
  // wholesale, and against a baseline frozen at mount either one left the
  // editor reporting input that nobody had typed. `replaceBuffer` re-baselines
  // it — except for a fork, which is the one wholesale replacement that IS
  // work (see there).
  const pristineText = useRef(liveText)

  // Reset to the entry screen each time the dialog opens. (Field state is
  // otherwise preserved across a close/reopen within the same session.)
  useEffect(() => {
    if (!open) return
    const previouslySaved = savedRecipe.current !== null
    savedRecipe.current = null
    if (routine) {
      const text = toYaml(initialDraft ?? routine.definition)
      setMode("advanced")
      setSection("Recipe")
      setDslFormat("yaml")
      setName(routine.name)
      setDescription(routine.description ?? "")
      setAuthorCrewId(routine.author_crew_id ?? "")
      setIcon(resolveRoutineIcon(routine))
      setColor(resolveRoutineColor(routine))
      setDslText(text)
      setLiveText(text)
      bufferRef.current = text
      pristineText.current = toYaml(routine.definition)
      setEditorKey((k) => k + 1)
      setTestResult(null)
      setSaveToken(null)
      setSaveError(null)
    } else {
      setMode("entry")
      if (previouslySaved) {
        const text = toYaml(STARTER_TEMPLATES[0].json)
        setName("")
        setDescription("")
        setAuthorCrewId("")
        setIcon("workflow")
        setColor("violet")
        setDslFormat("yaml")
        setDslText(text)
        setLiveText(text)
        bufferRef.current = text
        pristineText.current = text
        setEditorKey((k) => k + 1)
        setSection("Recipe")
        setForkSource(null)
        setTrigger((t) => ({ ...t, kind: "manual", at: "" }))
        setScheduledValues({})
        setGoal("")
        setSourcesAndOutput("")
        setTestResult(null)
        setSaveToken(null)
        setSaveError(null)
      }
    }
    // Seed once when opening; background refresh must not replace an unsaved draft.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const applyStoredDraft = (draft: RoutineDraft) => {
    draftRef.current = draft
    setDraftRevision(draft.revision)
    const doc = draft.document
    if (!doc.definition || typeof doc.definition !== "object") return
    const text = toYaml(doc.definition)
    setDslFormat("yaml")
    setDslText(text)
    setLiveText(text)
    bufferRef.current = text
    pristineText.current = text
    setEditorKey((k) => k + 1)
    setName(String(doc.name || draft.slug))
    setDescription(String(doc.description || ""))
    setAuthorCrewId(String(doc.author_crew_id || ""))
    if (typeof doc.icon === "string") setIcon(doc.icon)
    if (typeof doc.color === "string") setColor(doc.color)
    const start = doc.trigger as Record<string, unknown> | undefined
    if (start) {
      setTrigger((current) => ({
        ...current,
        kind: start.kind as typeof current.kind,
        cron: String(start.cron || current.cron),
        timezone: String(start.timezone || current.timezone),
        at: String(start.fire_at || ""),
      }))
      if (start.inputs && typeof start.inputs === "object")
        setScheduledValues(
          Object.fromEntries(
            Object.entries(start.inputs).map(([key, value]) => [
              key,
              typeof value === "string" ? value : JSON.stringify(value),
            ]),
          ),
        )
    }
    setMode("advanced")
    setSection("Recipe")
  }

  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    draftRef.current = null
    setDraftRevision(0)
    setApproveRisk(false)
    setDraftLoading(false)
    setSavedDrafts([])
    setDraftLoadFailed(false)
    if (routine || savedDraftLink) {
      setDraftLoading(true)
      if (savedDraftLink) setMode("advanced")
      const load = async () => {
        if (savedDraftLink?.workspaceId && savedDraftLink.workspaceId !== workspaceId)
          throw new Error(
            "This draft belongs to another workspace. Switch to its workspace to open this link.",
          )
        const draft = await loadRoutineDraft(
          workspaceId,
          savedDraftLink?.slug || routine!.slug,
          controller.signal,
        )
        if (
          savedDraftLink &&
          (!draft.id || (savedDraftLink.id && draft.id !== savedDraftLink.id))
        )
          throw new Error(
            "This draft link is no longer current. Close it and open the saved draft from New routine.",
          )
        return draft
      }
      void load()
        .then((draft) => {
          if (controller.signal.aborted) return
          draftRef.current = draft
          setDraftRevision(draft.revision)
          if (!initialDraft) applyStoredDraft(draft)
        })
        .catch((error) => {
          if (!controller.signal.aborted) {
            setDraftLoadFailed(true)
            setSaveError(String(error instanceof Error ? error.message : error))
          }
        })
        .finally(() => {
          if (!controller.signal.aborted) setDraftLoading(false)
        })
    } else {
      void apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/drafts`,
        {
          signal: controller.signal,
        },
      )
        .then(async (res) => {
          const data = res.ok ? await res.json() : []
          if (!controller.signal.aborted) setSavedDrafts(Array.isArray(data) ? data : [])
        })
        .catch(() => {})
    }
    return () => controller.abort()
    // Opening creates one editor baseline; refreshes must not replace local work.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    open,
    workspaceId,
    savedDraftLink?.slug,
    savedDraftLink?.id,
    savedDraftLink?.workspaceId,
  ])

  // Lazy-load crews + agents on first open. Side effects live in useEffect
  // (not the render body) so React's render pipeline isn't disturbed.
  useEffect(() => {
    if (!open) return
    let cancelled = false
    if (crews.length === 0) {
      apiFetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        .then((r) => (r.ok ? r.json() : []))
        .then((data: Crew[]) => {
          if (!cancelled) setCrews(Array.isArray(data) ? data : [])
        })
        .catch(() => {})
    }
    if (agents.length === 0) {
      apiFetch(`/api/v1/agents?workspace_id=${workspaceId}`)
        .then((r) => (r.ok ? r.json() : []))
        .then((data: AgentRec[]) => {
          if (!cancelled) setAgents(Array.isArray(data) ? data : [])
        })
        .catch(() => {})
    }
    return () => {
      cancelled = true
    }
  }, [open, workspaceId, crews.length, agents.length])

  // Load the workspace's own routines when entering Fork mode.
  useEffect(() => {
    if (!open || mode !== "fork" || routines.length > 0) return
    let cancelled = false
    setRoutinesLoading(true)
    apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: RoutineListItem[]) => {
        if (!cancelled) setRoutines(Array.isArray(data) ? data : [])
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setRoutinesLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, mode, workspaceId, routines.length])

  // The chosen crew's own row — its icon and colour draw the picker's trigger,
  // and its avatar_style is the fallback for a Lead that has none.
  const describeCrew = useMemo<Crew | null>(
    () => crews.find((c) => c.id === authorCrewId) ?? null,
    [crews, authorCrewId],
  )

  // The Lead agent for the chosen describe crew (LEAD role, same crew).
  // Falls back to any agent in the crew so a crew without an explicit Lead
  // can still be used as the authoring host.
  const describeLead = useMemo<AgentRec | null>(() => {
    if (!authorCrewId) return null
    const inCrew = agents.filter((a) => a.crew_id === authorCrewId)
    return inCrew.find((a) => a.agent_role === "LEAD") ?? inCrew[0] ?? null
  }, [agents, authorCrewId])

  // Parse the DSL JSON for slug-preview without touching state in render.
  // This useMemo must execute on EVERY render regardless of `open`/`mode`
  // so React's hooks contract holds — the `if (!open) return null` below
  // sits AFTER all hook declarations for that reason.
  const parsedDSL = useMemo<Record<string, unknown> | null>(() => {
    const r = parseRoutineBuffer(liveText, dslFormat)
    return r.ok ? r.parsed : null
  }, [liveText, dslFormat])

  // Schema completion + inline diagnostics — the same extensions the
  // routine editor uses, so a document written here and a document
  // edited there are held to one standard.
  const dslExtensions = useMemo(() => routineDslExtensions(dslFormat), [dslFormat])

  useEffect(() => {
    if (!open) return
    const changed =
      liveText !== pristineText.current ||
      name !== (routine?.name ?? "") ||
      description !== (routine?.description ?? "") ||
      authorCrewId !== (routine?.author_crew_id ?? "") ||
      icon !== (routine ? resolveRoutineIcon(routine) : "workflow") ||
      color !== (routine ? resolveRoutineColor(routine) : "violet") ||
      goal.trim() !== "" ||
      trigger.kind !== "manual" ||
      sourcesAndOutput.trim() !== "" ||
      Object.keys(scheduledValues).length > 0
    if (!changed) return
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ""
    }
    const onNavigate = (event: MouseEvent) => {
      const link = (event.target as Element)?.closest?.(
        "a[href]",
      ) as HTMLAnchorElement | null
      // Only guard a click that actually leaves this view. An in-page anchor
      // (href="#", a fragment, a download, a new tab) does not discard the
      // draft, and prompting on it — then swallowing the event in the capture
      // phase, before React sees it — breaks every anchor-shaped control on
      // the page for as long as the editor stays dirty.
      const href = link?.getAttribute("href") ?? ""
      const navigates =
        !!link &&
        !link.hasAttribute("download") &&
        link.target !== "_blank" &&
        href !== "" &&
        !href.startsWith("#")
      if (
        navigates &&
        !event.ctrlKey &&
        !event.metaKey &&
        !window.confirm("Discard unsaved recipe changes?")
      ) {
        event.preventDefault()
        event.stopPropagation()
      }
    }
    window.addEventListener("beforeunload", beforeUnload)
    document.addEventListener("click", onNavigate, true)
    return () => {
      window.removeEventListener("beforeunload", beforeUnload)
      document.removeEventListener("click", onNavigate, true)
    }
  }, [
    open,
    liveText,
    name,
    description,
    authorCrewId,
    icon,
    color,
    routine,
    goal,
    trigger.kind,
    sourcesAndOutput,
    scheduledValues,
  ])

  if (!open) return null

  const scheduledFields = slashFieldsFromRoutineInputs(
    Array.isArray(parsedDSL?.inputs) ? (parsedDSL.inputs as RoutineInputSpec[]) : [],
  )
  const slug = (parsedDSL?.["name"] as string) || "my-routine"

  const applyTemplate = (templateId: string) => {
    const tpl = STARTER_TEMPLATES.find((t) => t.id === templateId)
    if (!tpl) return
    const j = {
      ...tpl.json,
      name: name || tpl.json.name,
      description: description || tpl.json.description,
    }
    replaceBuffer(dslFormat === "yaml" ? toYaml(j) : JSON.stringify(j, null, 2))
    setParseError(null)
    setTestResult(null)
    setSaveToken(null) // template change → DSL change → bound token invalid
  }

  // Helper for handlers — re-parses with explicit error capture for
  // the inline UI feedback. Distinct from parsedDSL so the render
  // path stays side-effect-free.
  const parseDSLWithError = (): Record<string, unknown> | null => {
    const r = parseRoutineBuffer(bufferRef.current, dslFormat)
    if (!r.ok) {
      setParseError(r.message)
      return null
    }
    setParseError(null)
    return r.parsed
  }

  /**
   * Replace the buffer wholesale — template, format switch, fork.
   *
   * `baseline` says whether the new text counts as untouched. It does for a
   * starter template (the editor already opens on one; picking another is
   * choosing a starting point, not authoring) and for a format switch (same
   * document, other notation — that path re-baselines the OLD baseline through
   * the same converter and so passes `false` here). It does not for a fork,
   * which loads somebody's real routine on purpose and is exactly the kind of
   * work the guard exists to protect.
   */
  const replaceBuffer = (
    next: string,
    { baseline = true }: { baseline?: boolean } = {},
  ) => {
    setDslText(next)
    setLiveText(next)
    bufferRef.current = next
    if (baseline) pristineText.current = next
    setEditorKey((k) => k + 1)
    setTestResult(null)
    setSaveToken(null)
  }

  /** Every keystroke. Mirrors, never reconstructs. */
  const handleDocChange = (next: string) => {
    bufferRef.current = next
    setLiveText(next)
    const parsed = parseRoutineBuffer(next, dslFormat)
    // Mirror the description only when the document actually carries one.
    // In edit mode it is seeded from the routine and need not appear in the
    // DSL at all, so `?? ""` made the first keystroke in the Code pane blank
    // it — and the save path's `description || parsed.description || ""`
    // then wrote that empty string back over the saved routine.
    if (parsed.ok && parsed.parsed.description !== undefined)
      setDescription(String(parsed.parsed.description))
    setParseError(null)
    setTestResult(null)
    setSaveToken(null)
  }

  const switchDslFormat = (next: DslFormat) => {
    if (next === dslFormat) return
    const converted = convertDsl(bufferRef.current, dslFormat, next)
    if (!converted.ok) {
      toast.error(`Fix the ${dslFormat.toUpperCase()} error before switching`)
      return
    }
    // Carry the baseline across the switch instead of re-taking it from the
    // converted buffer. Re-taking it would forget every edit made before the
    // switch (the converted text IS the edited document, so it would match its
    // own new baseline); leaving the old one alone would call an untouched
    // buffer dirty, because a YAML baseline never equals a JSON buffer.
    // Converting the baseline the same way keeps both answers honest, and a
    // baseline that will not convert falls back to "dirty", which asks.
    const convertedBaseline = convertDsl(pristineText.current, dslFormat, next)
    setDslFormat(next)
    replaceBuffer(converted.text, { baseline: false })
    if (convertedBaseline.ok) pristineText.current = convertedBaseline.text
  }

  // Returns both the pass verdict AND the freshly-minted save_token so a
  // caller chaining straight into save (handleTestAndSave) can pass the token
  // explicitly — React state (setSaveToken) is async and wouldn't be visible
  // to a save invoked in the same tick.
  const handleTestRun = async (): Promise<{
    passed: boolean
    token: string | null
  }> => {
    const parsed = parseDSLWithError()
    if (!parsed) {
      toast.error("Fix the recipe before continuing")
      return { passed: false, token: null }
    }
    setBusy("testing")
    setTestResult(null)
    setSaveToken(null)
    try {
      const testBody: Record<string, unknown> = {
        definition: parsed,
        sample_inputs: {},
      }
      if (authorCrewId) testBody.author_crew_id = authorCrewId
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/test_run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(testBody),
      })
      const data = (await res.json().catch(() => ({}))) as {
        passed?: boolean
        status?: string
        error?: string
        output?: string
        save_token?: string
      }
      if (!res.ok) {
        const msg = data.error ?? `HTTP ${res.status}`
        setTestResult({ passed: false, details: msg })
        toast.error("Test failed", { description: msg })
        return { passed: false, token: null }
      }
      // DRY_RUN_OK is the dry-run validation's pass status; COMPLETED is
      // tolerated for forward-compat; passed!=false is the legacy fallback
      // for older servers that surface no status field.
      const passed =
        data.status === "DRY_RUN_OK" ||
        data.status === "COMPLETED" ||
        (data.status === undefined && data.passed !== false)
      setTestResult({
        passed,
        details: passed
          ? "Recipe checks passed."
          : (data.error ?? "The recipe did not pass its checks."),
      })
      const token = passed && data.save_token ? data.save_token : null
      if (token) {
        setSaveToken(token)
      }
      if (passed) {
        toast.success("Test passed")
      } else {
        toast.error("Test failed", {
          description: data.error ?? "see details below",
        })
      }
      return { passed, token }
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      setTestResult({ passed: false, details: msg })
      toast.error("Test unavailable", { description: msg })
      return { passed: false, token: null }
    } finally {
      setBusy("none")
    }
  }

  const discardSavedDraft = async () => {
    const draft = draftRef.current
    if (
      !draft?.id ||
      !window.confirm(
        "Discard the saved draft? Your local edits stay in this editor. Review them against the current published recipe before saving again.",
      )
    )
      return
    setBusy("saving")
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(draft.slug)}/draft`,
        {
          method: "DELETE",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ id: draft.id, revision: draft.revision }),
        },
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        throw new Error(body?.error || "Could not discard the draft")
      }
      draftRef.current = await loadRoutineDraft(workspaceId, draft.slug)
      setDraftRevision(0)
      setApproveRisk(false)
      setSaveToken(null)
      setSaveError(null)
      toast.success("Saved draft discarded. Local edits are ready for review.")
    } catch (error) {
      setSaveError(error instanceof Error ? error.message : String(error))
    } finally {
      setBusy("none")
    }
  }

  const handleSave = async (tokenOverride?: string | null, draftOnly = false) => {
    if (draftLoading || draftLoadFailed || (draftOnly && publicationBusy.current)) return
    const parsed = parseDSLWithError()
    if (!parsed) {
      toast.error("Fix the recipe before continuing")
      return
    }
    if (
      (routine || draftRef.current?.base_pipeline_id) &&
      parsed.name !== (routine?.slug || draftRef.current?.slug)
    ) {
      setSaveError(
        "The routine identifier cannot change while editing. Fork the routine to create a separate recipe.",
      )
      return
    }
    if (!parsed["name"]) {
      toast.error("The recipe needs a name in Code to identify it")
      return
    }
    setBusy("saving")
    setSaveError(null)
    setScheduleConflict(null)
    try {
      const body: Record<string, unknown> = {
        slug: parsed["name"],
        name: name || (parsed["name"] as string),
        description: description || (parsed["description"] as string | undefined) || "",
        definition: parsed,
        skip_test_gate: false,
      }
      if (!routine && !draftRef.current?.base_pipeline_id) {
        const values = Object.fromEntries(
          scheduledFields.map((f) => [
            f.name,
            scheduledValues[f.name] ?? f.default ?? "",
          ]),
        )
        if (!draftOnly && (trigger.kind === "schedule" || trigger.kind === "once")) {
          const missing = scheduledFields.find((f) =>
            isMissingRequired(f, values[f.name]),
          )
          if (missing)
            throw new Error(`Fill in ${missing.name} before activating this start.`)
        }
        const scheduledInputs = routineInputsFromValues(scheduledFields, values)
        const previousTrigger = draftRef.current?.document.trigger as
          | Record<string, unknown>
          | undefined
        if (trigger.kind === "schedule")
          body.trigger = {
            ...previousTrigger,
            kind: "schedule",
            cron: trigger.cron,
            timezone: trigger.timezone,
            inputs: scheduledInputs,
          }
        if (trigger.kind === "manual") body.trigger = { kind: "manual" }
        if (trigger.kind === "once") {
          if (!draftOnly && (!trigger.at || !(Date.parse(trigger.at) > Date.now())))
            throw new Error("Choose a future date and time")
          body.trigger = {
            ...previousTrigger,
            kind: "once",
            fire_at: draftOnly ? trigger.at : new Date(trigger.at).toISOString(),
            inputs: scheduledInputs,
          }
        }
      }
      if (routine)
        body.author_agent_id =
          routine.author_crew_id === authorCrewId ? routine.author_agent_id : ""
      // The server clears the save test-gate ONLY via the HMAC save_token
      // (minted by /test_run) or the OWNER/ADMIN skip — it no longer trusts a
      // body "it passed" claim. Prefer an explicitly-threaded token (the
      // test-then-save chain) over the async state copy.
      const effectiveToken = tokenOverride ?? saveToken
      if (effectiveToken) {
        body.save_token = effectiveToken
      }
      // skip_test_gate is already on the body; nothing else to add.
      if (authorCrewId) body.author_crew_id = authorCrewId

      if (!savedRecipe.current) {
        let draft = draftRef.current
        if (!draft || (!draft.id && draft.slug !== String(body.slug))) {
          const baseline = await loadRoutineDraft(workspaceId, String(body.slug))
          if (baseline.id)
            throw new Error(
              "A saved draft already exists. Open it from New routine before replacing its content.",
            )
          if (!routine && baseline.base_pipeline_id)
            throw new Error(
              "This routine already exists. Choose a new identifier or open Edit.",
            )
          draft = baseline
        }
        body.icon = icon
        body.color = color
        if (!routine)
          body.author_agent_id =
            draft.document.author_crew_id === authorCrewId
              ? draft.document.author_agent_id || ""
              : ""
        const stored = await saveRoutineDraft(
          workspaceId,
          { ...draft, slug: String(body.slug) },
          body,
        )
        draftRef.current = stored
        setDraftRevision(stored.revision)
        if (draftOnly) {
          pristineText.current = bufferRef.current
          toast.success(`Draft saved · revision ${stored.revision}`)
          return
        }
        const res = await apiFetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(stored.slug)}/publish`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              id: stored.id,
              revision: stored.revision,
              save_token: effectiveToken,
              approve_risk: approveRisk,
            }),
          },
        )
        const saved = await res.json().catch(() => ({}))
        if (!res.ok && saved.schedule_conflict?.schedule_id)
          setScheduleConflict(saved.schedule_conflict)
        if (!res.ok)
          throw new Error(saved.error || "Publication failed. The draft is still saved.")
        if (!saved.slug)
          throw new Error(
            "Publication could not be confirmed. Reload the recipe before retrying.",
          )
        savedRecipe.current = saved.slug
      }
      const appearance = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/${encodeURIComponent(savedRecipe.current!)}/appearance`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ icon, color }),
        },
      )
      if (!appearance.ok)
        throw new Error(
          "The recipe was saved, but its icon could not be saved. Retry to finish saving its appearance.",
        )
      toast.success(`Routine "${savedRecipe.current}" published`)
      onCreated(savedRecipe.current!)
      onClose()
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      setSaveError(msg)
      toast.error("Save failed", { description: msg })
    } finally {
      setBusy("none")
    }
  }

  const handleTestAndSave = async () => {
    if (publicationBusy.current || draftLoading || draftLoadFailed) return
    publicationBusy.current = true
    try {
      const { passed, token } = await handleTestRun()
      if (passed) await handleSave(token)
    } finally {
      publicationBusy.current = false
    }
  }

  // Describe handoff: navigate into the Lead's chat with the authoring
  // prompt pre-sent. The chat page reads ?prompt= , opens a fresh session
  // and auto-sends once connected; the Routine-Author skill takes over.
  const handleDescribe = () => {
    const text = goal.trim()
    if (!describeLead || !text) return
    const prompt = `Author a routine draft for me using get_routine_draft and save_routine_draft. Return the editor_url so I can review and publish it; do not publish with save_routine. Goal: ${text}
Sources and expected outputs: ${sourcesAndOutput || "Clarify these with me."}
Requested start: ${JSON.stringify(trigger.kind === "once" && trigger.at ? { ...trigger, fire_at: new Date(trigger.at).toISOString() } : trigger)}
Use scripts for deterministic work and agents where judgment is needed. Show a readable recipe and expected outputs before saving. Distinguish static validation from a real trial. Use outcomes.required for checks that must block unverified results.`
    router.push(
      `/chat/${encodeURIComponent(describeLead.slug)}?prompt=${encodeURIComponent(prompt)}`,
    )
    onClose()
  }

  // Fork: load an existing routine's DSL into the advanced editor.
  const handleForkPick = async (item: RoutineListItem) => {
    setForking(true)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/${item.slug}`,
      )
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const detail = (await res.json()) as {
        definition?: Record<string, unknown>
        name?: string
        description?: string
        author_crew_id?: string
        icon?: string
        color?: string
      }
      const def = detail.definition ?? {}
      // Rename the fork so it doesn't collide with the source slug on save.
      const forkName = `${item.slug}-copy`
      const nextDef = { ...def, name: forkName }
      // Not baselined: a fork is content you asked for, and losing it silently
      // is the same defect as losing a typed goal. The source description
      // usually keeps the surface dirty on its own; a routine without one
      // would have had nothing else to go on.
      replaceBuffer(
        dslFormat === "yaml" ? toYaml(nextDef) : JSON.stringify(nextDef, null, 2),
        {
          baseline: false,
        },
      )
      setAuthorCrewId(detail.author_crew_id ?? "")
      setIcon(resolveRoutineIcon({ ...detail, slug: item.slug }))
      setColor(resolveRoutineColor({ ...detail, slug: item.slug }))
      setForkSource(item.name || item.slug)
      setSection("Recipe")
      setName("")
      setDescription(item.description ?? detail.description ?? "")
      setParseError(null)
      setTestResult(null)
      setSaveToken(null)
      setMode("advanced")
    } catch (e) {
      toast.error("Could not load routine", {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setForking(false)
    }
  }

  const filteredRoutines = routines.filter((r) => {
    if (r.ephemeral) return false
    if (!forkSearch.trim()) return true
    const q = forkSearch.toLowerCase()
    return `${r.slug} ${r.name} ${r.description ?? ""}`.toLowerCase().includes(q)
  })

  const headerTitle =
    mode === "describe"
      ? "Describe your routine"
      : mode === "fork"
        ? "Start from an existing routine"
        : mode === "advanced"
          ? routine
            ? "Edit your routine"
            : "Build your routine"
          : "New routine"
  const headerSub =
    mode === "describe"
      ? "a Lead drafts it with you in chat"
      : mode === "fork"
        ? "fork one of your own routines"
        : mode === "advanced"
          ? "Set up the recipe, save a draft, then publish when ready."
          : // The entry screen had no subtitle, so three tiles appeared with
            // nothing saying they are three routes to the same place. People
            // read a picker as "which kind am I making", and the answer is that
            // it does not matter — pick the one whose inputs you already have.
            "Three ways in. All three land on the same routine — pick the one you have inputs for."

  // The shell's discard guard — Esc and an overlay click ask before throwing
  // input away, and the header's × and the footer's Cancel ask too. The
  // starter template the editor opens with is not input, so an untouched
  // buffer is not dirty.
  //
  // It describes the DRAFT, not the screen. Keyed on `mode` it had an
  // entry/fork arm of literal `false`, and nothing clears these fields when
  // the mode changes — so the header's back arrow, which is navigation rather
  // than a discard, moved a typed goal onto a screen that reported nothing to
  // lose, and every exit from there closed silently. The state that survives
  // the arrow is the state the guard has to look at.
  //
  // The entry tiles and the fork list still cost nothing when there is nothing
  // typed, which is the case #2076 measured: land on the tiles, press Cancel,
  // and no prompt appears. `forkSearch` is deliberately absent — it filters a
  // list rather than composing anything.
  const dirty =
    Object.keys(scheduledValues).length > 0 ||
    sourcesAndOutput.trim() !== "" ||
    trigger.kind !== "manual" ||
    goal.trim() !== "" ||
    name !== (routine?.name ?? "") ||
    description !== (routine?.description ?? "") ||
    authorCrewId !== (routine?.author_crew_id ?? "") ||
    icon !== (routine ? resolveRoutineIcon(routine) : "workflow") ||
    color !== (routine ? resolveRoutineColor(routine) : "violet") ||
    liveText !== pristineText.current

  // ⌘↵ / Ctrl↵, wired once by the shell. It does whatever the mode's primary
  // does, and nothing on the two modes whose actions are their list rows.
  const handleKeyboardSubmit = () => {
    if (busy !== "none") return
    if (mode === "describe") {
      handleDescribe()
    } else if (mode === "advanced") {
      void handleTestAndSave()
    }
  }

  const advancedPrimaryLabel =
    busy === "testing" ? "Testing…" : busy === "saving" ? "Publishing…" : "Publish"

  return (
    <CreateSurface
      open={open}
      restoreOpenerFocus
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
      size="lg"
      dirty={dirty}
      discardLabel="this routine"
      onSubmit={handleKeyboardSubmit}
      className={
        mode === "advanced"
          ? "sm:h-[min(85vh,760px)] sm:max-h-[90vh] sm:max-w-[800px]"
          : undefined
      }
    >
      <CreateSurfaceHeader
        concept="routines"
        context="Routines"
        title={headerTitle}
        description={headerSub}
        onBack={routine || mode === "entry" ? undefined : () => setMode("entry")}
        onClose={onClose}
      />

      {/* ── ENTRY — three cards ───────────────────────────────────────── */}
      {mode === "entry" && savedDrafts.length > 0 && (
        <div className="border-b border-hairline p-4">
          <p className="mb-2 text-xs text-muted-foreground">Continue a saved draft</p>
          {savedDrafts.map((d) => (
            <button
              key={d.slug}
              type="button"
              className="mr-2 rounded-md border px-3 py-2 text-sm"
              onClick={() => {
                draftSelectionController.current?.abort()
                const controller = new AbortController()
                draftSelectionController.current = controller
                setDraftLoading(true)
                void loadRoutineDraft(workspaceId, d.slug, controller.signal)
                  .then((draft) => {
                    if (!controller.signal.aborted) applyStoredDraft(draft)
                  })
                  .catch((e) => {
                    if (!controller.signal.aborted) setSaveError(String(e.message))
                  })
                  .finally(() => {
                    if (!controller.signal.aborted) setDraftLoading(false)
                  })
              }}
            >
              {d.slug} · r{d.revision}
            </button>
          ))}
        </div>
      )}
      {mode === "entry" && (
        <>
          <CreateSurfaceBody className="flex flex-col gap-2.5">
            {/* Three routes, three colours.
             *  Two of these were accent="slate", which made the picker read as
             *  one recommended option and two afterthoughts. The meta says in a
             *  word what each route trades — the sparkle glyph that used to
             *  mark the first one said "special" without saying why. */}
            <CreateSurfaceTile
              icon={Sparkles}
              accent="gold"
              title="Describe it"
              description="Tell a Lead agent your goal in plain words. It drafts the routine with you in chat, asks a couple of questions, and shows a readable preview before anything is saved."
              meta="fastest"
              className="border-primary/40 bg-primary/[0.06] hover:border-primary/60 hover:bg-primary/10"
              onClick={() => setMode("describe")}
            />
            <CreateSurfaceTile
              icon={GitFork}
              accent="purple"
              title="Fork an existing routine"
              description="Start from one of your workspace's own routines and tweak it. No curated catalog — the library grows from what you and your agents actually build."
              onClick={() => setMode("fork")}
            />
            <CreateSurfaceTile
              icon={Braces}
              accent="teal"
              title="Write it yourself"
              description="Build a recipe with a clear overview of inputs, steps and outputs. Edit YAML or JSON, test with sample data, then publish."
              meta="full control"
              onClick={() => setMode("advanced")}
            />
          </CreateSurfaceBody>

          {/* No primary: the three tiles ARE the action, which is the case the
            shell made `primaryLabel`/`onPrimary` optional for. What is not
            optional is the Cancel — this screen used to render a body and
            nothing else, so the one surface-wide rule the shell states
            outright ("Cancel is always present, always leftmost, always in
            the same place") was false the moment New routine opened. The ×
            worked, so this was never a dead end; it was the button being
            missing from the one place a person has learned to look.

            Guarded by default, and deliberately not opted out of: unlike a
            picker panel's "Cancel", this one closes the whole dialog, so it
            is the header ×'s twin and has to behave like it. Nothing on this
            screen is input, so on a fresh dialog the guard costs nothing —
            and now that `dirty` reads the draft rather than the mode, Cancel
            gets the prompt for free when the arrow has carried a goal or a
            buffer back here, instead of being one of four exits that all
            skipped it.

            The hint names Esc alone. There is no primary, so ⌘↵ has nothing
            to confirm — `handleKeyboardSubmit` returns without doing anything
            in this mode — and a footer that prints a keystroke which does
            nothing is worse than one that prints nothing. */}
          <CreateSurfaceFooter
            hint={
              <>
                <kbd className="font-mono">Esc</kbd> to cancel
              </>
            }
            onCancel={onClose}
          />
        </>
      )}

      {/* ── DESCRIBE ──────────────────────────────────────────────────── */}
      {mode === "describe" && (
        <>
          <CreateSurfaceBody className="flex flex-col gap-4">
            <div className="flex items-end gap-3">
              <CreateSurfaceField
                label="Owner (crew)"
                htmlFor="describe-crew"
                className="flex-1"
              >
                <CrewPicker
                  id="describe-crew"
                  ariaLabel="Select crew"
                  crews={crews}
                  value={authorCrewId}
                  onChange={setAuthorCrewId}
                  placeholder="Choose a crew…"
                />
              </CreateSurfaceField>
              <div className="pb-1.5 text-[11px] text-muted-foreground">
                {authorCrewId ? (
                  describeLead ? (
                    <span className="inline-flex items-center gap-1.5">
                      {/* The Lead's own face, from the same (seed, style)
                          fallback chain the roster uses — this was a purple
                          gradient disc that stood for nobody, next to the name
                          of somebody. */}
                      <AgentAvatar
                        seed={describeLead.avatar_seed || describeLead.name}
                        style={describeLead.avatar_style || describeCrew?.avatar_style}
                        agentId={describeLead.id}
                        avatarUrl={describeLead.avatar_url}
                        alt=""
                        className="h-4 w-4 shrink-0"
                      />
                      Lead: <b className="text-foreground">{describeLead.name}</b>
                    </span>
                  ) : (
                    <span className="text-warn">No Lead in this crew</span>
                  )
                ) : (
                  <span className="text-muted-foreground-soft">pick a crew →</span>
                )}
              </div>
            </div>

            <CreateSurfaceSection title="Goal" icon={Sparkles} accent="gold">
              <CreateSurfaceField
                label="What should the routine do?"
                htmlFor="describe-goal"
              >
                <CreateSurfaceDescriptionInput
                  id="describe-goal"
                  value={goal}
                  onChange={(e) => setGoal(e.target.value)}
                  rows={4}
                  placeholder="Describe it in your own words. e.g. Every weekday morning, fetch the top 5 Hacker News stories, summarize each in one sentence, and post the digest to Slack #standup."
                  className="rounded-md border border-hairline bg-background p-2.5 leading-relaxed"
                />
              </CreateSurfaceField>
            </CreateSurfaceSection>

            <CreateSurfaceField
              label="Sources and expected outputs (optional)"
              htmlFor="routine-sources-output"
            >
              <textarea
                id="routine-sources-output"
                value={sourcesAndOutput}
                onChange={(e) => setSourcesAndOutput(e.target.value)}
                className="min-h-20 w-full rounded-md border bg-background p-2 text-sm"
                placeholder="What should it work with, and what should you receive?"
              />
            </CreateSurfaceField>
            <RoutineTriggerFields
              workspaceId={workspaceId}
              value={trigger}
              onChange={setTrigger}
            />
            <p className="text-[11px] leading-relaxed text-muted-foreground">
              {describeLead?.name ?? "The Lead"} will draft it and ask a couple of
              questions, then show a readable preview — nothing is saved without you. It
              grounds the draft in your crew's connected integrations, your existing
              routines, and the routine schema.
            </p>

            <div className="flex gap-3 text-[11px] text-muted-foreground">
              <button
                type="button"
                className="hover:text-foreground"
                onClick={() => setMode("fork")}
              >
                fork a routine
              </button>
              <button
                type="button"
                className="hover:text-foreground"
                onClick={() => setMode("advanced")}
              >
                Build your routine
              </button>
            </div>
          </CreateSurfaceBody>

          <CreateSurfaceFooter
            onCancel={onClose}
            primaryLabel={`Draft with ${describeLead?.name ?? "a Lead"}`}
            primaryIcon={Sparkles}
            primaryDisabled={
              !describeLead ||
              !goal.trim() ||
              (trigger.kind === "once" && !(Date.parse(trigger.at) > Date.now()))
            }
            onPrimary={handleDescribe}
          />
        </>
      )}

      {/* ── FORK ──────────────────────────────────────────────────────── */}
      {mode === "fork" && (
        <>
          <CreateSurfaceBody className="flex flex-col gap-3">
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={forkSearch}
                onChange={(e) => setForkSearch(e.target.value)}
                placeholder="Search your routines…"
                className="h-8 pl-8 text-xs coarse:h-12 coarse:text-sm"
              />
            </div>
            {routinesLoading ? (
              <CreateSurfaceLoading rows={3} />
            ) : filteredRoutines.length === 0 ? (
              <div className="rounded-md border border-dashed border-border/60 px-3 py-6 text-center text-xs text-muted-foreground">
                {routines.length === 0
                  ? "No routines yet. Describe one, or write the first yourself."
                  : "No routines match your search."}
              </div>
            ) : (
              <div className="flex flex-col gap-1.5">
                {filteredRoutines.map((r) => (
                  <CreateSurfaceTile
                    key={r.id}
                    disabled={forking}
                    onClick={() => handleForkPick(r)}
                    // The routine's own icon and colour, with the last run's
                    // verdict on it. Same identity the sidebar and the detail
                    // header derive — you are choosing something to copy, and a
                    // column of slugs is not a thing you can recognise.
                    leading={
                      <span className="relative shrink-0">
                        <CrewIcon
                          icon={resolveRoutineIcon(r)}
                          color={resolveRoutineColor(r)}
                          size="sm"
                          className="!h-6 !w-6 !rounded-md"
                        />
                        <span
                          aria-hidden
                          title={r.last_invocation_status ?? "never invoked"}
                          className={cn(
                            "absolute -bottom-0.5 -right-0.5 h-2 w-2 rounded-full ring-2 ring-card",
                            forkStatusDot(r),
                          )}
                        />
                      </span>
                    }
                    title={r.name || r.slug}
                    description={
                      <>
                        <span className="block truncate font-mono text-[10px] text-muted-foreground-soft">
                          {r.slug}
                        </span>
                        {r.description && (
                          <span className="mt-0.5 block line-clamp-1">
                            {r.description}
                          </span>
                        )}
                      </>
                    }
                    meta={
                      r.invocation_count > 0 ? `ran ${r.invocation_count}×` : "never run"
                    }
                  />
                ))}
              </div>
            )}
            <p className="rounded-md border border-dashed border-border/60 px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
              Forking copies a routine&apos;s recipe into the editor so you can adapt it —
              the original is untouched. Publish creates a new routine.
            </p>
          </CreateSurfaceBody>

          {/* Same shape, same reasons: the rows are the action, so no primary
            and no ⌘↵ to promise, and Cancel leaves the dialog rather than the
            screen. The header's arrow already goes BACK to the entry tiles —
            without this footer the only way OUT of the fork list was the ×,
            so the two exits a person expects side by side were one exit and
            an arrow that keeps you inside.

            The search box is the only thing typed here and it filters a list
            rather than composing anything, so it stays out of `dirty` — the
            same judgement the shell asks for, reached from what the screen
            holds rather than copied from the surface next door. A draft
            carried in from another mode does count, which is the point.

            Cancel is not disabled while a fork is loading. `forking` locks
            the tiles because picking a second routine mid-load races the
            first, but a person who changes their mind during a fetch is
            entitled to leave. */}
          <CreateSurfaceFooter
            hint={
              <>
                <kbd className="font-mono">Esc</kbd> to cancel
              </>
            }
            onCancel={onClose}
          />
        </>
      )}

      {/* ── ADVANCED (the DSL editor) ─────────────────────────────────── */}
      {mode === "advanced" && (
        <>
          <div className="flex shrink-0 items-center justify-between border-b border-hairline px-4 py-2">
            <span className="text-sm font-medium">Recipe</span>
            <button
              type="button"
              aria-pressed={section === "Code"}
              disabled={draftLoading || busy !== "none"}
              onClick={() => setSection(section === "Code" ? "Recipe" : "Code")}
              className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-primary hover:bg-muted coarse:min-h-12"
            >
              <Braces className="size-3.5" />
              {section === "Code" ? "Back to recipe" : "Code"}
            </button>
          </div>
          {/* Keep the code buffer and overview mounted while navigating. */}
          <CreateSurfaceBody
            inert={savedRecipe.current !== null || draftLoading}
            className={cn(
              "flex overflow-y-hidden p-0 sm:p-0",
              savedRecipe.current && "opacity-60",
            )}
          >
            <div
              className={cn(
                "min-w-0 flex-1 overflow-y-auto p-4 sm:p-5",
                section === "Code" && "hidden",
              )}
            >
              <div className="space-y-4">
                {forkSource && (
                  <p className="rounded-xl border p-3 text-sm">
                    Based on {forkSource}. Publishing creates a separate routine.
                  </p>
                )}
                <CreateSurfaceSection title="Identity" concept="routines">
                  <div className="flex items-center gap-3">
                    <CrewIconPopover
                      modal
                      icon={icon}
                      color={color}
                      size="lg"
                      ariaLabel="Choose routine icon and color"
                      onIconChange={setIcon}
                      onColorChange={setColor}
                    />
                    <CreateSurfaceTitleInput
                      id="routine-name"
                      aria-label="Name"
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      placeholder="Routine name"
                    />
                  </div>
                  <CreateSurfaceField label="Description" htmlFor="routine-description">
                    <CreateSurfaceDescriptionInput
                      id="routine-description"
                      value={description || String(parsedDSL?.description ?? "")}
                      onChange={(e) => {
                        setDescription(e.target.value)
                        if (parsedDSL)
                          replaceBuffer(
                            dslFormat === "yaml"
                              ? toYaml({
                                  ...parsedDSL,
                                  description: e.target.value,
                                })
                              : JSON.stringify(
                                  { ...parsedDSL, description: e.target.value },
                                  null,
                                  2,
                                ),
                            { baseline: false },
                          )
                      }}
                      rows={3}
                      placeholder="One-line summary"
                      className="min-h-20 resize-y"
                    />
                  </CreateSurfaceField>

                  <CreateSurfaceField label="Team" htmlFor="routine-author-crew">
                    <CrewPicker
                      id="routine-author-crew"
                      ariaLabel="Select author crew"
                      crews={crews}
                      value={authorCrewId}
                      onChange={setAuthorCrewId}
                      placeholder="— choose at runtime —"
                      clearLabel="— choose at runtime —"
                    />
                  </CreateSurfaceField>
                </CreateSurfaceSection>

                {definitionRows(parsedDSL?.steps).some((s) => s.type === "agent_run") && (
                  <section className="space-y-3 border-t border-hairline pt-4">
                    <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                      Agents
                    </h3>
                    {definitionRows(parsedDSL?.steps).map((step, index) =>
                      step.type === "agent_run" ? (
                        <div key={index} className="space-y-2">
                          <label id={`agent-step-label-${index}`} className="text-sm">
                            {String(step.name || step.id)}
                          </label>
                          <Select
                            value={String(step.agent_slug || "")}
                            onValueChange={(agent_slug) => {
                              if (!parsedDSL) return
                              const steps = definitionRows(parsedDSL.steps).map((s, i) =>
                                i === index ? { ...s, agent_slug } : s,
                              )
                              replaceBuffer(
                                dslFormat === "yaml"
                                  ? toYaml({ ...parsedDSL, steps })
                                  : JSON.stringify({ ...parsedDSL, steps }, null, 2),
                                { baseline: false },
                              )
                            }}
                            disabled={!authorCrewId}
                          >
                            <SelectTrigger
                              aria-labelledby={`agent-step-label-${index}`}
                              className="h-11 w-full rounded-xl bg-muted/30"
                            >
                              <SelectValue placeholder="Choose an agent" />
                            </SelectTrigger>
                            <SelectContent>
                              {!agents.some(
                                (a) =>
                                  a.crew_id === authorCrewId &&
                                  a.slug === step.agent_slug,
                              ) && step.agent_slug ? (
                                <SelectItem value={String(step.agent_slug)} disabled>
                                  {String(step.agent_slug)} · choose an agent from this
                                  crew
                                </SelectItem>
                              ) : null}
                              {agents
                                .filter((a) => a.crew_id === authorCrewId)
                                .map((a) => (
                                  <SelectItem key={a.id} value={a.slug}>
                                    <span className="inline-flex items-center gap-2">
                                      <AgentAvatar
                                        seed={a.avatar_seed || a.name}
                                        style={
                                          a.avatar_style || describeCrew?.avatar_style
                                        }
                                        avatarUrl={a.avatar_url}
                                        className="h-6 w-6"
                                        alt=""
                                      />
                                      <span>{a.name}</span>
                                      <span className="text-xs text-muted-foreground">
                                        {a.role_title || a.agent_role.toLowerCase()}
                                      </span>
                                    </span>
                                  </SelectItem>
                                ))}
                            </SelectContent>
                          </Select>
                        </div>
                      ) : null,
                    )}
                    {!authorCrewId && (
                      <p className="text-xs text-muted-foreground">
                        Choose a crew above to see its agents.
                      </p>
                    )}
                  </section>
                )}
                <details className="group border-t border-hairline">
                  <summary className="flex cursor-pointer list-none items-center justify-between gap-3 py-3">
                    <span className="text-sm font-medium">Inputs</span>
                    <span className="text-xs text-muted-foreground">
                      {definitionRows(parsedDSL?.inputs).length
                        ? `${definitionRows(parsedDSL?.inputs).length} questions`
                        : "No questions"}{" "}
                      · Edit
                    </span>
                  </summary>
                  <div className="pb-3">
                    {" "}
                    {parsedDSL &&
                    (parsedDSL.inputs == null ||
                      (Array.isArray(parsedDSL.inputs) &&
                        parsedDSL.inputs.every(
                          (i) =>
                            i != null &&
                            typeof i === "object" &&
                            typeof i.name === "string",
                        ))) ? (
                      <RoutineInputFormBuilder
                        inputs={
                          definitionRows(
                            parsedDSL.inputs,
                          ) as unknown as RoutineInputSpec[]
                        }
                        onChange={(inputs) =>
                          replaceBuffer(
                            dslFormat === "yaml"
                              ? toYaml({ ...parsedDSL, inputs })
                              : JSON.stringify({ ...parsedDSL, inputs }, null, 2),
                            { baseline: false },
                          )
                        }
                      />
                    ) : (
                      <p className="text-sm text-destructive">
                        Fix the input declarations in Code to edit the start form.
                      </p>
                    )}
                  </div>
                </details>
                <details className="border-t border-hairline">
                  <summary className="flex cursor-pointer list-none items-center justify-between gap-3 py-3">
                    <span className="text-sm font-medium">Results</span>
                    <span className="text-xs text-muted-foreground">
                      {definitionRows(parsedDSL?.outputs).length} declared · View
                    </span>
                  </summary>
                  <div className="space-y-3 pb-3">
                    {definitionRows(parsedDSL?.outputs).map((output, i) => (
                      <div key={i}>
                        <p className="text-sm">
                          {String(output.label || output.name || "Result")}
                        </p>
                        <p className="text-sm text-muted-foreground">
                          {String(
                            output.description ||
                              "No description supplied by the author.",
                          )}
                        </p>
                      </div>
                    ))}
                    {!definitionRows(parsedDSL?.outputs).length && (
                      <p className="text-sm text-muted-foreground">
                        No outputs declared yet.
                      </p>
                    )}
                    <button
                      type="button"
                      className="text-sm text-primary"
                      onClick={() => setSection("Code")}
                    >
                      Edit expected results in Code →
                    </button>
                  </div>
                </details>
                <details className="border-t border-hairline pt-3">
                  <summary className="cursor-pointer text-xs text-muted-foreground">
                    Technical identity
                  </summary>
                  <div className="mt-4">
                    {" "}
                    <CreateSurfaceField
                      label="Routine identifier"
                      htmlFor="routine-slug"
                      hint={
                        routine
                          ? "Permanent identifier. Editing saves a new version of this routine."
                          : "Unique name used in links and code. Edit it in Code."
                      }
                    >
                      <Input
                        id="routine-slug"
                        value={slug}
                        readOnly
                        className={CREATE_SURFACE_INPUT}
                      />
                    </CreateSurfaceField>
                  </div>
                </details>
                {!routine && (
                  <details className="border-t border-hairline pt-3">
                    <summary className="cursor-pointer text-sm">
                      Choose a starter template
                    </summary>
                    <div className="mt-4 grid gap-3 sm:grid-cols-3">
                      {STARTER_TEMPLATES.map((t) => (
                        <CreateSurfaceTile
                          key={t.id}
                          onClick={() => applyTemplate(t.id)}
                          title={t.label}
                          description={t.description}
                        />
                      ))}
                    </div>
                  </details>
                )}
              </div>
              {parsedDSL ? (
                <RoutineRecipeSteps
                  definition={parsedDSL}
                  testPanel={{
                    workspaceId,
                    definition: parsedDSL,
                    busy: busy !== "none",
                    result: testResult,
                    onValidate: handleTestRun,
                    onOpenCode: () => setSection("Code"),
                    parseError,
                  }}
                  slug={slug}
                  name={name || slug}
                  onChange={(next) =>
                    replaceBuffer(
                      dslFormat === "yaml" ? toYaml(next) : JSON.stringify(next, null, 2),
                      { baseline: false },
                    )
                  }
                  onOpenCode={() => setSection("Code")}
                />
              ) : (
                <p className="text-sm text-destructive">
                  Fix the recipe in Code to see its steps.
                </p>
              )}
              {reviewingExisting && (
                <p className="text-sm text-muted-foreground">
                  Plans, webhooks and budgets are managed outside this draft, in the
                  routine’s Plan tab.
                </p>
              )}
              {!reviewingExisting && (
                <div className="space-y-5">
                  {" "}
                  <RoutineTriggerFields
                    workspaceId={workspaceId}
                    value={trigger}
                    onChange={setTrigger}
                  />
                  {(trigger.kind === "schedule" || trigger.kind === "once") &&
                    scheduledFields.length > 0 && (
                      <section className="space-y-3">
                        <h3 className="text-sm font-medium">Inputs for scheduled runs</h3>
                        {scheduledFields.map((field) => (
                          <FormField
                            key={field.name}
                            field={field}
                            value={scheduledValues[field.name] ?? field.default ?? ""}
                            onChange={(e) =>
                              setScheduledValues((values) => ({
                                ...values,
                                [field.name]: e.target.value,
                              }))
                            }
                            idPrefix="scheduled-input-"
                          />
                        ))}
                      </section>
                    )}
                  <p className="text-sm text-muted-foreground">
                    Publishing activates the selected schedule. Save draft leaves
                    automatic starts unchanged.
                  </p>
                </div>
              )}
              <details className="mt-5 border-t border-border pt-4">
                <summary className="cursor-pointer text-sm font-medium">
                  Publication changes
                </summary>
                <div className="mt-3">
                  <RoutinePublicationReview
                    draft={parsedDSL}
                    published={publishedRecipe?.definition}
                    existing={!!draftRef.current?.base_pipeline_id || !!routine}
                    name={name || slug}
                    validated={!!testResult?.passed}
                  />
                </div>
              </details>
            </div>
            <div
              className={cn(
                "flex min-w-0 flex-1 flex-col overflow-hidden",
                section !== "Code" && "hidden",
              )}
            >
              {/* The bar the routine editor has, because this is the
                  same job: format toggle, the slug the DSL will save
                  under, and the parse error WITH its line — the old
                  strip said "invalid JSON" and left you to find it. */}
              <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-hairline px-3 py-1.5">
                <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
                  {/* The kit's segmented control. It was a pair of 10px
                      buttons in a hand-rolled group — the same choice the
                      other surfaces make with CreateSurfaceChoice, drawn
                      differently and, at that size, barely tappable. */}
                  <CreateSurfaceChoice
                    ariaLabel="Code format"
                    value={dslFormat}
                    onChange={switchDslFormat}
                    options={[
                      { value: "yaml" as DslFormat, label: "YAML" },
                      { value: "json" as DslFormat, label: "JSON" },
                    ]}
                  />
                  <span className="font-mono">slug: {slug}</span>
                </div>
                <div className="flex items-center gap-2">
                  {!parsedDSL ? (
                    <span
                      className="truncate text-[10px] text-destructive"
                      title={parseError ?? "Open Test for details"}
                    >
                      Recipe needs attention
                    </span>
                  ) : (
                    <span className="text-[10px] text-success">syntax ok</span>
                  )}
                </div>
              </div>
              <div className="flex min-h-0 flex-1 flex-col">
                <div className="min-h-[240px] w-full min-w-0 flex-1 overflow-hidden">
                  <FileEditor
                    key={editorKey}
                    code={dslText}
                    language={dslFormat}
                    onDocChange={handleDocChange}
                    extraExtensions={dslExtensions}
                    onSave={(next) => {
                      bufferRef.current = next
                      setLiveText(next)
                    }}
                  />
                </div>
              </div>
            </div>
          </CreateSurfaceBody>

          {/* Both verdicts sit outside the scrollport, next to the button that
              produced them. */}
          {testResult && (
            <div
              className={cn(
                "shrink-0 border-t px-4 py-2 text-xs sm:px-5",
                testResult.passed
                  ? "border-success/30 bg-success/5 text-success"
                  : "border-destructive/30 bg-destructive/5 text-destructive",
              )}
            >
              <div className="flex items-center gap-1.5 font-medium">
                {testResult.passed ? "Test passed" : "Test needs attention"}
              </div>
              <p className="mt-0.5 max-h-24 overflow-y-auto break-words font-mono text-[10px] opacity-80">
                {testResult.details}
              </p>
            </div>
          )}

          <CreateSurfaceRefusal
            message={saveError == null ? null : `Save failed — ${saveError}`}
            onDismiss={() => setSaveError(null)}
          />

          {saveError && scheduleConflict && (
            <p className="px-4 py-2 text-sm">
              <a
                className="underline underline-offset-4"
                target="_blank"
                rel="noopener noreferrer"
                href={`/routines?slug=${encodeURIComponent(draftRef.current?.slug || routine?.slug || "")}&view=plan#schedule-${encodeURIComponent(scheduleConflict.schedule_id)}`}
              >
                Review schedule: {scheduleConflict.name}
              </a>
              <span className="ml-2 text-muted-foreground">
                Opens in another tab. Your draft stays here; update the preset, then retry
                publication.
              </span>
            </p>
          )}

          <div className="flex items-center gap-3 border-t border-hairline px-4 py-2 text-xs">
            <button
              type="button"
              disabled={
                busy !== "none" ||
                draftLoading ||
                draftLoadFailed ||
                !!savedRecipe.current
              }
              onClick={() => void handleSave(undefined, true)}
              className="rounded-md border px-3 py-2 disabled:opacity-50"
            >
              Save draft
            </button>
            {draftRevision > 0 && (
              <button
                type="button"
                disabled={busy !== "none"}
                onClick={() => void discardSavedDraft()}
                className="rounded-md border px-3 py-2 disabled:opacity-50"
              >
                Discard saved draft
              </button>
            )}
            {
              <label className="flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={approveRisk}
                  onChange={(e) => setApproveRisk(e.target.checked)}
                />
                Approve capability changes for this publication
              </label>
            }
          </div>
          <Dialog open={reviewOpen} onOpenChange={setReviewOpen}>
            <DialogContent className="max-h-[90dvh] overflow-y-auto border-border bg-card sm:max-w-2xl">
              <DialogHeader>
                <DialogTitle>Confirm publication</DialogTitle>
                <DialogDescription>
                  Review what will change before updating the live recipe.
                </DialogDescription>
              </DialogHeader>
              {reviewError && (
                <p role="alert" className="text-sm text-destructive">
                  {reviewError}
                </p>
              )}
              {publishedRecipe &&
                (name !== publishedRecipe.name ||
                  description !== (publishedRecipe.description ?? "")) && (
                  <div className="space-y-2 rounded-xl border p-3 text-sm">
                    {name !== publishedRecipe.name && (
                      <p>
                        Name: {publishedRecipe.name} → {name || "Untitled recipe"}
                      </p>
                    )}
                    {description !== (publishedRecipe.description ?? "") && (
                      <p>
                        Description: {publishedRecipe.description || "None"} →{" "}
                        {description || "None"}
                      </p>
                    )}
                  </div>
                )}
              <RoutinePublicationReview
                draft={parsedDSL}
                published={publishedRecipe?.definition}
                existing={!!draftRef.current?.base_pipeline_id || !!routine}
                name={name || slug}
                validated={!!testResult?.passed}
              />
              <DialogFooter>
                <Button variant="outline" onClick={() => setReviewOpen(false)}>
                  Back to editing
                </Button>
                <Button
                  disabled={
                    !parsedDSL ||
                    busy !== "none" ||
                    (reviewingExisting && !publishedRecipe?.definition)
                  }
                  onClick={() => {
                    setReviewOpen(false)
                    void handleTestAndSave()
                  }}
                >
                  Confirm and publish
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
          <CreateSurfaceFooter
            hint={
              draftLoading
                ? "Loading saved draft…"
                : draftRevision
                  ? "Saved draft · Publish updates the live recipe"
                  : "Unsaved draft · Save draft does not change live work"
            }
            onCancel={onClose}
            primaryLabel={advancedPrimaryLabel}
            primaryIcon={Save}
            onPrimary={() => setReviewOpen(true)}
            primaryDisabled={draftLoading || draftLoadFailed}
            busy={busy !== "none"}
          />
        </>
      )}
    </CreateSurface>
  )
}

/** Status dot for a fork candidate — same vocabulary as the sidebar. */
function forkStatusDot(r: RoutineListItem): string {
  const s = r.last_invocation_status?.toLowerCase()
  if (s === "completed" || s === "succeeded") return "bg-success"
  if (s === "failed" || s === "error") return "bg-destructive"
  if (r.invocation_count === 0) return "bg-muted-foreground/30"
  return "bg-primary"
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, n - 1) + "…"
}

/** A partially authored definition must not crash its readable preview. */
function definitionRows(value: unknown): Record<string, unknown>[] {
  return Array.isArray(value)
    ? value.filter(
        (row): row is Record<string, unknown> =>
          row != null && typeof row === "object" && !Array.isArray(row),
      )
    : []
}
