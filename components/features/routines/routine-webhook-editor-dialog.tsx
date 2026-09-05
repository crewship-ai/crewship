"use client"

import { useEffect, useState } from "react"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import type { PipelineWebhook, WebhookUpdateBody } from "@/hooks/use-pipeline-webhooks"

/**
 * RoutineWebhookEditorDialog — F21 / B9 (#2362): editing a webhook used to
 * mean delete + recreate, which mints a new token and breaks every sender
 * already configured with the old URL. This edits name, rate limit and the
 * inputs template in place; the token is never touched here, full stop.
 * Rotating the HMAC signing secret is the one explicit, opt-in exception —
 * gated behind a confirmation because it invalidates the OLD secret for
 * every sender still signing with it (the URL itself still survives).
 */
export interface RoutineWebhookEditorDialogProps {
  webhook: PipelineWebhook | null
  submitting?: boolean
  onCancel: () => void
  onSave: (body: WebhookUpdateBody) => void
}

export function RoutineWebhookEditorDialog({
  webhook,
  submitting,
  onCancel,
  onSave,
}: RoutineWebhookEditorDialogProps) {
  const [name, setName] = useState("")
  const [rateLimit, setRateLimit] = useState("60")
  const [inputsTemplateJson, setInputsTemplateJson] = useState("{}")
  const [enabled, setEnabled] = useState(true)
  const [rotateSecret, setRotateSecret] = useState(false)
  const [jsonError, setJsonError] = useState<string | null>(null)

  useEffect(() => {
    if (!webhook) return
    setName(webhook.name)
    setRateLimit(String(webhook.rate_limit_per_min || 60))
    setInputsTemplateJson(JSON.stringify(webhook.inputs_template ?? {}, null, 2))
    setEnabled(webhook.enabled)
    setRotateSecret(false)
    setJsonError(null)
  }, [webhook])

  if (!webhook) return null

  const submit = () => {
    let inputsTemplate: Record<string, unknown>
    try {
      inputsTemplate = inputsTemplateJson.trim() ? JSON.parse(inputsTemplateJson) : {}
    } catch {
      setJsonError("Inputs template must be valid JSON")
      return
    }
    setJsonError(null)
    const body: WebhookUpdateBody = {
      name,
      rate_limit_per_min: Math.max(1, parseInt(rateLimit, 10) || 60),
      inputs_template: inputsTemplate,
      enabled,
    }
    if (rotateSecret) {
      if (!confirm(
        "Rotate the signing secret? The URL stays the same, but every sender signing with the CURRENT secret will start failing HMAC verification until they update.",
      )) {
        return
      }
      body.rotate_secret = true
    }
    onSave(body)
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onCancel()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Edit webhook</DialogTitle>
          <DialogDescription>
            The public URL never changes here — only delete + recreate rotates it.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div>
            <Label htmlFor="wh-name">Name</Label>
            <Input id="wh-name" value={name} onChange={(e) => setName(e.target.value)} className="mt-1.5" />
          </div>
          <div>
            <Label htmlFor="wh-rate-limit">Rate limit (per minute)</Label>
            <Input
              id="wh-rate-limit"
              type="number"
              min={1}
              max={600}
              value={rateLimit}
              onChange={(e) => setRateLimit(e.target.value)}
              className="mt-1.5 w-32"
            />
          </div>
          <div>
            <Label htmlFor="wh-inputs-template">Inputs template (JSON)</Label>
            <textarea
              id="wh-inputs-template"
              value={inputsTemplateJson}
              onChange={(e) => setInputsTemplateJson(e.target.value)}
              className="mt-1.5 h-24 w-full resize-none rounded-md border border-white/[0.1] bg-background p-2.5 font-mono text-[12px] leading-relaxed"
            />
            {jsonError && <p className="mt-1 text-[11px] text-destructive">{jsonError}</p>}
            <p className="mt-1.5 text-[11px] text-muted-foreground">
              Merged with the request body to form routine inputs on every fire.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="wh-enabled" checked={enabled} onCheckedChange={setEnabled} />
            <Label htmlFor="wh-enabled" className="text-xs font-normal text-muted-foreground">Enabled</Label>
          </div>
          <div className="rounded-md border border-white/[0.08] p-3">
            <div className="flex items-center gap-2">
              <Switch id="wh-rotate" checked={rotateSecret} onCheckedChange={setRotateSecret} />
              <Label htmlFor="wh-rotate" className="text-xs font-normal">Rotate signing secret</Label>
            </div>
            <p className="mt-1.5 text-[11px] text-muted-foreground">
              Explicit and opt-in (F21). The URL/token is never rotated by this
              dialog — only the HMAC secret, and only when you check this.
            </p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={onCancel} disabled={submitting}>Cancel</Button>
          <Button onClick={submit} disabled={submitting}>{submitting ? "Saving…" : "Save"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
