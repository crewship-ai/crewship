"use client"

import { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from "react"
import { AlertCircle, RotateCcw, Save, Wand2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { FileEditor } from "@/components/features/files/file-editor"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { convertDsl, hasYamlComments, toYaml, type DslFormat } from "@/lib/routine-dsl-format"
import { parseRoutineBuffer } from "@/lib/routine-buffer"
import { stepIdAtLine, stepLineRanges } from "@/lib/routine-dsl-lines"
import { routineDslExtensions } from "@/lib/routine-dsl-editor-extensions"
import type { RoutineDetail } from "./routines-detail-panel"

// RoutineEditorTab — editable DSL view backed by the same
// CodeMirror surface the file-editor uses. Three primary affordances:
//
//   - live syntax + structural validation (must parse to an object
//     with `name` + `steps` to be considered savable)
//   - Format button to re-pretty-print the buffer (Cmd+Shift+F also)
//   - Save button that POSTs the new definition to /pipelines/save
//
// Save uses skip_test_gate=true so an OWNER/ADMIN editing in the UI
// can land changes without first running through /test_run. Lower
// roles get a clear 403 message back from the server. A follow-up
// will chain test_run → save_token → save behind one button so any
// MANAGER+ role can edit; for now this path is the fast lane the
// user asked for.

interface Props {
  routine: RoutineDetail
  workspaceId: string
  onSaved: () => void
  /**
   * Fires with the step the caret sits in, or null between steps.
   *
   * Already deduped — only a change of STEP is reported, not every
   * caret move — so the caller can drive a viewport with it directly.
   * The caret fires on every arrow key; forwarding each one would have
   * the graph re-centre on the node it is already centred on dozens of
   * times a second while someone types.
   */
  onStepAtCaret?: (stepId: string | null) => void
}

export function RoutineEditorTab({ routine, workspaceId, onSaved, onStepAtCaret }: Props) {
  const [format, setFormat] = useState<DslFormat>("yaml")
  const initial = useMemo(() => {
    try {
      const json = JSON.stringify(routine.definition, null, 2)
      if (format === "json") return json
      const converted = convertDsl(json, "json", "yaml")
      return converted.ok ? converted.text : json
    } catch {
      return "// failed to render definition"
    }
  }, [routine.definition, format])

  const [text, setText] = useState(initial)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const saveRef = useRef<(() => void) | null>(null)
  // bufferRef mirrors the editor's latest doc. FileEditor only hands
  // its buffer back through onSave (⌘S or a saveRef flush), and
  // setText is asynchronous — so the save path below must read this
  // ref, not the `text` state, or it validates + POSTs the PREVIOUS
  // value (typing then clicking Save silently saved stale JSON).
  const bufferRef = useRef(initial)

  // The editor is remounted on key change to force a fresh CodeMirror
  // instance after Format / Revert / refetch (FileEditor only consumes
  // `code` on first mount). Cheap because the routine DSL is small.
  const [editorKey, setEditorKey] = useState(0)

  // FileEditor controls its own internal state; we re-key it by the
  // routine slug so switching routines remounts with fresh content.
  // A same-slug refetch (new `initial`) must ALSO remount — without
  // the key bump the visible editor keeps the old buffer while
  // bufferRef already points at the new definition.
  useEffect(() => {
    setText(initial)
    bufferRef.current = initial
    setDirty(false)
    setEditorKey((k) => k + 1)
  }, [initial, routine.slug])

  const validation = useMemo(() => parseRoutineBuffer(text, format), [text, format])

  // Schema completion + inline diagnostics. Memoized on the format
  // because FileEditor rebuilds its EditorState when this identity
  // changes — an unmemoized array would blow the buffer away on every
  // render.
  const extraExtensions = useMemo(() => routineDslExtensions(format), [format])

  // Line spans come from the LIVE buffer, not the definition as first
  // rendered: inserting one line shifts every step below it, and spans
  // computed once resolve the caret against positions that no longer
  // exist. Deferred so a parse does not run on the keystroke itself —
  // a frame of lag is invisible, parsing per character is not.
  const deferredText = useDeferredValue(text)
  const stepRanges = useMemo(() => stepLineRanges(deferredText), [deferredText])
  const bufferHasComments = useMemo(
    () => format === "yaml" && hasYamlComments(deferredText),
    [deferredText, format],
  )
  const lastStepRef = useRef<string | null>(null)
  const handleCursorLine = useCallback(
    (line: number) => {
      const id = stepIdAtLine(stepRanges, line)
      if (id === lastStepRef.current) return
      lastStepRef.current = id
      onStepAtCaret?.(id)
    },
    [stepRanges, onStepAtCaret],
  )

  // Switching format converts the BUFFER, not the stored definition, so
  // an in-progress edit survives the toggle instead of being discarded.
  const switchFormat = (next: DslFormat) => {
    if (next === format) return
    const converted = convertDsl(bufferRef.current, format, next)
    if (!converted.ok) {
      toast.error(`Fix the ${format.toUpperCase()} error before switching`)
      return
    }
    setFormat(next)
    setText(converted.text)
    bufferRef.current = converted.text
    setEditorKey((k) => k + 1)
  }

  const handleEditorSave = (next: string) => {
    bufferRef.current = next
    setText(next)
  }

  const handleFormat = () => {
    if (!validation.ok || !validation.parsed) {
      toast.error(`Fix the ${format.toUpperCase()} error before formatting`)
      return
    }
    const pretty =
      format === "yaml" ? toYaml(validation.parsed) : JSON.stringify(validation.parsed, null, 2)
    setText(pretty)
    bufferRef.current = pretty
    // Force the editor to remount with the formatted content. The
    // simplest way is to re-render with a new key, which we accomplish
    // by toggling the key prop below.
    setEditorKey((k) => k + 1)
    toast.success("Formatted")
  }

  const handleRevert = () => {
    setText(initial)
    bufferRef.current = initial
    setEditorKey((k) => k + 1)
    setDirty(false)
    toast.success("Reverted")
  }

  const handleSave = async () => {
    // Always pull the latest doc from CodeMirror. FileEditor's
    // saveRef invokes onSave(doc) synchronously, which lands in
    // bufferRef — reading the `text` state here would still see the
    // pre-flush value (setText hasn't re-rendered yet), silently
    // saving a stale definition when the user types and clicks Save
    // without pressing ⌘S first.
    saveRef.current?.()
    // The SAME parse the header's validity indicator ran. It used to be
    // a second, JSON-only function, so a valid YAML buffer showed
    // "syntax ok" and then failed on Save with a JSON parser error.
    const v = parseRoutineBuffer(bufferRef.current, format)
    if (!v.ok) {
      toast.error(v.message)
      return
    }
    setSaving(true)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/save`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            slug: routine.slug,
            name: (v.parsed.name as string) ?? routine.name,
            description:
              typeof v.parsed.description === "string"
                ? v.parsed.description
                : routine.description ?? "",
            definition: v.parsed,
            author_crew_id: routine.author_crew_id,
            // OWNER / ADMIN can land edits without re-running test_run
            // first. The server gate-checks the role; lower roles get
            // a 403 with an actionable message.
            skip_test_gate: true,
          }),
        },
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        const msg = body?.error ?? body?.detail ?? `Save failed (${res.status})`
        toast.error(msg)
        return
      }
      // Save re-classifies risk, and a routine it judges risky lands as
      // `proposed` — it stops being runnable until a manager approves
      // it. That is the gate working, but a bare "Routine saved" while
      // an approval banner silently appears above reads as the page
      // breaking. Say which happened.
      const saved = (await res.json().catch(() => null)) as { status?: string } | null
      if (saved?.status === "proposed") {
        toast.warning("Saved — sent for approval", {
          description:
            "The change was classified risky, so the routine can't run until a manager approves it.",
        })
      } else {
        toast.success("Routine saved")
      }
      setDirty(false)
      onSaved()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Save failed")
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full flex-col">
      {/* ── Toolbar ─────────────────────────────────────────────── */}
      <div className="flex shrink-0 items-center justify-between gap-3 border-b border-border/60 bg-card/30 px-4 py-2.5">
        <div className="flex items-center gap-2.5 text-[12px] text-muted-foreground">
          <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
            {(["yaml", "json"] as const).map((f) => (
              <button
                key={f}
                type="button"
                onClick={() => switchFormat(f)}
                aria-pressed={format === f}
                className={cn(
                  "rounded px-1.5 py-0.5 font-mono text-[10px] uppercase transition-colors",
                  format === f
                    ? "bg-primary/15 text-primary"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {f}
              </button>
            ))}
          </div>
          <span className="opacity-60">·</span>
          <span className="font-mono">DSL v{routine.dsl_version}</span>
          {dirty && (
            <span className="inline-flex items-center gap-1.5 rounded-full bg-warn/20 px-2.5 py-0.5 text-[11px] font-medium text-warn">
              <span className="h-1.5 w-1.5 rounded-full bg-current" />
              unsaved
            </span>
          )}
        </div>
        <div className="flex items-center gap-1.5">
          <Button
            size="sm"
            variant="ghost"
            onClick={handleFormat}
            disabled={!validation.ok}
            className="h-8 gap-1.5 px-2.5 text-xs"
            title="Re-indent the buffer from the parsed definition — sorts nothing, changes no values"
          >
            <Wand2 className="h-3.5 w-3.5" />
            Format
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={handleRevert}
            disabled={!dirty}
            className="h-8 gap-1.5 px-2.5 text-xs"
            title="Discard changes and reload from server"
          >
            <RotateCcw className="h-3.5 w-3.5" />
            Revert
          </Button>
          <Button
            size="sm"
            variant="default"
            onClick={handleSave}
            disabled={!validation.ok || !dirty || saving}
            className="h-8 gap-1.5 px-3 text-xs font-semibold"
            title={
              validation.ok
                ? "Save changes. \u2318/Ctrl+S flushes the buffer first. Requires OWNER / ADMIN."
                : `Cannot save: ${validation.ok ? "" : validation.message}`
            }
          >
            <Save className="h-3.5 w-3.5" />
            {saving ? "Saving…" : "Save"}
          </Button>
        </div>
      </div>

      {/* ── Validation banner (only when the buffer is broken) ── */}
      {!validation.ok && (
        <div className="shrink-0 border-b border-destructive/30 bg-destructive/[0.06] px-4 py-2.5 text-[13px] text-destructive">
          <div className="flex items-start gap-2">
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
            <span className="font-mono">{validation.message}</span>
          </div>
        </div>
      )}

      {/* ── Editor ─────────────────────────────────────────────── */}
      <div className="flex-1 overflow-hidden">
        <FileEditor
          key={`${routine.slug}-${format}-${editorKey}`}
          code={text}
          language={format}
          onSave={handleEditorSave}
          onDirtyChange={setDirty}
          saveRef={saveRef}
          onCursorLine={handleCursorLine}
          onDocChange={setText}
          extraExtensions={extraExtensions}
        />
      </div>

      {/* The footer earns its place or it does not appear.
          
          It used to carry three permanent sentences — how to flush the
          buffer, what makes a save land, which role is required — plus
          a standing warning about YAML comments. All true, none of it
          news after the first read, and a strip of always-on text is
          what people learn to skip. The instructions moved onto the
          Save button, which is where you look when Save does not do
          what you expected. What is left is the one thing that can lose
          work, shown only when there is work to lose. */}
      {format === "yaml" && bufferHasComments && (
        <div className="shrink-0 border-t border-warn/30 bg-warn/[0.06] px-4 py-2 text-[11px] text-warn">
          This document has comments. Canonical JSON is what gets stored, so they will not survive
          the save.
        </div>
      )}
    </div>
  )
}
