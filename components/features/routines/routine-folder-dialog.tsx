"use client"

import * as React from "react"

import { Input } from "@/components/ui/input"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"

// New folder (ODCH-RT-01): a name. A folder only groups routines in the
// panel; a refusal (a name already taken) is shown in the dialog in the
// server's words and keeps what was typed.
export function RoutineFolderDialog({
  open,
  onOpenChange,
  onCreate,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Resolves to a refusal message, or null once the folder exists. */
  onCreate: (name: string) => Promise<string | null>
}) {
  const [name, setName] = React.useState("")
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const [busy, setBusy] = React.useState(false)
  const nameId = React.useId()
  React.useEffect(() => {
    if (open) {
      setName("")
      setRefusal(null)
    }
  }, [open])
  const trimmed = name.trim()
  const submit = async () => {
    if (!trimmed || busy) return
    setBusy(true)
    setRefusal(null)
    try {
      const message = await onCreate(trimmed)
      if (message) setRefusal(message)
      else onOpenChange(false)
    } finally {
      setBusy(false)
    }
  }
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" dirty={trimmed !== ""} onSubmit={() => void submit()}>
      <CreateSurfaceHeader
        concept="routines"
        title="New folder"
        description="A folder groups routines in the panel. A routine is in at most one folder."
        onClose={() => onOpenChange(false)}
      />
      <CreateSurfaceBody>
        <CreateSurfaceField label="Name" htmlFor={nameId} required>
          <Input
            id={nameId}
            className={CREATE_SURFACE_INPUT}
            value={name}
            maxLength={80}
            autoFocus
            onChange={(e) => {
              setName(e.target.value)
              if (refusal) setRefusal(null)
            }}
          />
        </CreateSurfaceField>
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />
      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel="Create folder"
        onPrimary={() => void submit()}
        primaryDisabled={!trimmed || busy}
        busy={busy}
      />
    </CreateSurface>
  )
}
