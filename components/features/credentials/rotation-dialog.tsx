"use client"

import * as React from "react"
import { Eye, EyeOff, AlertTriangle, CheckCircle2, XCircle } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

type GraceMode = "immediate" | "24h" | "custom"

export interface RotationDialogProps {
  workspaceId: string
  credentialId: string
  credentialName: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onRotated: () => void
}

export function RotationDialog({
  workspaceId, credentialId, credentialName, open, onOpenChange, onRotated,
}: RotationDialogProps) {
  const [value, setValue] = React.useState("")
  const [showValue, setShowValue] = React.useState(false)
  const [grace, setGrace] = React.useState<GraceMode>("24h")
  const [customHours, setCustomHours] = React.useState(12)
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [testing, setTesting] = React.useState(false)
  const [testResult, setTestResult] = React.useState<{ valid: boolean; error?: string } | null>(null)
  const debounceRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)

  React.useEffect(() => {
    if (!open) {
      setValue("")
      setGrace("24h")
      setCustomHours(12)
      setSubmitting(false)
      setError(null)
      setTesting(false)
      setTestResult(null)
    }
  }, [open])

  // Auto-test debounced (mirrors AddCredentialWizard step 3 pattern).
  React.useEffect(() => {
    if (!value.trim()) return
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(async () => {
      setTesting(true)
      setTestResult(null)
      try {
        // We don't know the type/provider here without re-fetching the
        // credential — fall back to the per-credential test endpoint.
        const res = await apiFetch(`/api/v1/credentials/${credentialId}/test?workspace_id=${workspaceId}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ value: value.trim() }),
        })
        if (!res.ok) {
          setTestResult({ valid: false, error: "Test request failed" })
          setTesting(false)
          return
        }
        const data = await res.json()
        setTestResult({ valid: data.valid, error: data.error })
      } catch {
        setTestResult({ valid: false, error: "Network error" })
      } finally {
        setTesting(false)
      }
    }, 800)
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [value, credentialId, workspaceId])

  const graceSeconds = grace === "immediate" ? 0 : grace === "24h" ? 86400 : Math.max(0, customHours * 3600)

  const handleRotate = async () => {
    if (!value.trim()) return
    setSubmitting(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/credentials/${credentialId}/rotate?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ value: value.trim(), grace_seconds: graceSeconds }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(typeof data.error === "string" ? data.error : "Failed to rotate")
        return
      }
      onRotated()
      onOpenChange(false)
    } catch {
      setError("Network error")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Rotate <span className="font-mono">{credentialName}</span></DialogTitle>
          <DialogDescription>
            New value takes effect immediately. The old value stays usable during the grace
            window so in-flight agent runs don&apos;t break.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
              New value
            </label>
            <div className="relative">
              <input
                autoFocus
                type={showValue ? "text" : "password"}
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder="Paste the new token..."
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
            <div className="min-h-[16px] text-xs">
              {testing && (
                <span className="inline-flex items-center gap-1.5 text-muted-foreground">
                  <Spinner className="h-3 w-3" />
                  Testing...
                </span>
              )}
              {!testing && testResult?.valid && (
                <span className="inline-flex items-center gap-1.5 text-emerald-400">
                  <CheckCircle2 className="h-3 w-3" />
                  Valid
                </span>
              )}
              {!testing && testResult && !testResult.valid && (
                <span className="inline-flex items-center gap-1.5 text-amber-400">
                  <XCircle className="h-3 w-3" />
                  {testResult.error || "Could not validate (will rotate anyway)"}
                </span>
              )}
            </div>
          </div>

          <div className="space-y-1.5">
            <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
              Grace overlap
            </label>
            <div className="grid grid-cols-3 gap-2">
              {(["immediate", "24h", "custom"] as const).map((m) => (
                <button
                  key={m}
                  type="button"
                  onClick={() => setGrace(m)}
                  className={cn(
                    "rounded-md border bg-zinc-950 p-2.5 text-left text-xs transition-all",
                    grace === m
                      ? "border-blue-400 ring-2 ring-blue-400/20"
                      : "border-white/10 hover:border-white/25",
                  )}
                >
                  <div className="font-medium">
                    {m === "immediate" && "Immediate"}
                    {m === "24h" && "24 hours"}
                    {m === "custom" && "Custom"}
                  </div>
                  <div className="text-[11px] text-muted-foreground mt-0.5">
                    {m === "immediate" && "Old value dies now"}
                    {m === "24h" && "Recommended"}
                    {m === "custom" && "Set hours"}
                  </div>
                </button>
              ))}
            </div>
            {grace === "custom" && (
              <div className="flex items-center gap-2 mt-2">
                <input
                  type="number"
                  min={0}
                  max={168}
                  aria-label="Grace period in hours"
                  value={customHours}
                  onChange={(e) => setCustomHours(Number(e.target.value))}
                  className="w-24 bg-zinc-950 border border-white/15 rounded-md px-2 py-1 text-sm outline-none focus:border-blue-400"
                />
                <span className="text-xs text-muted-foreground">hours (max 168 = 7 days)</span>
              </div>
            )}
          </div>

          <div className="rounded-md border border-blue-500/25 bg-blue-500/[0.05] px-3 py-2.5 text-xs space-y-1">
            <p className="font-medium">After grace expires:</p>
            <ul className="list-disc list-inside text-foreground/80 space-y-0.5">
              <li>Old value is permanently scrubbed from the rotation row</li>
              <li>Sidecar fallback path stops retrying with old key</li>
              <li>Audit log records the ROTATE event for compliance</li>
            </ul>
          </div>

          {error && (
            <div className="rounded-md border border-red-500/30 bg-red-500/[0.05] p-2 text-xs text-red-400 flex items-center gap-1.5">
              <AlertTriangle className="h-3.5 w-3.5" />
              {error}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>Cancel</Button>
          <Button onClick={handleRotate} disabled={!value.trim() || submitting}>
            {submitting && <Spinner className="mr-2 h-4 w-4" />}
            Rotate
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
