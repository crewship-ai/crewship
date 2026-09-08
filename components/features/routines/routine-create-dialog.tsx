"use client"

import { RoutineTriggerFields, type RoutineTriggerDraft } from "./routine-trigger-fields"
import { useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import {
  FlaskConical,
  Save,
  Sparkles,
  GitFork,
  Braces,
  Search,
} from "lucide-react"
import { FormField } from "@/components/features/chat/asks/form-field"
import { slashFieldsFromRoutineInputs, routineInputsFromValues, isMissingRequired, type RoutineInputSpec } from "@/lib/routine-inputs"
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
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceLoading,
  CreateSurfaceRefusal,
  CreateSurfaceSecondaryAction,
  CreateSurfaceSection,
  CreateSurfaceTile,
} from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
// The shared picker, not a second copy of it. The local one was a verbatim
// fork that had already drifted: it never got the `modal` prop that fixes
// scrolling inside a dialog, so its list clipped and would not move — the
// exact cost of the duplication.
import { CrewPicker } from "@/components/features/crews/crew-picker"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { FileEditor } from "@/components/features/files/file-editor"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"
import { parseRoutineBuffer } from "@/lib/routine-buffer"
import { routineDslExtensions } from "@/lib/routine-dsl-editor-extensions"
import { convertDsl, toYaml, type DslFormat } from "@/lib/routine-dsl-format"

/** Which reading of the definition the editor pane is showing. */
type EditorPane = "code" | "graph"

// All three creation paths share the same recipe definition and save contract.
// The server's test_run endpoint performs static validation and mints the save
// token; it does not execute work. Code stays mounted while sections change.

interface Props {
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
      inputs: [{ name: "text", type: "string", required: true, description: "Text to summarize" }],
      outputs: [{ name: "summary", type: "string" }],
      steps: [
        {
          id: "summarize",
          type: "agent_run",
          agent_slug: "your-agent-slug",
          complexity: "fast",
          prompt: "Summarize the following text in 3 concise bullet points:\n\n{{ inputs.text }}",
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
          prompt: "Summarize the following content in 3 bullets:\n\n{{ steps.fetch.output }}",
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

export function RoutineCreateDialog({ workspaceId, open, onClose, onCreated }: Props) {
  const router = useRouter()
  const [mode, setMode] = useState<Mode>("entry")

  // ── Shared meta ────────────────────────────────────────────────────
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
  const [editorPane, setEditorPane] = useState<EditorPane>("code")
  const [dslText, setDslText] = useState(() => toYaml(STARTER_TEMPLATES[0].json))
  const [liveText, setLiveText] = useState(() => toYaml(STARTER_TEMPLATES[0].json))
  const bufferRef = useRef(toYaml(STARTER_TEMPLATES[0].json))
  const [editorKey, setEditorKey] = useState(0)
  const [parseError, setParseError] = useState<string | null>(null)
  const [busy, setBusy] = useState<"none" | "testing" | "saving">("none")
  const [trigger, setTrigger] = useState<RoutineTriggerDraft>({ kind: "manual", cron: "0 9 * * 1-5", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", at: "" })
  const [sourcesAndOutput, setSourcesAndOutput] = useState("")
  const [scheduledValues, setScheduledValues] = useState<Record<string,string>>({})
  const [testResult, setTestResult] = useState<{ passed: boolean; details: string } | null>(null)
  const [section, setSection] = useState("Overview")
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
    if (open) setMode("entry")
  }, [open])

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

  if (!open) return null

  const scheduledFields = slashFieldsFromRoutineInputs(Array.isArray(parsedDSL?.inputs) ? parsedDSL.inputs as RoutineInputSpec[] : [])
  const slug = (parsedDSL?.["name"] as string) || "my-routine"

  const applyTemplate = (templateId: string) => {
    const tpl = STARTER_TEMPLATES.find((t) => t.id === templateId)
    if (!tpl) return
    const j = { ...tpl.json, name: name || tpl.json.name, description: description || tpl.json.description }
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
  const replaceBuffer = (next: string, { baseline = true }: { baseline?: boolean } = {}) => {
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
    if (parsed.ok) setDescription(String(parsed.parsed.description ?? ""))
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
  const handleTestRun = async (): Promise<{ passed: boolean; token: string | null }> => {
    const parsed = parseDSLWithError()
    if (!parsed) {
      toast.error("Fix the definition before continuing")
      return { passed: false, token: null }
    }
    setBusy("testing")
    setTestResult(null)
    setSaveToken(null)
    try {
      const testBody: Record<string, unknown> = { definition: parsed, sample_inputs: {} }
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
        toast.error("Definition validation failed", { description: msg })
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
        details: passed ? `Passed${data.output ? ` (output: ${truncate(String(data.output), 120)})` : ""}` : data.error ?? "test_run reported failure",
      })
      const token = passed && data.save_token ? data.save_token : null
      if (token) {
        setSaveToken(token)
      }
      if (passed) {
        toast.success("Definition validated — no work was executed")
      } else {
        toast.error("Definition validation failed", { description: data.error ?? "see details below" })
      }
      return { passed, token }
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      setTestResult({ passed: false, details: msg })
      toast.error("Definition validation unavailable", { description: msg })
      return { passed: false, token: null }
    } finally {
      setBusy("none")
    }
  }

  const handleSave = async (tokenOverride?: string | null) => {
    const parsed = parseDSLWithError()
    if (!parsed) {
      toast.error("Fix the definition before continuing")
      return
    }
    if (!parsed["name"]) {
      toast.error("DSL must include a 'name' (used as slug)")
      return
    }
    setBusy("saving")
    setSaveError(null)
    try {
      const body: Record<string, unknown> = {
        slug: parsed["name"],
        name: name || (parsed["name"] as string),
        description: description || (parsed["description"] as string | undefined) || "",
        definition: parsed,
        skip_test_gate: false,
      }
      const values = Object.fromEntries(scheduledFields.map(f => [f.name, scheduledValues[f.name] ?? f.default ?? ""]))
      if (trigger.kind === "schedule" || trigger.kind === "once") {
        const missing = scheduledFields.find(f => isMissingRequired(f, values[f.name]))
        if (missing) throw new Error(`Fill in ${missing.name} before activating this start.`)
      }
      const scheduledInputs = routineInputsFromValues(scheduledFields, values)
      if (trigger.kind === "schedule") body.trigger = { kind: "schedule", cron: trigger.cron, timezone: trigger.timezone, inputs: scheduledInputs }
      if (trigger.kind === "manual") body.trigger = { kind: "manual" }
      if (trigger.kind === "once") {
        if (!trigger.at || !(Date.parse(trigger.at) > Date.now())) throw new Error("Choose a future date and time")
        body.trigger = { kind: "once", fire_at: new Date(trigger.at).toISOString(), inputs: scheduledInputs }
      }
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

      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/save`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      const saved = (await res.json()) as { slug: string }
      toast.success(`Routine "${saved.slug}" saved`)
      onCreated(saved.slug)
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
    const { passed, token } = await handleTestRun()
    if (passed) {
      await handleSave(token)
    }
  }

  // Describe handoff: navigate into the Lead's chat with the authoring
  // prompt pre-sent. The chat page reads ?prompt= , opens a fresh session
  // and auto-sends once connected; the Routine-Author skill takes over.
  const handleDescribe = () => {
    const text = goal.trim()
    if (!describeLead || !text) return
    const prompt = `Author a routine for me: ${text}
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
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/${item.slug}`)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const detail = (await res.json()) as { definition?: Record<string, unknown>; name?: string; description?: string }
      const def = detail.definition ?? {}
      // Rename the fork so it doesn't collide with the source slug on save.
      const forkName = `${item.slug}-copy`
      const nextDef = { ...def, name: forkName }
      // Not baselined: a fork is content you asked for, and losing it silently
      // is the same defect as losing a typed goal. The source description
      // usually keeps the surface dirty on its own; a routine without one
      // would have had nothing else to go on.
      replaceBuffer(dslFormat === "yaml" ? toYaml(nextDef) : JSON.stringify(nextDef, null, 2), {
        baseline: false,
      })
      setForkSource(item.name || item.slug)
      setSection("Overview")
      setName("")
      setDescription(item.description ?? detail.description ?? "")
      setParseError(null)
      setTestResult(null)
      setSaveToken(null)
      setMode("advanced")
    } catch (e) {
      toast.error("Could not load routine", { description: e instanceof Error ? e.message : String(e) })
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
          ? "Build your routine"
          : "New routine"
  const headerSub =
    mode === "describe"
      ? "a Lead drafts it with you in chat"
      : mode === "fork"
        ? "fork one of your own routines"
        : mode === "advanced"
          ? "Prepare your recipe, review its steps, then validate and save."
          // The entry screen had no subtitle, so three tiles appeared with
          // nothing saying they are three routes to the same place. People
          // read a picker as "which kind am I making", and the answer is that
          // it does not matter — pick the one whose inputs you already have.
          : "Three ways in. All three land on the same routine — pick the one you have inputs for."

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
    name.trim() !== "" ||
    description.trim() !== "" ||
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

  const advancedPrimaryLabel = busy === "testing" ? "Validating…" : busy === "saving" ? "Saving…" : "Validate & Save"

  return (
    <CreateSurface
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
      size="lg"
      dirty={dirty}
      discardLabel="this routine"
      onSubmit={handleKeyboardSubmit}
      className={mode === "advanced" ? "sm:h-[min(90vh,900px)] sm:max-h-[90vh] sm:max-w-[1180px]" : undefined}
    >
      <CreateSurfaceHeader
        concept="routines"
        context="Routines"
        title={headerTitle}
        description={headerSub}
        onBack={mode === "entry" ? undefined : () => setMode("entry")}
        onClose={onClose}
      />

      {/* ── ENTRY — three cards ───────────────────────────────────────── */}
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
            description="Build a recipe with a clear overview of inputs, steps and outputs. Edit YAML or JSON, preview the graph, then validate and save."
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
              <CreateSurfaceField label="Owner (crew)" htmlFor="describe-crew" className="flex-1">
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
              <CreateSurfaceField label="What should the routine do?" htmlFor="describe-goal">
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

            <CreateSurfaceField label="Sources and expected outputs (optional)" htmlFor="routine-sources-output"><textarea id="routine-sources-output" value={sourcesAndOutput} onChange={e => setSourcesAndOutput(e.target.value)} className="min-h-20 w-full rounded-md border bg-background p-2 text-sm" placeholder="What should it work with, and what should you receive?" /></CreateSurfaceField>
            <RoutineTriggerFields workspaceId={workspaceId} value={trigger} onChange={setTrigger} />
            <p className="text-[11px] leading-relaxed text-muted-foreground">
              {describeLead?.name ?? "The Lead"} will draft it and ask a couple of questions, then show a
              readable preview — nothing is saved without you. It grounds the draft in your crew's connected
              integrations, your existing routines, and the routine schema.
            </p>

            <div className="flex gap-3 text-[11px] text-muted-foreground">
              <button type="button" className="hover:text-foreground" onClick={() => setMode("fork")}>
                fork a routine
              </button>
              <button type="button" className="hover:text-foreground" onClick={() => setMode("advanced")}>
                Build your routine
              </button>
            </div>
          </CreateSurfaceBody>

          <CreateSurfaceFooter
            onCancel={onClose}
            primaryLabel={`Draft with ${describeLead?.name ?? "a Lead"}`}
            primaryIcon={Sparkles}
            primaryDisabled={!describeLead || !goal.trim() || (trigger.kind === "once" && !(Date.parse(trigger.at) > Date.now()))}
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
              className="h-8 pl-8 text-xs max-sm:h-12 max-sm:text-sm"
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
                      <span className="block truncate font-mono text-[10px] text-muted-foreground-soft">{r.slug}</span>
                      {r.description && <span className="mt-0.5 block line-clamp-1">{r.description}</span>}
                    </>
                  }
                  meta={r.invocation_count > 0 ? `ran ${r.invocation_count}×` : "never run"}
                />
              ))}
            </div>
          )}
          <p className="rounded-md border border-dashed border-border/60 px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
            Forking copies a routine&apos;s definition into the editor so you can adapt it — the original is
            untouched. Save creates a new routine.
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
          {/* The body is the shell's scrollport everywhere else; here it is a
              two-pane frame, because a code editor that scrolls the dialog
              instead of itself is not a code editor. */}
          <CreateSurfaceBody className="flex overflow-y-hidden p-0 sm:p-0">
            {/* The kit's Section and Field, not a column of bespoke
                `text-[10px] uppercase` labels over `h-7` inputs. Two of those
                were 28px tall, which is not a tap target on a phone — and
                every other surface says its field labels the same way. */}
            <aside className="flex w-48 shrink-0 flex-col gap-2 overflow-y-auto border-r border-hairline bg-card p-3 max-sm:w-32 max-sm:p-2">
              <p className="px-2 py-2 text-xs text-muted-foreground">Recipe · Unsaved</p>
              <nav aria-label="Recipe sections" className="space-y-1">
                {["Overview", "Inputs", "Steps", "Outputs", "Code", "Schedule", "Validate"].map(item => <button key={item} type="button" aria-current={section === item ? "page" : undefined} onClick={() => setSection(item)} className={cn("w-full rounded-xl px-3 py-2.5 text-left text-sm hover:bg-muted", section === item ? "bg-primary/10 text-primary" : "text-muted-foreground")}>{item}</button>)}
              </nav>
              <p className="mt-auto px-2 pt-6 text-xs leading-relaxed text-muted-foreground">Work is not executed while you edit or validate.</p>
            </aside>
            <div className={cn("min-w-0 flex-1 overflow-y-auto p-5 sm:p-7", section === "Code" && "hidden")}>
              <h2 className="text-lg font-medium">{section === "Validate" ? "Review before saving" : section}</h2>
              <p className="mb-6 mt-1 text-sm text-muted-foreground">{({ Overview: "Give your routine a clear purpose and choose the team responsible for it.", Inputs: "What this recipe needs before it can start.", Steps: "The recipe your agents and tools will follow.", Outputs: "What this recipe declares it will produce.", Schedule: "Choose when the saved routine should start.", Validate: "Check the definition without executing agents, scripts or external actions." } as Record<string, string>)[section]}</p>
              <div hidden={section !== "Overview"} className="space-y-6">
                {forkSource && <p className="rounded-xl border p-3 text-sm">Based on {forkSource}. Saving creates a separate routine.</p>}
              <CreateSurfaceSection title="Routine details" concept="routines">
                <CreateSurfaceField
                  label="Name"
                  htmlFor="routine-name"
                  hint="Display name shown in Routines and Calendar."
                >
                  <Input
                    id="routine-name"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="Friendly name"
                    className={CREATE_SURFACE_INPUT}
                  />
                </CreateSurfaceField>

                <CreateSurfaceField label="Description" htmlFor="routine-description">
                  <CreateSurfaceDescriptionInput
                    id="routine-description"
                    value={description || String(parsedDSL?.description ?? "")}
                    onChange={(e) => { setDescription(e.target.value); if (parsedDSL) replaceBuffer(dslFormat === "yaml" ? toYaml({ ...parsedDSL, description: e.target.value }) : JSON.stringify({ ...parsedDSL, description: e.target.value }, null, 2), { baseline: false }) }}
                    rows={3}
                    placeholder="One-line summary"
                    className="resize-none rounded-md border border-hairline bg-background p-1.5"
                  />
                </CreateSurfaceField>

                <CreateSurfaceField
                  label="Author crew"
                  htmlFor="routine-author-crew"
                  hint="Crew whose agents and credentials run this routine."
                >
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
                <CreateSurfaceField label="Routine identifier" htmlFor="routine-slug" hint="Unique name used in links and code. Edit it in Code."><Input id="routine-slug" value={slug} readOnly className={CREATE_SURFACE_INPUT} /></CreateSurfaceField>
              </CreateSurfaceSection>

                <details className="rounded-2xl border border-hairline p-4"><summary className="cursor-pointer text-sm">Choose a starter template</summary><div className="mt-4 grid gap-3 sm:grid-cols-3">

                {STARTER_TEMPLATES.map((t) => (
                  <CreateSurfaceTile
                    key={t.id}
                    onClick={() => applyTemplate(t.id)}
                    title={t.label}
                    description={t.description}
                  />
                ))}
                </div></details>
              </div>
              {(section === "Inputs" || section === "Outputs") && <div className="space-y-3">
                {definitionRows(parsedDSL?.[section.toLowerCase()]).map((field, i) => <div key={i} className="rounded-2xl border border-hairline bg-card p-4"><div className="flex flex-wrap items-center gap-2"><span className="font-medium">{String(field.label || field.name || `Field ${i + 1}`)}</span><span className="text-xs text-muted-foreground">{String(field.type || "value")}{field.required ? " · Required" : ""}</span></div>{field.description != null && <p className="mt-2 text-sm text-muted-foreground">{String(field.description)}</p>}</div>)}
                {!Array.isArray(parsedDSL?.[section.toLowerCase()]) || !(parsedDSL?.[section.toLowerCase()] as unknown[])?.length ? <p className="rounded-2xl border border-dashed p-5 text-sm text-muted-foreground">No {section.toLowerCase()} declared in this definition.</p> : null}
                <button type="button" className="text-sm text-primary" onClick={() => setSection("Code")}>Edit {section.toLowerCase()} in Code →</button>
              </div>}
              {section === "Steps" && <div className="space-y-4">{definitionRows(parsedDSL?.steps).map((step, i) => <div key={i} className="rounded-2xl border border-hairline p-4"><h3 className="font-medium">{i + 1}. {String(step.name || step.id || "Step")}</h3><p className="mt-1 text-xs text-muted-foreground">{String(step.type || "Unspecified").replaceAll("_", " ")}{step.agent_slug ? ` · ${step.agent_slug}` : ""}</p>{(step.description || step.prompt) ? <p className="mt-3 whitespace-pre-wrap break-words text-sm text-muted-foreground">{String(step.description || step.prompt)}</p> : null}</div>)}<div className="relative h-[380px] overflow-hidden rounded-2xl border border-hairline">{parsedDSL ? <RoutineDefinitionCanvas definition={parsedDSL} slug={slug} name={name || slug} /> : <p className="p-5 text-sm text-muted-foreground">Fix the definition in Code to display its steps.</p>}</div><button type="button" className="text-sm text-primary" onClick={() => setSection("Code")}>Edit steps in Code →</button></div>}
              {section === "Schedule" && <div className="space-y-5">              <RoutineTriggerFields workspaceId={workspaceId} value={trigger} onChange={setTrigger} />
              {(trigger.kind === "schedule" || trigger.kind === "once") && scheduledFields.length > 0 && <section className="space-y-3"><h3 className="text-sm font-medium">Inputs for scheduled runs</h3>{scheduledFields.map(field => <FormField key={field.name} field={field} value={scheduledValues[field.name] ?? field.default ?? ""} onChange={e => setScheduledValues(values => ({ ...values, [field.name]: e.target.value }))} idPrefix="scheduled-input-" />)}</section>}
<p className="text-sm text-muted-foreground">Saving activates the selected schedule. Leave “When I start it” selected to save without an automatic start.</p></div>}
              {section === "Validate" && <div className="space-y-4"><div className="rounded-2xl border border-hairline p-5"><h3 className="font-medium">Definition check</h3><p className="mt-2 text-sm text-muted-foreground">Checks the recipe and its configuration. A successful check does not mean a real run has succeeded.</p><button type="button" disabled={busy !== "none"} onClick={handleTestRun} className="mt-4 rounded-full border px-4 py-2 text-sm">{busy === "testing" ? "Validating…" : "Check definition"}</button></div><div className="rounded-2xl border border-hairline p-5"><h3 className="font-medium">Real execution</h3><p className="mt-2 text-sm text-muted-foreground">After saving, use Run to review inputs and start real work. Follow its steps and outputs in History and Activity.</p></div>{parseError && <button type="button" className="text-sm text-destructive" onClick={() => setSection("Code")}>{parseError} · Open Code →</button>}</div>}
              {section !== "Validate" && <button type="button" className="mt-6 rounded-full border border-hairline px-4 py-2 text-sm" onClick={() => setSection(({ Overview: "Inputs", Inputs: "Steps", Steps: "Outputs", Outputs: "Code", Schedule: "Validate" } as Record<string, string>)[section] || "Validate")}>Continue →</button>}
            </div>
            <div className={cn("flex min-w-0 flex-1 flex-col overflow-hidden", section !== "Code" && "hidden")}>
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
                    ariaLabel="DSL format"
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
                  {/* Code and graph SHARE the pane rather than splitting it.
                   *
                   * They sat side by side, and with the identity aside also
                   * on screen that is three columns inside an 800px surface:
                   * the graph got ~240px and the code ~52% of what was left,
                   * so neither was usable and the thing you are actually
                   * doing here — typing a DSL — was the narrower of the two.
                   *
                   * Code leads because it is the input; the graph is a
                   * reading of it. Switching is one click and keeps the
                   * buffer, so it is a look rather than a mode change. */}
                  <CreateSurfaceChoice
                    ariaLabel="Editor pane"
                    value={editorPane}
                    onChange={(pane) => { if (pane === "code") setDslText(bufferRef.current); setEditorPane(pane) }}
                    options={[
                      { value: "code" as EditorPane, label: "Code" },
                      { value: "graph" as EditorPane, label: "Preview" },
                    ]}
                  />
                  {!parsedDSL ? (
                    <span className="truncate text-[10px] text-destructive" title={parseError ?? "Open Validate for details"}>
                      Definition needs attention
                    </span>
                  ) : (
                    <span className="text-[10px] text-success">syntax ok</span>
                  )}
                </div>
              </div>
              <div className="flex min-h-0 flex-1 flex-col">
                {editorPane === "code" ? (
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
                ) : (
                  <div className="relative min-h-[240px] w-full min-w-0 flex-1">
                    {parsedDSL ? (
                      <RoutineDefinitionCanvas
                        definition={parsedDSL}
                        slug={slug}
                        name={name || slug}
                      />
                    ) : (
                      <div className="flex h-full flex-col items-center justify-center gap-2 px-4 text-center text-[11px] text-muted-foreground-soft">
                        <span>The graph appears once the definition parses.</span>
                        {parseError && (
                          <span className="font-mono text-destructive">{parseError}</span>
                        )}
                      </div>
                    )}
                  </div>
                )}
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
                {testResult.passed ? "Definition valid" : "Definition invalid"}
              </div>
              <p className="mt-0.5 max-h-24 overflow-y-auto break-words font-mono text-[10px] opacity-80">{testResult.details}</p>
            </div>
          )}

          <CreateSurfaceRefusal
            message={saveError == null ? null : `Save failed — ${saveError}`}
            onDismiss={() => setSaveError(null)}
          />

          <CreateSurfaceFooter
            hint="Unsaved recipe · Validation does not run work"
            onCancel={onClose}
            secondary={
              <span className="max-sm:hidden"><CreateSurfaceSecondaryAction
                icon={FlaskConical}
                onClick={handleTestRun}
                disabled={busy !== "none"}
              >
                {busy === "testing" ? "Validating…" : "Validate definition"}
              </CreateSurfaceSecondaryAction></span>
            }
            primaryLabel={advancedPrimaryLabel}
            primaryIcon={Save}
            onPrimary={handleTestAndSave}
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
  return Array.isArray(value) ? value.filter((row): row is Record<string, unknown> => row != null && typeof row === "object" && !Array.isArray(row)) : []
}
