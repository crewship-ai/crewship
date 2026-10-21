import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"

import { ConfirmHost } from "@/components/ui/confirm-host"

/** Mounts the shell's single in-page confirmation, as components/providers.tsx does. */
export function mountConfirmHost() {
  return render(<ConfirmHost />)
}

/** Clicks a button of the open confirmation and waits for it to close. */
export async function answerConfirm(label: string | RegExp) {
  const dialog = await screen.findByRole("alertdialog")
  fireEvent.click(within(dialog).getByRole("button", { name: label }))
  await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
}
