import { askConfirm, confirmHostMounted } from "@/components/ui/confirm-host"

/**
 * Asks, in the app's own dialog, whether to leave a Sheet that has changes not
 * saved yet. Without the shell's confirmation host (a standalone root, a test)
 * the Sheet stays open: nothing is lost, and no native dialog is used.
 * Resolves true only on a clear yes.
 */
export function confirmLeaveUnsavedSheet(): Promise<boolean> {
  if (confirmHostMounted()) {
    return askConfirm({
      title: "Leave this Sheet?",
      description: "It has changes that are not saved yet.",
      consequences: [{ tone: "lost", text: "Changes not saved yet are lost if you leave now." }],
      confirmLabel: "Leave without saving",
      cancelLabel: "Keep editing",
      destructive: true,
    })
  }
  return Promise.resolve(false)
}
