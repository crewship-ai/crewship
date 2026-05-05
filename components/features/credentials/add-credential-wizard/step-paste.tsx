"use client"

import * as React from "react"
import { Eye, EyeOff, Loader2, CheckCircle2, XCircle, FileUp } from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import type { WizardState } from "./types"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

export function StepPaste({ state, setState }: Props) {
  const [showValue, setShowValue] = React.useState(false)
  const [bulkMode, setBulkMode] = React.useState(false)
  const debounceRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)

  // Auto-test debounced 800ms after paste — but skip for SECRET type
  // (no provider to test against) and for value === "" (cleared).
  React.useEffect(() => {
    if (state.type === "SECRET" || state.provider === "NONE" || !state.value.trim()) {
      return
    }
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(async () => {
      setState({ testing: true, testResult: null })
      try {
        const res = await fetch("/api/v1/credentials/test", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            provider: state.provider,
            type: state.type,
            value: state.value.trim(),
          }),
        })
        if (!res.ok) {
          setState({ testing: false, testResult: { valid: false, error: "Test request failed" } })
          return
        }
        const data = await res.json()
        setState({ testing: false, testResult: { valid: data.valid, error: data.error } })
      } catch {
        setState({ testing: false, testResult: { valid: false, error: "Network error" } })
      }
    }, 800)
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state.value])

  if (bulkMode) {
    return <BulkImport setBulkMode={setBulkMode} />
  }

  const placeholder =
    state.authMethod === "setup-token"
      ? "Paste output of `claude setup-token`..."
      : state.provider === "ANTHROPIC"
        ? "sk-ant-..."
        : state.provider === "OPENAI"
          ? "sk-proj-..."
          : state.provider === "GITHUB"
            ? "ghp_..."
            : "Paste value..."

  return (
    <div className="space-y-3">
      {state.authMethod === "setup-token" && (
        <div className="rounded-md border border-blue-500/25 bg-blue-500/[0.05] px-3 py-2.5 text-xs space-y-1.5">
          <p className="font-medium">How to get a setup token:</p>
          <ol className="list-decimal list-inside space-y-0.5 text-foreground/80">
            <li>Open a terminal on your computer</li>
            <li>Run: <code className="rounded bg-black/40 px-1 font-mono">claude setup-token</code></li>
            <li>Copy the entire output and paste below</li>
          </ol>
        </div>
      )}

      <div className="space-y-1.5">
        <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
          Value
        </label>
        <div className="relative">
          <input
            autoFocus
            type={showValue ? "text" : "password"}
            value={state.value}
            onChange={(e) => setState({ value: e.target.value, testResult: null })}
            placeholder={placeholder}
            className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 pr-10 text-sm font-mono outline-none focus:border-blue-400"
          />
          <button
            type="button"
            onClick={() => setShowValue((s) => !s)}
            className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
          >
            {showValue ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
          </button>
        </div>
      </div>

      {/* Test status */}
      <div className="flex items-center justify-between min-h-[24px]">
        <div className="text-xs">
          {state.testing && (
            <span className="inline-flex items-center gap-1.5 text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Testing key...
            </span>
          )}
          {!state.testing && state.testResult?.valid && (
            <span className="inline-flex items-center gap-1.5 text-emerald-400">
              <CheckCircle2 className="h-3.5 w-3.5" />
              Valid
            </span>
          )}
          {!state.testing && state.testResult && !state.testResult.valid && (
            <span className={cn("inline-flex items-center gap-1.5 text-red-400")}>
              <XCircle className="h-3.5 w-3.5" />
              {state.testResult.error || "Invalid"}
            </span>
          )}
        </div>
        <button
          type="button"
          onClick={() => setBulkMode(true)}
          className="text-[11px] text-muted-foreground hover:text-foreground inline-flex items-center gap-1"
        >
          <FileUp className="h-3 w-3" />
          Import from .env
        </button>
      </div>
    </div>
  )
}

function BulkImport({ setBulkMode }: { setBulkMode: (b: boolean) => void }) {
  const [text, setText] = React.useState("")
  const parsed = React.useMemo(() => {
    return text
      .split("\n")
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith("#"))
      .map((l) => {
        const eq = l.indexOf("=")
        if (eq === -1) return null
        const key = l.slice(0, eq).trim()
        let val = l.slice(eq + 1).trim()
        if ((val.startsWith('"') && val.endsWith('"')) || (val.startsWith("'") && val.endsWith("'"))) {
          val = val.slice(1, -1)
        }
        return { key, val }
      })
      .filter((x): x is { key: string; val: string } => x !== null && x.key.length > 0 && x.val.length > 0)
  }, [text])

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium">Bulk import from .env</span>
        <Button variant="ghost" size="sm" onClick={() => setBulkMode(false)}>Back</Button>
      </div>
      <textarea
        rows={8}
        autoFocus
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder="ANTHROPIC_API_KEY=sk-ant-...&#10;GH_TOKEN=ghp_..."
        className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-xs font-mono outline-none focus:border-blue-400"
      />
      <div className="rounded-md border border-amber-500/25 bg-amber-500/[0.05] px-3 py-2.5 text-xs">
        <strong>{parsed.length}</strong> credential{parsed.length === 1 ? "" : "s"} detected.
        Bulk-create flow lands in EPIC 2.5 — for now this is preview-only.
        Use the wizard for one credential at a time.
      </div>
    </div>
  )
}
