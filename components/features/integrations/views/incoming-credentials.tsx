"use client"

import { Copy } from "lucide-react"
import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import type { IncomingTarget } from "../incoming-model"

export interface Reveal {
  url?: string
  secret?: string
  target: IncomingTarget
}
export function absolute(path: string) {
  return new URL(path, window.location.origin).toString()
}
export async function copy(value: string) {
  try {
    await navigator.clipboard.writeText(value)
    toast.success("Copied")
  } catch {
    toast.error("Could not copy; select and save the value manually.")
  }
}
function RevealBody({
  reveal,
  onClose,
  footerDone = false,
}: {
  reveal: Reveal
  onClose: () => void
  /** Done lives in the surface's footer instead. */
  footerDone?: boolean
}) {
  return (
    <div className="space-y-4">
      <h3 className="text-sm font-medium">Save your endpoint credentials</h3>
      <p className="text-xs text-muted-foreground">
        Secret values are shown once. Save them in your sender before closing.
      </p>
      {reveal.url && (
        <CreateSurfaceField label="Receiving URL">
          <code
            data-testid="incoming-receiving-url"
            className="break-all rounded border p-3 text-xs"
          >
            {reveal.url}
          </code>
          <Button variant="soft" onClick={() => copy(reveal.url!)}>
            <Copy className="size-3.5" />
            Copy receiving URL
          </Button>
        </CreateSurfaceField>
      )}
      {reveal.secret && (
        <CreateSurfaceField label="Signing secret">
          <code className="break-all rounded border p-3 text-xs">
            {reveal.secret}
          </code>
          <Button variant="soft" onClick={() => copy(reveal.secret!)}>
            <Copy className="size-3.5" />
            Copy signing secret
          </Button>
        </CreateSurfaceField>
      )}
      {!reveal.url && (
        <p className="text-xs text-muted-foreground">
          Your receiving URL has not changed. Rotating the signing secret cannot
          recover a lost URL.
        </p>
      )}
      {!footerDone && <Button onClick={onClose}>Done</Button>}
    </div>
  )
}
export function RevealDialog({
  reveal,
  onClose,
}: {
  reveal: Reveal
  onClose: () => void
}) {
  return (
    <CreateSurface
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      size="md"
    >
      <CreateSurfaceHeader
        concept="integrations"
        context="Integrations"
        title="Endpoint credentials"
        onClose={onClose}
      />
      <CreateSurfaceBody>
        <RevealBody reveal={reveal} onClose={onClose} />
      </CreateSurfaceBody>
    </CreateSurface>
  )
}
