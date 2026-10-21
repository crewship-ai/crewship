"use client"

import * as React from "react"

import { ConfirmDialog, type Consequence } from "@/components/ui/confirm-dialog"

/**
 * One in-page confirmation for code that cannot render a dialog of its own:
 * a navigation guard, or a hook shared with another team's editor. The shell
 * mounts a single <ConfirmHost /> (components/providers.tsx); `askConfirm`
 * puts the question there and answers a promise.
 *
 * Callers check `confirmHostMounted()` first and keep their own path when it
 * is false — a standalone component root or a unit test has no shell.
 */
export interface ConfirmRequest {
  title: string
  description?: React.ReactNode
  consequences?: Consequence[]
  confirmLabel: string
  cancelLabel?: string
  destructive?: boolean
}

type Pending = ConfirmRequest & { resolve: (ok: boolean) => void }

let hosts = 0
let current: Pending | null = null
const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

export function confirmHostMounted(): boolean {
  return hosts > 0
}

/** Shows the question in the mounted host. A newer question answers the older one with "no". */
export function askConfirm(request: ConfirmRequest): Promise<boolean> {
  return new Promise((resolve) => {
    current?.resolve(false)
    current = { ...request, resolve }
    emit()
  })
}

function settle(ok: boolean) {
  const p = current
  current = null
  emit()
  p?.resolve(ok)
}

export function ConfirmHost() {
  const pending = React.useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => current,
    () => null,
  )
  React.useLayoutEffect(() => {
    hosts += 1
    return () => {
      hosts -= 1
    }
  }, [])
  return (
    <ConfirmDialog
      open={pending != null}
      onOpenChange={(open) => {
        if (!open && current) settle(false)
      }}
      title={pending?.title ?? ""}
      description={pending?.description}
      consequences={pending?.consequences}
      confirmLabel={pending?.confirmLabel ?? "OK"}
      cancelLabel={pending?.cancelLabel}
      destructive={pending?.destructive}
      onConfirm={() => settle(true)}
    />
  )
}
