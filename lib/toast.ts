import { toast as sonnerToast, type ExternalToast } from "sonner"

/**
 * The app's toast helper: sonner's `toast` with the Harbor 2 feedback rules.
 *
 * - Success, info and every other kind leave after the Toaster's default
 *   4 s (`TOAST_DURATION_MS` in components/ui/sonner.tsx).
 * - An error stays until the person closes it: `toast.error` defaults to
 *   `duration: Infinity`. A caller that passes its own `duration` keeps it.
 *
 * Every method delegates to sonner at call time rather than copying sonner's
 * functions at import time, so a test that mocks or spies on sonner's
 * `toast` sees the call. Import `toast` from here, never from "sonner"
 * (guarded by lib/__tests__/toast-rules.test.ts).
 */

type SonnerToast = typeof sonnerToast
type Message = Parameters<SonnerToast["error"]>[0]

function error(message: Message, data?: ExternalToast) {
  return sonnerToast.error(message, { duration: Infinity, ...data })
}

const base = (...args: Parameters<SonnerToast>) => sonnerToast(...args)

export const toast: SonnerToast = Object.assign(base, {
  success: (...args: Parameters<SonnerToast["success"]>) => sonnerToast.success(...args),
  info: (...args: Parameters<SonnerToast["info"]>) => sonnerToast.info(...args),
  warning: (...args: Parameters<SonnerToast["warning"]>) => sonnerToast.warning(...args),
  error,
  custom: (...args: Parameters<SonnerToast["custom"]>) => sonnerToast.custom(...args),
  message: (...args: Parameters<SonnerToast["message"]>) => sonnerToast.message(...args),
  promise: ((...args: Parameters<SonnerToast["promise"]>) =>
    sonnerToast.promise(...args)) as SonnerToast["promise"],
  dismiss: (...args: Parameters<SonnerToast["dismiss"]>) => sonnerToast.dismiss(...args),
  loading: (...args: Parameters<SonnerToast["loading"]>) => sonnerToast.loading(...args),
  getHistory: () => sonnerToast.getHistory(),
  getToasts: () => sonnerToast.getToasts(),
})
