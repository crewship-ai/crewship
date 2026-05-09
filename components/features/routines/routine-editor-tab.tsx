"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { AlertCircle, Check, Copy, RotateCcw, Save, Wand2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { FileEditor } from "@/components/features/files/file-editor"
import type { RoutineDetail } from "./routines-detail-panel"

// RoutineEditorTab — editable JSON DSL view backed by the same
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
}

interface ValidationResult {
  ok: boolean
  message?: string
  parsed?: Record<string, unknown>
}

function validate(text: string): ValidationResult {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch (err) {
    return { ok: false, message: err instanceof Error ? err.message : "invalid JSON" }
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    return { ok: false, message: "definition must be a JSON object" }
  }
  const obj = parsed as Record<string, unknown>
  if (typeof obj.name !== "string" || obj.name === "") {
    return { ok: false, message: "missing or empty `name` field" }
  }
  if (!Array.isArray(obj.steps) || obj.steps.length === 0) {
    return { ok: false, message: "missing or empty `steps` array" }
  }
  return { ok: true, parsed: obj }
}

export function RoutineEditorTab({ routine, workspaceId, onSaved }: Props) {
  const initial = useMemo(() => {
    try {
      return JSON.stringify(routine.definition, null, 2)
    } catch {
      return "// failed to render definition"
    }
  }, [routine.definition])

  const [text, setText] = useState(initial)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [copied, setCopied] = useState(false)
  const saveRef = useRef<(() => void) | null>(null)

  // FileEditor controls its own internal state; we re-key it by the
  // routine slug so switching routines remounts with fresh content.
  // text is updated on save() (CodeMirror reads its current doc and
  // hands the string back via onSave).
  useEffect(() => {
    setText(initial)
    setDirty(false)
  }, [initial, routine.slug])

  const validation = useMemo(() => validate(text), [text])

  const handleEditorSave = (next: string) => {
    setText(next)
  }

  const handleFormat = () => {
    if (!validation.ok || !validation.parsed) {
      toast.error("Fix the JSON error before formatting")
      return
    }
    const pretty = JSON.stringify(validation.parsed, null, 2)
    setText(pretty)
    // Force the editor to remount with the formatted content. The
    // simplest way is to re-render with a new key, which we accomplish
    // by toggling the key prop below.
    setEditorKey((k) => k + 1)
    toast.success("Formatted")
  }

  const handleRevert = () => {
    setText(initial)
    setEditorKey((k) => k + 1)
    setDirty(false)
    toast.success("Reverted")
  }

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      toast.error("Copy failed")
    }
  }

  const handleSave = async () => {
    // Always pull the latest doc from CodeMirror — text state may
    // lag if the user typed and clicked Save before the buffer
    // synced through onSave.
    saveRef.current?.()
    const v = validate(text)
    if (!v.ok || !v.parsed) {
      toast.error(v.message ?? "definition is not valid")
      return
    }
    setSaving(true)
    try {
      const res = await fetch(
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
      toast.success("Routine saved")
      setDirty(false)
      onSaved()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Save failed")
    } finally {
      setSaving(false)
    }
  }

  // The editor is remounted on key change to force a fresh CodeMirror
  // instance after Format / Revert (since FileEditor only consumes
  // `code` on first mount). Cheap because the routine DSL is small.
  const [editorKey, setEditorKey] = useState(0)

  return (
    <div className="flex h-full flex-col">
      {/* ── Toolbar ─────────────────────────────────────────────── */}
      <div className="flex shrink-0 items-center justify-between border-b border-white/[0.06] bg-card/30 px-3 py-1.5">
        <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
          <span>JSON DSL</span>
          <span>·</span>
          <span>{text.length.toLocaleString()} chars</span>
          <span>·</span>
          <span className="font-mono">v{routine.dsl_version}</span>
          {dirty && <span className="text-amber-400">· unsaved</span>}
        </div>
        <div className="flex items-center gap-1">
          <Button
            size="sm"
            variant="ghost"
            onClick={handleCopy}
            className="h-6 gap-1.5 text-[10px]"
            title="Copy current buffer"
          >
            {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
            {copied ? "Copied" : "Copy"}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={handleFormat}
            disabled={!validation.ok}
            className="h-6 gap-1.5 text-[10px]"
            title="Re-pretty-print the buffer"
          >
            <Wand2 className="h-3 w-3" />
            Format
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={handleRevert}
            disabled={!dirty}
            className="h-6 gap-1.5 text-[10px]"
            title="Discard changes and reload from server"
          >
            <RotateCcw className="h-3 w-3" />
            Revert
          </Button>
          <Button
            size="sm"
            variant="default"
            onClick={handleSave}
            disabled={!validation.ok || !dirty || saving}
            className="h-6 gap-1.5 text-[10px]"
            title="Save changes (requires OWNER / ADMIN)"
          >
            <Save className="h-3 w-3" />
            {saving ? "Saving…" : "Save"}
          </Button>
        </div>
      </div>

      {/* ── Validation banner (only when the buffer is broken) ── */}
      {!validation.ok && (
        <div className="shrink-0 border-b border-rose-500/30 bg-rose-500/10 px-3 py-1.5 text-[11px] text-rose-300">
          <div className="flex items-start gap-1.5">
            <AlertCircle className="mt-px h-3 w-3 shrink-0" />
            <span className="font-mono">{validation.message}</span>
          </div>
        </div>
      )}

      {/* ── Editor ─────────────────────────────────────────────── */}
      <div className="flex-1 overflow-hidden">
        <FileEditor
          key={`${routine.slug}-${editorKey}`}
          code={text}
          language="json"
          onSave={handleEditorSave}
          onDirtyChange={setDirty}
          saveRef={saveRef}
        />
      </div>

      {/* ── Footer hint ────────────────────────────────────────── */}
      <div className="shrink-0 border-t border-white/[0.06] bg-card/20 px-3 py-1.5 text-[10px] text-muted-foreground">
        Cmd/Ctrl+S to flush buffer · Save lands changes when JSON is valid
        and has both <span className="font-mono">name</span> +{" "}
        <span className="font-mono">steps</span>. Save needs OWNER/ADMIN role
        (lower roles get a clear 403; we&apos;ll add test_run+save chaining
        next).
      </div>
    </div>
  )
}
