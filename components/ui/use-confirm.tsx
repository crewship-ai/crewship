"use client"

import * as React from "react"

import { ConfirmDialog, type ConfirmDialogProps } from "@/components/ui/confirm-dialog"

/** Everything the dialog says; open state and the answer belong to the hook. */
export type ConfirmOptions = Omit<ConfirmDialogProps, "open" | "onOpenChange" | "onConfirm">

export interface UseConfirm {
  /**
   * Ask the question and wait for the answer: `true` on the confirm button,
   * `false` on Cancel, Escape, a click outside, a newer question or unmount.
   * The drop-in for `window.confirm()` in an async handler.
   */
  ask: (options: ConfirmOptions) => Promise<boolean>
  /** Render once, anywhere in the caller's tree. */
  dialog: React.ReactElement
}

/**
 * The promise form of `ConfirmDialog`, for handlers that used to read
 * `window.confirm()` inline. The dialog closes as soon as the person answers;
 * the caller then does the work and reports its own outcome, as it did after
 * the browser's prompt.
 */
export function useConfirm(): UseConfirm {
  const [options, setOptions] = React.useState<ConfirmOptions | null>(null)
  const [open, setOpen] = React.useState(false)
  const resolver = React.useRef<((answer: boolean) => void) | null>(null)

  const settle = React.useCallback((answer: boolean) => {
    const resolve = resolver.current
    resolver.current = null
    resolve?.(answer)
  }, [])

  React.useEffect(() => () => settle(false), [settle])

  const ask = React.useCallback(
    (next: ConfirmOptions) => {
      // One question at a time: an unanswered older one reads as Cancel.
      settle(false)
      setOptions(next)
      setOpen(true)
      return new Promise<boolean>((resolve) => {
        resolver.current = resolve
      })
    },
    [settle],
  )

  const onOpenChange = React.useCallback(
    (next: boolean) => {
      setOpen(next)
      if (!next) settle(false)
    },
    [settle],
  )

  const dialog = (
    <ConfirmDialog
      {...(options ?? { title: "", confirmLabel: "" })}
      open={open && options !== null}
      onOpenChange={onOpenChange}
      onConfirm={() => settle(true)}
    />
  )

  return { ask, dialog }
}
